package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// hashSecret is how codes and reset tokens are stored. They are short lived
// and high entropy (or attempt limited), so a plain SHA-256 is the right tool
// here: unlike a password, there is nothing to slow down an offline guesser
// against, and the point is simply that a database dump is not enough to
// verify an address or seize an account.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// sameSecret compares two hashes without leaking where they differ.
func sameSecret(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// silentEmail parses an address for the endpoints that must answer the same
// whatever happens. A malformed address is not an error there, it simply
// means there is no account to act on: reporting it would turn the endpoint
// into a way of probing which addresses exist.
func silentEmail(raw string) (valueobject.Email, bool) {
	address, err := valueobject.NewEmail(raw)
	return address, err == nil
}

// VerifyEmail confirms the address with the code that was emailed and opens a
// session. Every failure mode returns the same error on purpose: telling a
// caller apart "no such account" from "wrong code" would turn this endpoint
// into an account enumerator.
func (s *service) VerifyEmail(ctx context.Context, email, code string) (Session, error) {
	address, err := valueobject.NewEmail(email)
	if err != nil {
		return Session{}, ErrInvalidVerificationCode
	}

	user, err := s.repo.FindByEmail(ctx, address)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return Session{}, ErrInvalidVerificationCode
		}
		return Session{}, fmt.Errorf("verify email: look up account: %w", err)
	}

	// Already verified must NOT open a session: the code is the only thing
	// proving ownership here, so handing out a token for a known address
	// alone would be an authentication bypass.
	if user.EmailVerified() {
		return Session{}, ErrEmailAlreadyVerified
	}

	verification, err := s.verifications.FindVerification(ctx, user.ID)
	if err != nil {
		if errors.Is(err, ErrVerificationNotFound) {
			return Session{}, ErrInvalidVerificationCode
		}
		return Session{}, fmt.Errorf("verify email: look up code: %w", err)
	}

	now := s.clock().UTC()
	if !verification.Usable(now, s.policy.MaxVerificationAttempts) {
		return Session{}, ErrInvalidVerificationCode
	}

	if !sameSecret(verification.CodeHash, hashSecret(code)) {
		// Count the miss before answering: this is what bounds guessing a
		// six digit code to a handful of tries.
		if err := s.verifications.RecordVerificationAttempt(ctx, user.ID); err != nil {
			s.logger.ErrorContext(ctx, "could not record a verification attempt",
				slog.Int64("user_id", user.ID.Int64()), slog.Any("error", err))
		}
		return Session{}, ErrInvalidVerificationCode
	}

	if err := s.verifications.ConsumeVerification(ctx, user.ID, now); err != nil {
		return Session{}, fmt.Errorf("verify email: consume code: %w", err)
	}

	verified, err := s.repo.MarkEmailVerified(ctx, user.ID, now)
	if err != nil {
		return Session{}, fmt.Errorf("verify email: mark verified: %w", err)
	}

	// Confirming the address is activity too.
	if touched, err := s.repo.TouchActivity(ctx, verified.ID, DayIn(now, verified.Profile.Location())); err == nil {
		verified = touched
	} else {
		s.logger.WarnContext(ctx, "could not record activity on verification",
			slog.Int64("user_id", verified.ID.Int64()), slog.Any("error", err))
	}

	s.logger.InfoContext(ctx, "email verified", slog.Int64("user_id", verified.ID.Int64()))
	return s.openSession(verified)
}

// ResendVerification issues a fresh code, which invalidates the previous one.
// It says nothing about whether the account exists or is already verified.
func (s *service) ResendVerification(ctx context.Context, email string) error {
	address, ok := silentEmail(email)
	if !ok {
		return nil
	}

	user, err := s.repo.FindByEmail(ctx, address)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			s.logger.DebugContext(ctx, "verification resend for an unknown address")
			return nil
		}
		return fmt.Errorf("resend verification: look up account: %w", err)
	}
	if user.EmailVerified() {
		s.logger.DebugContext(ctx, "verification resend for an already verified address",
			slog.Int64("user_id", user.ID.Int64()))
		return nil
	}

	return s.issueVerificationCode(ctx, user)
}

// issueVerificationCode generates, stores and emails a code. The send is best
// effort: the account already exists and the user can ask for another one, so
// a mail outage must not roll back a registration.
func (s *service) issueVerificationCode(ctx context.Context, user User) error {
	code, err := s.secrets.VerificationCode()
	if err != nil {
		return fmt.Errorf("generate verification code: %w", err)
	}

	now := s.clock().UTC()
	verification := EmailVerification{
		UserID:    user.ID,
		CodeHash:  hashSecret(code),
		ExpiresAt: now.Add(s.policy.VerificationCodeTTL),
	}
	if err := s.verifications.PutVerification(ctx, verification); err != nil {
		return fmt.Errorf("store verification code: %w", err)
	}

	// A failed send is logged, never returned. The code is already stored,
	// the caller can ask for another one, and surfacing the failure would
	// tell an anonymous caller that this address is registered.
	if sendErr := s.notifier.SendVerificationCode(ctx, user.Email.String(), code); sendErr != nil {
		s.logger.ErrorContext(ctx, "could not send the verification email",
			slog.Int64("user_id", user.ID.Int64()), slog.Any("error", sendErr))
	} else {
		s.logger.InfoContext(ctx, "verification code sent",
			slog.Int64("user_id", user.ID.Int64()),
			slog.Duration("ttl", s.policy.VerificationCodeTTL),
		)
	}

	return nil
}
