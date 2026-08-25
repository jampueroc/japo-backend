package validatorx_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/validatorx"
)

type credentials struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=8,maxbytes=72,password"`
}

func newValidator(t *testing.T) *validatorx.Validator {
	t.Helper()

	v, err := validatorx.New()
	if err != nil {
		t.Fatalf("build validator: %v", err)
	}
	return v
}

// The upper bound is bcrypt's, and bcrypt counts bytes. The stock max rule
// counts runes, so a password of 30 Japanese characters (88 bytes) would sail
// past it and only blow up later, in the hasher.
func TestValidatorPasswordLengthIsMeasuredInBytes(t *testing.T) {
	t.Parallel()

	v := newValidator(t)

	tests := []struct {
		name       string
		password   string
		wantReject bool
	}{
		{name: "ascii within the limit", password: strings.Repeat("a", 71) + "1"},
		{name: "ascii over the limit", password: strings.Repeat("a", 72) + "1", wantReject: true},
		{
			// 24 runes, 72 bytes exactly: the last one that fits.
			name:     "japanese exactly at the byte limit",
			password: strings.Repeat("か", 23) + "1",
		},
		{
			// 30 runes but 88 bytes: the case the rune based rule missed.
			name:       "japanese under the rune limit but over the byte limit",
			password:   strings.Repeat("か", 29) + "1",
			wantReject: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := v.Struct(credentials{Email: "learner@example.com", Password: tc.password})
			if tc.wantReject {
				if err == nil {
					t.Fatalf("accepted a password of %d bytes, want it rejected", len(tc.password))
				}
				var appErr *apperror.Error
				if !errors.As(err, &appErr) || appErr.Kind != apperror.KindValidation {
					t.Fatalf("got %v, want a validation error", err)
				}
				// The message has to name bytes, otherwise it tells someone
				// who typed 30 characters that the limit is 72 characters.
				if !strings.Contains(appErr.Fields[0].Message, "bytes") {
					t.Fatalf("got message %q, want it to talk about bytes", appErr.Fields[0].Message)
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected a password of %d bytes: %v", len(tc.password), err)
			}
		})
	}
}

func TestValidatorReportsEveryFailedField(t *testing.T) {
	t.Parallel()

	v := newValidator(t)

	err := v.Struct(credentials{Email: "not-an-email", Password: "short"})

	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("got %v, want an application error", err)
	}
	if len(appErr.Fields) != 2 {
		t.Fatalf("got %d field errors, want one per invalid field: %+v", len(appErr.Fields), appErr.Fields)
	}
	// Field names must be the JSON ones: the client renders them next to its
	// own inputs.
	for _, field := range appErr.Fields {
		if field.Field != "email" && field.Field != "password" {
			t.Fatalf("got field name %q, want the JSON name", field.Field)
		}
	}
}

// The custom rule is what stops a password made only of letters or only of
// digits.
func TestValidatorPasswordNeedsALetterAndADigit(t *testing.T) {
	t.Parallel()

	v := newValidator(t)

	for _, password := range []string{"onlyletters", "12345678", "ながいパスワード"} {
		if err := v.Struct(credentials{Email: "learner@example.com", Password: password}); err == nil {
			t.Fatalf("accepted %q, want it rejected", password)
		}
	}
	if err := v.Struct(credentials{Email: "learner@example.com", Password: "nihongo2026"}); err != nil {
		t.Fatalf("rejected a valid password: %v", err)
	}
}
