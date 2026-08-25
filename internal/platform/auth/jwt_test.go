package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/platform/auth"
)

const (
	testSecret = "a-test-secret-that-is-long-enough-32"
	testIssuer = "japo-api"
)

func TestJWTManagerIssueAndVerify(t *testing.T) {
	t.Parallel()

	manager := auth.NewJWTManager(testSecret, testIssuer, time.Hour)

	token, expiresAt, err := manager.Issue(42)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if got := time.Until(expiresAt).Round(time.Minute); got != time.Hour {
		t.Fatalf("got ttl %v, want 1h", got)
	}

	claims, err := manager.Verify(token)
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if claims.UserID != 42 {
		t.Fatalf("got user id %d, want 42", claims.UserID)
	}
	if claims.Subject != "42" {
		t.Fatalf("got subject %q, want %q", claims.Subject, "42")
	}
}

func TestJWTManagerVerifyRejects(t *testing.T) {
	t.Parallel()

	manager := auth.NewJWTManager(testSecret, testIssuer, time.Hour)
	valid, _, err := manager.Issue(7)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	expiredManager := auth.NewJWTManager(testSecret, testIssuer, -time.Minute)
	expired, _, err := expiredManager.Issue(7)
	if err != nil {
		t.Fatalf("issue expired token: %v", err)
	}

	tests := []struct {
		name    string
		token   string
		manager *auth.JWTManager
		wantErr error
	}{
		{name: "garbage", token: "not-a-token", manager: manager, wantErr: auth.ErrInvalidToken},
		{name: "empty", token: "", manager: manager, wantErr: auth.ErrInvalidToken},
		{
			name:    "tampered payload",
			token:   strings.ToUpper(valid[:8]) + valid[8:],
			manager: manager,
			wantErr: auth.ErrInvalidToken,
		},
		{
			name:    "signed with another secret",
			token:   valid,
			manager: auth.NewJWTManager("another-secret-that-is-long-enough-x", testIssuer, time.Hour),
			wantErr: auth.ErrInvalidToken,
		},
		{
			name:    "issued for another issuer",
			token:   valid,
			manager: auth.NewJWTManager(testSecret, "someone-else", time.Hour),
			wantErr: auth.ErrInvalidToken,
		},
		{name: "expired", token: expired, manager: manager, wantErr: auth.ErrTokenExpired},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := tc.manager.Verify(tc.token); !errors.Is(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestPasswordHasher(t *testing.T) {
	t.Parallel()

	// Minimum cost keeps the unit suite fast; production uses BCRYPT_COST.
	hasher := auth.NewPasswordHasher(4)

	hash, err := hasher.Hash("supersecret1")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if hash == "supersecret1" {
		t.Fatal("the password was stored in clear text")
	}

	if err := hasher.Compare(hash, "supersecret1"); err != nil {
		t.Fatalf("compare matching password: %v", err)
	}
	if err := hasher.Compare(hash, "wrong-password"); !errors.Is(err, auth.ErrPasswordMismatch) {
		t.Fatalf("got error %v, want %v", err, auth.ErrPasswordMismatch)
	}
	if _, err := hasher.Hash(strings.Repeat("x", auth.MaxPasswordLength+1)); err == nil {
		t.Fatal("expected an error for a password longer than bcrypt allows")
	}
}
