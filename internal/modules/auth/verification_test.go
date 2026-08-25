package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/modules/auth"
	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// These cover the email verification and password reset flows. Most of the
// cases here are security properties rather than features: no enumeration, no
// unlimited guessing, no secret stored in the clear, no reusable link.

// harness bundles a service with the doubles behind it, so a test can assert
// on what was stored and what would have been emailed.
type harness struct {
	service  auth.Service
	repo     *stubRepository
	store    *stubSecretStore
	notifier *stubNotifier
}

type harnessOptions struct {
	user     *auth.User
	policy   auth.Policy
	secrets  stubSecrets
	notifier *stubNotifier
}

func newHarness(t *testing.T, opts harnessOptions) *harness {
	t.Helper()

	repo := &stubRepository{}
	if opts.user != nil {
		user := *opts.user
		repo.state = &user
		repo.findByEmailFn = func(context.Context, valueobject.Email) (auth.User, error) { return *repo.state, nil }
		repo.findByIDFn = func(context.Context, valueobject.ID) (auth.User, error) { return *repo.state, nil }
	}

	store := &stubSecretStore{}
	notifier := opts.notifier
	if notifier == nil {
		notifier = &stubNotifier{}
	}

	return &harness{
		service: auth.NewService(auth.ServiceDeps{
			Repo:          repo,
			Verifications: store,
			Resets:        store,
			Hasher:        stubHasher{},
			Tokens:        stubTokenIssuer{},
			Notifier:      notifier,
			Secrets:       opts.secrets,
			Policy:        opts.policy,
			Clock:         frozenClock(),
			Logger:        discardLogger(),
		}),
		repo:     repo,
		store:    store,
		notifier: notifier,
	}
}

func unverifiedUser() auth.User {
	user := existingUserWithActivity(auth.Activity{
		LastActiveDate:    auth.UTCDay(fixedNow),
		DistinctLoginDays: 1,
		StreakDays:        1,
	}, "hashed:supersecret1")
	user.EmailVerifiedAt = time.Time{}
	return user
}

func verifiedUser() auth.User {
	user := unverifiedUser()
	user.EmailVerifiedAt = fixedNow.Add(-24 * time.Hour)
	return user
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// --- registration and the gate --------------------------------------------

func TestServiceRegisterIssuesAVerificationCode(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessOptions{secrets: stubSecrets{code: "424242"}})

	if _, err := h.service.Register(context.Background(),
		auth.Credentials{Email: testEmail, Password: "supersecret1"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if len(h.notifier.codes) != 1 || h.notifier.codes[0] != "424242" {
		t.Fatalf("emailed codes %v, want exactly one 424242", h.notifier.codes)
	}
	// What is stored must be the hash, never the code itself: a dump of the
	// table must not let anyone verify somebody else's address.
	if h.store.verification.CodeHash == "424242" {
		t.Fatal("the verification code was stored in the clear")
	}
	if h.store.verification.CodeHash != sha256Hex("424242") {
		t.Fatalf("got code hash %q, want the sha256 of the code", h.store.verification.CodeHash)
	}
	if want := fixedNow.Add(15 * time.Minute); !h.store.verification.ExpiresAt.Equal(want) {
		t.Fatalf("got expiry %v, want %v", h.store.verification.ExpiresAt, want)
	}
}

func TestServiceRegisterHonoursTheVerificationGate(t *testing.T) {
	t.Parallel()

	t.Run("gate off: the session starts immediately", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, harnessOptions{})
		session, err := h.service.Register(context.Background(),
			auth.Credentials{Email: testEmail, Password: "supersecret1"})
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		if session.Token == "" {
			t.Fatal("no token issued with the gate off")
		}
	})

	t.Run("gate on: no token until the address is confirmed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, harnessOptions{policy: auth.Policy{RequireVerifiedEmail: true}})
		session, err := h.service.Register(context.Background(),
			auth.Credentials{Email: testEmail, Password: "supersecret1"})
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		// This is the gate: it is enforced by not issuing a credential at
		// all, so a client cannot skip it by ignoring a flag.
		if session.Token != "" {
			t.Fatalf("a token was issued before verification: %q", session.Token)
		}
		if session.User.ID.IsZero() {
			t.Fatal("the account should still come back so the client can show the code screen")
		}
	})
}

