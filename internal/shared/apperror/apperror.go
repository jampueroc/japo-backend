// Package apperror defines the application level error type shared by every
// module. It is transport agnostic: it knows nothing about Fiber, HTTP
// handlers or the database, it only classifies failures so that the edge of
// the application (see internal/shared/httpx) can render them consistently.
package apperror

import (
	"errors"
	"fmt"
	"net/http"
)

// Kind classifies an error so it can be mapped to a transport status code.
type Kind uint8

const (
	// KindInternal is the zero value: an unexpected failure. Never exposed.
	KindInternal Kind = iota
	// KindValidation is a malformed or semantically invalid request payload.
	KindValidation
	// KindUnauthorized means the caller is unknown or its token is invalid.
	KindUnauthorized
	// KindForbidden means the caller is known but not allowed.
	KindForbidden
	// KindNotFound means the requested resource does not exist.
	KindNotFound
	// KindConflict means the request collides with the current state.
	KindConflict
	// KindTooManyRequests means the caller tripped a rate limit.
	KindTooManyRequests
	// KindUnavailable means a downstream dependency is not reachable.
	KindUnavailable
)

// HTTPStatus maps a Kind to its HTTP status code.
func (k Kind) HTTPStatus() int {
	switch k {
	case KindValidation:
		return http.StatusBadRequest
	case KindUnauthorized:
		return http.StatusUnauthorized
	case KindForbidden:
		return http.StatusForbidden
	case KindNotFound:
		return http.StatusNotFound
	case KindConflict:
		return http.StatusConflict
	case KindTooManyRequests:
		return http.StatusTooManyRequests
	case KindUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// FieldError describes a single failed validation rule.
type FieldError struct {
	Field   string `json:"field"`
	Rule    string `json:"rule,omitempty"`
	Message string `json:"message"`
}

// Error is the application error. Message is safe to return to clients; the
// wrapped err is for logs only and is never rendered.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Fields  []FieldError
	err     error
}

func (e *Error) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the wrapped cause to errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.err }

// WithCause attaches (or replaces) the underlying cause. It returns e so it
// can be used inline.
func (e *Error) WithCause(err error) *Error {
	e.err = err
	return e
}

// WithFields attaches validation details.
func (e *Error) WithFields(fields ...FieldError) *Error {
	e.Fields = append(e.Fields, fields...)
	return e
}

func newError(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

// Validation builds a 400 error.
func Validation(code, message string) *Error { return newError(KindValidation, code, message) }

// Unauthorized builds a 401 error.
func Unauthorized(code, message string) *Error { return newError(KindUnauthorized, code, message) }

// Forbidden builds a 403 error.
func Forbidden(code, message string) *Error { return newError(KindForbidden, code, message) }

// NotFound builds a 404 error.
func NotFound(code, message string) *Error { return newError(KindNotFound, code, message) }

// Conflict builds a 409 error.
func Conflict(code, message string) *Error { return newError(KindConflict, code, message) }

// TooManyRequests builds a 429 error.
func TooManyRequests(code, message string) *Error {
	return newError(KindTooManyRequests, code, message)
}

// Unavailable builds a 503 error.
func Unavailable(code, message string) *Error { return newError(KindUnavailable, code, message) }

// Internal builds a 500 error whose cause is kept for logging only.
func Internal(err error) *Error {
	return newError(KindInternal, "internal_error", "something went wrong").WithCause(err)
}

// From normalises any error into an *Error. Errors that are not already
// application errors become opaque internal errors.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return Internal(err)
}

// KindOf reports the Kind of err, defaulting to KindInternal.
func KindOf(err error) Kind {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.Kind
	}
	return KindInternal
}

// HTTPStatus maps any error to an HTTP status code.
func HTTPStatus(err error) int { return KindOf(err).HTTPStatus() }
