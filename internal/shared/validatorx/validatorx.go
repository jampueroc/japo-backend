// Package validatorx wraps go-playground/validator with the project defaults:
// JSON field names in the error output, a couple of custom rules, and
// translation of validation failures into *apperror.Error.
package validatorx

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-playground/validator/v10"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
)

// Validator validates DTOs before they reach the service layer.
type Validator struct {
	v *validator.Validate
}

// New builds the shared validator instance. A single instance is safe for
// concurrent use and caches struct metadata, so build it once at startup and
// inject it.
func New() (*Validator, error) {
	v := validator.New(validator.WithRequiredStructEnabled())

	// Report the JSON name of the field, not the Go name.
	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
		if name == "-" || name == "" {
			return field.Name
		}
		return name
	})

	if err := v.RegisterValidation("password", validatePassword); err != nil {
		return nil, fmt.Errorf("register password rule: %w", err)
	}
	if err := v.RegisterValidation("notblank", validateNotBlank); err != nil {
		return nil, fmt.Errorf("register notblank rule: %w", err)
	}
	if err := v.RegisterValidation("maxbytes", validateMaxBytes); err != nil {
		return nil, fmt.Errorf("register maxbytes rule: %w", err)
	}

	return &Validator{v: v}, nil
}

// Struct validates s and returns a KindValidation *apperror.Error listing
// every failed field, or nil when the payload is valid.
func (val *Validator) Struct(s any) error {
	err := val.v.Struct(s)
	if err == nil {
		return nil
	}

	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		// Programming error: a non struct was passed in.
		return apperror.Internal(fmt.Errorf("validator misuse: %w", err))
	}

	var validationErrs validator.ValidationErrors
	if !errors.As(err, &validationErrs) {
		return apperror.Internal(fmt.Errorf("validate struct: %w", err))
	}

	appErr := apperror.Validation("validation_failed", "the request payload is invalid").WithCause(err)
	for _, fieldErr := range validationErrs {
		appErr.Fields = append(appErr.Fields, apperror.FieldError{
			Field:   fieldErr.Field(),
			Rule:    fieldErr.Tag(),
			Message: messageFor(fieldErr),
		})
	}
	return appErr
}

// validatePassword enforces a pragmatic policy: at least one letter and one
// digit. Length is expressed with the standard min/max tags.
func validatePassword(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	var hasLetter, hasDigit bool
	for _, r := range value {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

// validateMaxBytes bounds a string by its encoded length rather than by its
// rune count, which is what the standard max rule measures. bcrypt truncates
// at 72 bytes, and in an app about Japanese a password of 30 characters is
// easily 90 bytes: measuring runes there would accept a password the hasher
// then refuses.
func validateMaxBytes(fl validator.FieldLevel) bool {
	limit, err := strconv.Atoi(fl.Param())
	if err != nil {
		return false
	}
	return len(fl.Field().String()) <= limit
}

// validateNotBlank rejects strings made only of whitespace.
func validateNotBlank(fl validator.FieldLevel) bool {
	return strings.TrimSpace(fl.Field().String()) != ""
}

func messageFor(fieldErr validator.FieldError) string {
	switch fieldErr.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "min":
		return fmt.Sprintf("must be at least %s characters long", fieldErr.Param())
	case "max":
		return fmt.Sprintf("must be at most %s characters long", fieldErr.Param())
	case "maxbytes":
		return fmt.Sprintf("must be at most %s bytes long; accented and Japanese characters count as more than one",
			fieldErr.Param())
	case "password":
		return "must contain at least one letter and one digit"
	case "notblank":
		return "must not be blank"
	case "oneof":
		return fmt.Sprintf("must be one of: %s", strings.ReplaceAll(fieldErr.Param(), " ", ", "))
	case "json":
		return "must be valid JSON"
	default:
		return fmt.Sprintf("failed the %q rule", fieldErr.Tag())
	}
}
