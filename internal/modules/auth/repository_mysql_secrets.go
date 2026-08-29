package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// The verification code and the reset token live in their own tables, keyed
// one row per account: asking for a new one replaces the previous, so there
// is never more than one live secret per user per purpose.

const (
	markEmailVerifiedQuery = `UPDATE users SET email_verified_at = ? WHERE id = ?`

	updatePasswordQuery = `UPDATE users SET password_hash = ? WHERE id = ?`

	saveProfileQuery = `UPDATE users SET name = ?, gender = ?, birth_date = ?, timezone = ? WHERE id = ?`

	putVerificationQuery = `
		INSERT INTO email_verification_codes (user_id, code_hash, attempts, expires_at, consumed_at)
		VALUES (?, ?, 0, ?, NULL)
		ON DUPLICATE KEY UPDATE
			code_hash   = VALUES(code_hash),
			attempts    = 0,
			expires_at  = VALUES(expires_at),
			consumed_at = NULL,
			created_at  = CURRENT_TIMESTAMP`

	selectVerificationQuery = `
		SELECT user_id, code_hash, attempts, expires_at, consumed_at
		FROM email_verification_codes
		WHERE user_id = ?`

	recordVerificationAttemptQuery = `
		UPDATE email_verification_codes SET attempts = attempts + 1 WHERE user_id = ?`

	consumeVerificationQuery = `
		UPDATE email_verification_codes SET consumed_at = ? WHERE user_id = ? AND consumed_at IS NULL`

	putResetQuery = `
		INSERT INTO password_reset_tokens (user_id, token_hash, expires_at, consumed_at)
		VALUES (?, ?, ?, NULL)
		ON DUPLICATE KEY UPDATE
			token_hash  = VALUES(token_hash),
			expires_at  = VALUES(expires_at),
			consumed_at = NULL,
			created_at  = CURRENT_TIMESTAMP`

	selectResetByHashQuery = `
		SELECT user_id, token_hash, expires_at, consumed_at
		FROM password_reset_tokens
		WHERE token_hash = ?`

	consumeResetQuery = `
		UPDATE password_reset_tokens SET consumed_at = ? WHERE user_id = ? AND consumed_at IS NULL`
)

// MarkEmailVerified stamps the confirmation and returns the updated user.
func (r *MySQLRepository) MarkEmailVerified(ctx context.Context, id valueobject.ID, at time.Time) (User, error) {
	result, err := r.db.ExecContext(ctx, markEmailVerifiedQuery, at.UTC(), id.Int64())
	if err != nil {
		return User{}, fmt.Errorf("mark email verified: %w", err)
	}
	if err := requireOneRow(result, "mark email verified"); err != nil {
		return User{}, err
	}
	return r.FindByID(ctx, id)
}

// UpdatePassword replaces the stored hash.
func (r *MySQLRepository) UpdatePassword(ctx context.Context, id valueobject.ID, passwordHash string) error {
	result, err := r.db.ExecContext(ctx, updatePasswordQuery, passwordHash, id.Int64())
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return requireOneRow(result, "update password")
}

// SaveProfile stores the onboarding identity, replacing whatever was there.
// It is a full replacement rather than a merge: the client always sends the
// three fields together, so a partial write would only invite a stale copy to
// blank out what the user just typed.
func (r *MySQLRepository) SaveProfile(ctx context.Context, id valueobject.ID, profile Profile) (User, error) {
	var birthDate any
	if !profile.BirthDate.IsZero() {
		birthDate = profile.BirthDate.UTC().Format(dateLayout)
	}

	var timezone any
	if profile.Timezone != "" {
		timezone = profile.Timezone
	}

	result, err := r.db.ExecContext(ctx, saveProfileQuery,
		profile.Name, string(profile.Gender), birthDate, timezone, id.Int64())
	if err != nil {
		return User{}, fmt.Errorf("save profile: %w", err)
	}
	// RowsAffected is 0 when the values are unchanged, which is a perfectly
	// good idempotent write, so the read below is what proves the account
	// exists.
	if _, err := result.RowsAffected(); err != nil {
		return User{}, fmt.Errorf("save profile: read affected rows: %w", err)
	}

	return r.FindByID(ctx, id)
}

