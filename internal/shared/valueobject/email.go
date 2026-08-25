// Package valueobject holds small, self validating value objects shared by
// the modules. It is infrastructure agnostic on purpose: no Fiber, no SQL.
package valueobject

import (
	"net/mail"
	"strings"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
)

// MaxEmailLength matches the users.email column width.
const MaxEmailLength = 255

// Email is a normalised, syntactically valid email address.
type Email struct {
	value string
}

// NewEmail validates and normalises raw (trimmed + lowercased).
func NewEmail(raw string) (Email, error) {
	normalised := strings.ToLower(strings.TrimSpace(raw))
	if normalised == "" {
		return Email{}, apperror.Validation("invalid_email", "email is required")
	}
	if len(normalised) > MaxEmailLength {
		return Email{}, apperror.Validation("invalid_email", "email is too long")
	}
	addr, err := mail.ParseAddress(normalised)
	if err != nil || addr.Address != normalised {
		return Email{}, apperror.Validation("invalid_email", "email is not a valid address").WithCause(err)
	}
	return Email{value: normalised}, nil
}

// MustEmail is a test/seed helper: it panics on invalid input.
func MustEmail(raw string) Email {
	email, err := NewEmail(raw)
	if err != nil {
		panic(err)
	}
	return email
}

// String returns the normalised address.
func (e Email) String() string { return e.value }

// IsZero reports whether the value object is unset.
func (e Email) IsZero() bool { return e.value == "" }
