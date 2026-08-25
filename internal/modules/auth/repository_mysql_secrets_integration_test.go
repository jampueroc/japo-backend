//go:build integration

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/modules/auth"
	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
	"github.com/jorgeampuero/japo-backend/internal/testsupport"
)

// seedAccount creates a user to hang codes and tokens off.
func seedAccount(t *testing.T, repo *auth.MySQLRepository, email string) auth.User {
	t.Helper()

	user, err := repo.Create(context.Background(), auth.User{
		Email:        valueobject.MustEmail(email),
		PasswordHash: "$2a$10$fakehashfakehashfakeha",
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return user
}

func TestMySQLRepositoryEmailVerification(t *testing.T) {
	testsupport.Truncate(t, testDB, "progress", "email_verification_codes", "password_reset_tokens", "users")

	ctx := context.Background()
	repo := auth.NewMySQLRepository(testDB)
	user := seedAccount(t, repo, "verify@example.com")

	// A fresh account is unverified: the column starts NULL.
	if user.EmailVerified() {
		t.Fatal("a new account should not be verified")
	}

	t.Run("no code yet", func(t *testing.T) {
		_, err := repo.FindVerification(ctx, user.ID)
		if !errors.Is(err, auth.ErrVerificationNotFound) {
			t.Fatalf("got error %v, want %v", err, auth.ErrVerificationNotFound)
		}
	})

	expires := time.Now().UTC().Add(15 * time.Minute).Truncate(time.Second)
	if err := repo.PutVerification(ctx, auth.EmailVerification{
		UserID: user.ID, CodeHash: "hash-one", ExpiresAt: expires,
	}); err != nil {
		t.Fatalf("store code: %v", err)
	}

	t.Run("stores and reads it back", func(t *testing.T) {
		stored, err := repo.FindVerification(ctx, user.ID)
		if err != nil {
			t.Fatalf("read code: %v", err)
		}
		if stored.CodeHash != "hash-one" || stored.Attempts != 0 {
			t.Fatalf("got %+v, want hash-one with no attempts", stored)
		}
		if !stored.ExpiresAt.Equal(expires) {
			t.Fatalf("got expiry %v, want %v", stored.ExpiresAt, expires)
		}
	})

	t.Run("counts the wrong guesses", func(t *testing.T) {
		for range 3 {
			if err := repo.RecordVerificationAttempt(ctx, user.ID); err != nil {
				t.Fatalf("record attempt: %v", err)
			}
		}
		stored, err := repo.FindVerification(ctx, user.ID)
		if err != nil {
			t.Fatalf("read code: %v", err)
		}
		if stored.Attempts != 3 {
			t.Fatalf("got %d attempts, want 3", stored.Attempts)
		}
	})

	// Asking for a new code must replace the old one AND clear the counter,
	// otherwise a user who mistyped three times would be locked out of
	// their own fresh code.
	t.Run("a new code replaces the old one and resets the counter", func(t *testing.T) {
		if err := repo.PutVerification(ctx, auth.EmailVerification{
			UserID: user.ID, CodeHash: "hash-two", ExpiresAt: expires,
		}); err != nil {
			t.Fatalf("store second code: %v", err)
		}

		stored, err := repo.FindVerification(ctx, user.ID)
		if err != nil {
			t.Fatalf("read code: %v", err)
		}
		if stored.CodeHash != "hash-two" {
			t.Fatalf("got hash %q, want the new one", stored.CodeHash)
		}
		if stored.Attempts != 0 {
			t.Fatalf("got %d attempts after a new code, want 0", stored.Attempts)
		}
		if !stored.ConsumedAt.IsZero() {
			t.Fatal("the new code came back already consumed")
		}

		var rows int
		if err := testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM email_verification_codes WHERE user_id = ?`, user.ID.Int64()).Scan(&rows); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if rows != 1 {
			t.Fatalf("got %d live codes for the account, want exactly 1", rows)
		}
	})

	t.Run("consuming it is what stops a replay", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		if err := repo.ConsumeVerification(ctx, user.ID, now); err != nil {
			t.Fatalf("consume code: %v", err)
		}
		stored, err := repo.FindVerification(ctx, user.ID)
		if err != nil {
			t.Fatalf("read code: %v", err)
		}
		if stored.ConsumedAt.IsZero() {
			t.Fatal("the code is still usable after being consumed")
		}
		if stored.Usable(now, 5) {
			t.Fatal("a consumed code reports itself as usable")
		}
	})

	t.Run("marking the account verified", func(t *testing.T) {
		at := time.Now().UTC().Truncate(time.Second)
		verified, err := repo.MarkEmailVerified(ctx, user.ID, at)
		if err != nil {
			t.Fatalf("mark verified: %v", err)
		}
		if !verified.EmailVerified() {
			t.Fatal("the account is still unverified after being marked")
		}
		if !verified.EmailVerifiedAt.Equal(at) {
			t.Fatalf("got %v, want %v", verified.EmailVerifiedAt, at)
		}
	})

	t.Run("marking an account that does not exist", func(t *testing.T) {
		_, err := repo.MarkEmailVerified(ctx, valueobject.ID(999999), time.Now())
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("got error %v, want %v", err, auth.ErrUserNotFound)
		}
	})

	// Deleting the account must take its codes with it: a dangling code is
	// a credential nobody owns.
	t.Run("codes die with the account", func(t *testing.T) {
		victim := seedAccount(t, repo, "cascade@example.com")
		if err := repo.PutVerification(ctx, auth.EmailVerification{
			UserID: victim.ID, CodeHash: "doomed", ExpiresAt: expires,
		}); err != nil {
			t.Fatalf("store code: %v", err)
		}
		if _, err := testDB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, victim.ID.Int64()); err != nil {
			t.Fatalf("delete account: %v", err)
		}

		var rows int
		if err := testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM email_verification_codes WHERE user_id = ?`, victim.ID.Int64()).Scan(&rows); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if rows != 0 {
			t.Fatalf("got %d orphaned codes, want none", rows)
		}
	})
}

func TestMySQLRepositoryPasswordReset(t *testing.T) {
	testsupport.Truncate(t, testDB, "progress", "email_verification_codes", "password_reset_tokens", "users")

	ctx := context.Background()
	repo := auth.NewMySQLRepository(testDB)
	user := seedAccount(t, repo, "reset@example.com")
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	t.Run("an unknown token", func(t *testing.T) {
		_, err := repo.FindResetByHash(ctx, "nothing-like-it")
		if !errors.Is(err, auth.ErrResetNotFound) {
			t.Fatalf("got error %v, want %v", err, auth.ErrResetNotFound)
		}
	})

	if err := repo.PutReset(ctx, auth.PasswordReset{
		UserID: user.ID, TokenHash: "token-hash-one", ExpiresAt: expires,
	}); err != nil {
		t.Fatalf("store token: %v", err)
	}

	t.Run("is found by its hash", func(t *testing.T) {
		stored, err := repo.FindResetByHash(ctx, "token-hash-one")
		if err != nil {
			t.Fatalf("read token: %v", err)
		}
		if stored.UserID != user.ID {
			t.Fatalf("got owner %d, want %d", stored.UserID.Int64(), user.ID.Int64())
		}
		if !stored.Usable(time.Now().UTC()) {
			t.Fatal("a fresh token reports itself unusable")
		}
	})

	// Asking again must invalidate the previous link, so an old email
	// cannot be used after a newer one was requested.
	t.Run("a new request invalidates the previous link", func(t *testing.T) {
		if err := repo.PutReset(ctx, auth.PasswordReset{
			UserID: user.ID, TokenHash: "token-hash-two", ExpiresAt: expires,
		}); err != nil {
			t.Fatalf("store second token: %v", err)
		}

		if _, err := repo.FindResetByHash(ctx, "token-hash-one"); !errors.Is(err, auth.ErrResetNotFound) {
			t.Fatalf("the old link still works: %v", err)
		}
		if _, err := repo.FindResetByHash(ctx, "token-hash-two"); err != nil {
			t.Fatalf("the new link does not work: %v", err)
		}
	})

	t.Run("consuming it is what makes it single use", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		if err := repo.ConsumeReset(ctx, user.ID, now); err != nil {
			t.Fatalf("consume token: %v", err)
		}
		stored, err := repo.FindResetByHash(ctx, "token-hash-two")
		if err != nil {
			t.Fatalf("read token: %v", err)
		}
		if stored.Usable(now) {
			t.Fatal("a consumed token reports itself as usable")
		}
	})

	t.Run("changing the password", func(t *testing.T) {
		if err := repo.UpdatePassword(ctx, user.ID, "$2a$10$brandnewhashbrandnewha"); err != nil {
			t.Fatalf("update password: %v", err)
		}
		stored, err := repo.FindByID(ctx, user.ID)
		if err != nil {
			t.Fatalf("read account: %v", err)
		}
		if stored.PasswordHash != "$2a$10$brandnewhashbrandnewha" {
			t.Fatalf("got hash %q, want the new one", stored.PasswordHash)
		}
	})

	t.Run("changing the password of an account that does not exist", func(t *testing.T) {
		err := repo.UpdatePassword(ctx, valueobject.ID(999999), "whatever")
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("got error %v, want %v", err, auth.ErrUserNotFound)
		}
	})
}