// PutVerification stores a code, replacing any previous one and resetting the
// attempt counter with it.
func (r *MySQLRepository) PutVerification(ctx context.Context, verification EmailVerification) error {
	_, err := r.db.ExecContext(ctx, putVerificationQuery,
		verification.UserID.Int64(),
		verification.CodeHash,
		verification.ExpiresAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("store verification code: %w", err)
	}
	return nil
}

// FindVerification loads the live code of an account.
func (r *MySQLRepository) FindVerification(ctx context.Context, userID valueobject.ID) (EmailVerification, error) {
	var (
		rawUserID  int64
		consumedAt sql.NullTime
		result     EmailVerification
	)

	err := r.db.QueryRowContext(ctx, selectVerificationQuery, userID.Int64()).Scan(
		&rawUserID,
		&result.CodeHash,
		&result.Attempts,
		&result.ExpiresAt,
		&consumedAt,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return EmailVerification{}, ErrVerificationNotFound
	case err != nil:
		return EmailVerification{}, fmt.Errorf("select verification code: %w", err)
	}

	if result.UserID, err = valueobject.NewID(rawUserID); err != nil {
		return EmailVerification{}, fmt.Errorf("stored verification owner: %w", err)
	}
	result.ExpiresAt = result.ExpiresAt.UTC()
	if consumedAt.Valid {
		result.ConsumedAt = consumedAt.Time.UTC()
	}
	return result, nil
}

// RecordVerificationAttempt counts one wrong guess.
func (r *MySQLRepository) RecordVerificationAttempt(ctx context.Context, userID valueobject.ID) error {
	if _, err := r.db.ExecContext(ctx, recordVerificationAttemptQuery, userID.Int64()); err != nil {
		return fmt.Errorf("record verification attempt: %w", err)
	}
	return nil
}

// ConsumeVerification marks the code as spent.
func (r *MySQLRepository) ConsumeVerification(ctx context.Context, userID valueobject.ID, at time.Time) error {
	if _, err := r.db.ExecContext(ctx, consumeVerificationQuery, at.UTC(), userID.Int64()); err != nil {
		return fmt.Errorf("consume verification code: %w", err)
	}
	return nil
}

// PutReset stores a reset token, replacing any previous one.
func (r *MySQLRepository) PutReset(ctx context.Context, reset PasswordReset) error {
	_, err := r.db.ExecContext(ctx, putResetQuery,
		reset.UserID.Int64(),
		reset.TokenHash,
		reset.ExpiresAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("store reset token: %w", err)
	}
	return nil
}

// FindResetByHash loads a reset token by its hash, which is what the link
// carries.
func (r *MySQLRepository) FindResetByHash(ctx context.Context, tokenHash string) (PasswordReset, error) {
	var (
		rawUserID  int64
		consumedAt sql.NullTime
		result     PasswordReset
	)

	err := r.db.QueryRowContext(ctx, selectResetByHashQuery, tokenHash).Scan(
		&rawUserID,
		&result.TokenHash,
		&result.ExpiresAt,
		&consumedAt,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return PasswordReset{}, ErrResetNotFound
	case err != nil:
		return PasswordReset{}, fmt.Errorf("select reset token: %w", err)
	}

	if result.UserID, err = valueobject.NewID(rawUserID); err != nil {
		return PasswordReset{}, fmt.Errorf("stored reset owner: %w", err)
	}
	result.ExpiresAt = result.ExpiresAt.UTC()
	if consumedAt.Valid {
		result.ConsumedAt = consumedAt.Time.UTC()
	}
	return result, nil
}

// ConsumeReset marks the token as spent, which is what makes it single use.
func (r *MySQLRepository) ConsumeReset(ctx context.Context, userID valueobject.ID, at time.Time) error {
	if _, err := r.db.ExecContext(ctx, consumeResetQuery, at.UTC(), userID.Int64()); err != nil {
		return fmt.Errorf("consume reset token: %w", err)
	}
	return nil
}

// requireOneRow turns "updated nothing" into the domain's not found error,
// which is the only way an UPDATE reports a missing account.
func requireOneRow(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: read affected rows: %w", operation, err)
	}
	if affected == 0 {
		return ErrUserNotFound
	}
	return nil
}