func TestServiceLoginHonoursTheVerificationGate(t *testing.T) {
	t.Parallel()

	unverified := unverifiedUser()

	t.Run("gate on: an unverified account cannot sign in", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, harnessOptions{
			user:   &unverified,
			policy: auth.Policy{RequireVerifiedEmail: true},
		})

		_, err := h.service.Login(context.Background(),
			auth.Credentials{Email: testEmail, Password: "supersecret1"})
		if !errors.Is(err, auth.ErrEmailNotVerified) {
			t.Fatalf("got error %v, want %v", err, auth.ErrEmailNotVerified)
		}
	})

	t.Run("gate off: verification does not block anyone", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, harnessOptions{user: &unverified})

		session, err := h.service.Login(context.Background(),
			auth.Credentials{Email: testEmail, Password: "supersecret1"})
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		if session.Token == "" {
			t.Fatal("no token issued with the gate off")
		}
	})
}

// --- verifying ------------------------------------------------------------

func TestServiceVerifyEmail(t *testing.T) {
	t.Parallel()

	const code = "424242"

	t.Run("the right code opens the session", func(t *testing.T) {
		t.Parallel()

		user := unverifiedUser()
		h := newHarness(t, harnessOptions{user: &user, policy: auth.Policy{RequireVerifiedEmail: true}})
		h.store.PutVerification(context.Background(), auth.EmailVerification{
			UserID: user.ID, CodeHash: sha256Hex(code), ExpiresAt: fixedNow.Add(time.Minute),
		})

		session, err := h.service.VerifyEmail(context.Background(), testEmail, code)
		if err != nil {
			t.Fatalf("verify email: %v", err)
		}
		if session.Token == "" {
			t.Fatal("verifying did not open a session")
		}
		if !session.User.EmailVerified() {
			t.Fatal("the returned account is still unverified")
		}
		if !h.store.consumedVerification {
			t.Fatal("the code was not consumed, so it could be replayed")
		}
	})

	t.Run("an already verified address must not hand out a token", func(t *testing.T) {
		t.Parallel()

		// The code is the only proof of ownership here. Returning a
		// session for a known address with no valid code would be an
		// authentication bypass: anyone knowing the email would be in.
		user := verifiedUser()
		h := newHarness(t, harnessOptions{user: &user})

		session, err := h.service.VerifyEmail(context.Background(), testEmail, code)
		if !errors.Is(err, auth.ErrEmailAlreadyVerified) {
			t.Fatalf("got error %v, want %v", err, auth.ErrEmailAlreadyVerified)
		}
		if session.Token != "" {
			t.Fatalf("a token was issued without checking a code: %q", session.Token)
		}
	})

	tests := []struct {
		name         string
		verification *auth.EmailVerification
		user         *auth.User
		code         string
		wantAttempt  bool
	}{
		{
			name: "a wrong code is counted",
			verification: &auth.EmailVerification{
				CodeHash: sha256Hex(code), ExpiresAt: fixedNow.Add(time.Minute),
			},
			code:        "000000",
			wantAttempt: true,
		},
		{
			name: "an expired code",
			verification: &auth.EmailVerification{
				CodeHash: sha256Hex(code), ExpiresAt: fixedNow.Add(-time.Second),
			},
			code: code,
		},
		{
			name: "a code already used",
			verification: &auth.EmailVerification{
				CodeHash: sha256Hex(code), ExpiresAt: fixedNow.Add(time.Minute), ConsumedAt: fixedNow,
			},
			code: code,
		},
		{
			name: "a code with too many guesses behind it",
			verification: &auth.EmailVerification{
				CodeHash: sha256Hex(code), ExpiresAt: fixedNow.Add(time.Minute), Attempts: 5,
			},
			code: code,
		},
		{
			name: "no code at all",
			code: code,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			user := unverifiedUser()
			h := newHarness(t, harnessOptions{user: &user})
			if tc.verification != nil {
				verification := *tc.verification
				verification.UserID = user.ID
				h.store.PutVerification(context.Background(), verification)
				// PutVerification resets the counter, as the real one does.
				h.store.verification.Attempts = verification.Attempts
				h.store.verification.ConsumedAt = verification.ConsumedAt
			}

			_, err := h.service.VerifyEmail(context.Background(), testEmail, tc.code)
			if !errors.Is(err, auth.ErrInvalidVerificationCode) {
				t.Fatalf("got error %v, want %v", err, auth.ErrInvalidVerificationCode)
			}
			if tc.wantAttempt && h.store.attemptCalls != 1 {
				t.Fatalf("recorded %d attempts, want 1: guessing must be bounded", h.store.attemptCalls)
			}
			if !tc.wantAttempt && h.store.attemptCalls != 0 {
				t.Fatalf("recorded %d attempts for a code that was never compared", h.store.attemptCalls)
			}
		})
	}

	// An unknown address must fail exactly like a wrong code, or the
	// endpoint becomes an account enumerator.
	t.Run("an unknown address is indistinguishable from a wrong code", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, harnessOptions{})
		_, err := h.service.VerifyEmail(context.Background(), "nobody@example.com", code)
		if !errors.Is(err, auth.ErrInvalidVerificationCode) {
			t.Fatalf("got error %v, want %v", err, auth.ErrInvalidVerificationCode)
		}
	})
}

