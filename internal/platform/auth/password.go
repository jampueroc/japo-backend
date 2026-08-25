package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// MaxPasswordLength is the hard limit of bcrypt: longer inputs are rejected
// by the library instead of being silently truncated.
const MaxPasswordLength = 72

// ErrPasswordMismatch is returned when the password does not match the hash.
var ErrPasswordMismatch = errors.New("password does not match")

// PasswordHasher hashes and verifies passwords with bcrypt. A cost of 10 is
// a sensible compromise on a Raspberry Pi (roughly 100 ms per hash).
type PasswordHasher struct {
	cost int
}

// NewPasswordHasher builds a hasher; an out of range cost falls back to the
// bcrypt default.
func NewPasswordHasher(cost int) *PasswordHasher {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	return &PasswordHasher{cost: cost}
}

// Hash returns the bcrypt hash of plain.
func (h *PasswordHasher) Hash(plain string) (string, error) {
	if len(plain) > MaxPasswordLength {
		return "", fmt.Errorf("hash password: longer than %d bytes", MaxPasswordLength)
	}
	digest, err := bcrypt.GenerateFromPassword([]byte(plain), h.cost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(digest), nil
}

// Compare checks plain against hash. It returns ErrPasswordMismatch when they
// differ, and a wrapped error when the stored hash itself is unusable.
func (h *PasswordHasher) Compare(hash, plain string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return ErrPasswordMismatch
	default:
		return fmt.Errorf("compare password: %w", err)
	}
}