func TestServiceResendVerification(t *testing.T) {
	t.Parallel()

	t.Run("issues a fresh code", func(t *testing.T) {
		t.Parallel()

		user := unverifiedUser()
		h := newHarness(t, harnessOptions{user: &user, secrets: stubSecrets{code: "999111"}})

		if err := h.service.ResendVerification(context.Background(), testEmail); err != nil {
			t.Fatalf("resend: %v", err)
		}
		if len(h.notifier.codes) != 1 || h.notifier.codes[0] != "999111" {
			t.Fatalf("emailed codes %v, want one 999111", h.notifier.codes)
		}
	})

	// Both of these must look like success and send nothing.
	for _, tc := range []struct {
		name string
		user *auth.User
	}{
		{name: "says nothing about an unknown address"},
		{name: "says nothing about an already verified address", user: func() *auth.User { u := verifiedUser(); return &u }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, harnessOptions{user: tc.user})
			if err := h.service.ResendVerification(context.Background(), testEmail); err != nil {
				t.Fatalf("resend must not report anything, got %v", err)
			}
			if len(h.notifier.codes) != 0 {
				t.Fatalf("sent %d emails, want none", len(h.notifier.codes))
			}
		})
	}
}

// --- password reset -------------------------------------------------------

func TestServiceForgotPassword(t *testing.T) {
	t.Parallel()

	t.Run("emails a link and stores only its hash", func(t *testing.T) {
		t.Parallel()

		user := verifiedUser()
		h := newHarness(t, harnessOptions{user: &user, secrets: stubSecrets{token: "a-long-random-token"}})

		if err := h.service.ForgotPassword(context.Background(), testEmail); err != nil {
			t.Fatalf("forgot password: %v", err)
		}
		if len(h.notifier.tokens) != 1 || h.notifier.tokens[0] != "a-long-random-token" {
			t.Fatalf("emailed tokens %v, want one", h.notifier.tokens)
		}
		if h.store.reset.TokenHash == "a-long-random-token" {
			t.Fatal("the reset token was stored in the clear")
		}
		if h.store.reset.TokenHash != sha256Hex("a-long-random-token") {
			t.Fatalf("got token hash %q, want the sha256 of the token", h.store.reset.TokenHash)
		}
	})

	t.Run("says nothing about an unknown address", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, harnessOptions{})
		if err := h.service.ForgotPassword(context.Background(), "nobody@example.com"); err != nil {
			t.Fatalf("forgot password must not report anything, got %v", err)
		}
		if len(h.notifier.tokens) != 0 {
			t.Fatalf("sent %d emails, want none", len(h.notifier.tokens))
		}
	})
}

func TestServiceResetPassword(t *testing.T) {
	t.Parallel()

	const token = "a-long-random-token"

	t.Run("replaces the password and burns the token", func(t *testing.T) {
		t.Parallel()

		user := unverifiedUser()
		h := newHarness(t, harnessOptions{user: &user})
		h.store.PutReset(context.Background(), auth.PasswordReset{
			UserID: user.ID, TokenHash: sha256Hex(token), ExpiresAt: fixedNow.Add(time.Hour),
		})

		if err := h.service.ResetPassword(context.Background(), token, "brandnew2026"); err != nil {
			t.Fatalf("reset password: %v", err)
		}
		if len(h.repo.updatedPasswords) != 1 {
			t.Fatalf("stored %d passwords, want 1", len(h.repo.updatedPasswords))
		}
		if h.repo.updatedPasswords[0] == "brandnew2026" {
			t.Fatal("the new password was stored in the clear")
		}
		if !h.store.consumedReset {
			t.Fatal("the token was not consumed, so the link would still work")
		}
		// Receiving the email proves the address belongs to them.
		if h.repo.verifiedCalls == 0 {
			t.Fatal("a completed reset should also confirm the address")
		}
	})

	t.Run("a spent link cannot be reused", func(t *testing.T) {
		t.Parallel()

		user := verifiedUser()
		h := newHarness(t, harnessOptions{user: &user})
		h.store.PutReset(context.Background(), auth.PasswordReset{
			UserID: user.ID, TokenHash: sha256Hex(token), ExpiresAt: fixedNow.Add(time.Hour),
		})
		if err := h.service.ResetPassword(context.Background(), token, "brandnew2026"); err != nil {
			t.Fatalf("first reset: %v", err)
		}

		err := h.service.ResetPassword(context.Background(), token, "another2026x")
		if !errors.Is(err, auth.ErrInvalidResetToken) {
			t.Fatalf("got error %v, want %v", err, auth.ErrInvalidResetToken)
		}
		if len(h.repo.updatedPasswords) != 1 {
			t.Fatalf("the password was changed %d times, want 1", len(h.repo.updatedPasswords))
		}
	})

	tests := []struct {
		name     string
		reset    *auth.PasswordReset
		token    string
		password string
		wantErr  error
	}{
		{
			name:     "an unknown token",
			token:    "not-the-token",
			password: "brandnew2026",
			wantErr:  auth.ErrInvalidResetToken,
		},
		{
			name: "an expired token",
			reset: &auth.PasswordReset{
				TokenHash: sha256Hex(token), ExpiresAt: fixedNow.Add(-time.Second),
			},
			token:    token,
			password: "brandnew2026",
			wantErr:  auth.ErrInvalidResetToken,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			user := verifiedUser()
			h := newHarness(t, harnessOptions{user: &user})
			if tc.reset != nil {
				reset := *tc.reset
				reset.UserID = user.ID
				h.store.PutReset(context.Background(), reset)
			}

			err := h.service.ResetPassword(context.Background(), tc.token, tc.password)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
			if len(h.repo.updatedPasswords) != 0 {
				t.Fatal("the password was changed despite the failure")
			}
		})
	}

	t.Run("the new password still has to pass the policy", func(t *testing.T) {
		t.Parallel()

		user := verifiedUser()
		h := newHarness(t, harnessOptions{user: &user})
		h.store.PutReset(context.Background(), auth.PasswordReset{
			UserID: user.ID, TokenHash: sha256Hex(token), ExpiresAt: fixedNow.Add(time.Hour),
		})

		err := h.service.ResetPassword(context.Background(), token, "short")
		if kind := apperror.KindOf(err); err == nil || kind != apperror.KindValidation {
			t.Fatalf("got %v (kind %v), want a validation error", err, kind)
		}
		// And a rejected password must not burn the link.
		if h.store.consumedReset {
			t.Fatal("the token was consumed by a rejected password")
		}
	})

	t.Run("a password over bcrypt's byte limit is rejected, not truncated", func(t *testing.T) {
		t.Parallel()

		user := verifiedUser()
		h := newHarness(t, harnessOptions{user: &user})
		h.store.PutReset(context.Background(), auth.PasswordReset{
			UserID: user.ID, TokenHash: sha256Hex(token), ExpiresAt: fixedNow.Add(time.Hour),
		})

		// 30 kana are 90 bytes.
		err := h.service.ResetPassword(context.Background(), token, strings.Repeat("か", 29)+"1")
		if kind := apperror.KindOf(err); err == nil || kind != apperror.KindValidation {
			t.Fatalf("got %v (kind %v), want a validation error", err, kind)
		}
	})
}
