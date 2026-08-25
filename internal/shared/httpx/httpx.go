// Package httpx renders the standard JSON responses of the API. Together with
// internal/platform it is the only place allowed to import Fiber: domain and
// service code stays transport agnostic.
//
// Shape of the contract agreed with the web client:
//
//	success: the resource itself, unwrapped   -> {"token":"…","user":{…}}
//	failure: a flat error object              -> {"error":"…","message":"…","fields":[…]}
package httpx

import (
	"errors"
	"log/slog"

	"github.com/gofiber/fiber/v2"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/pagination"
)

// ErrorBody is the client facing error representation. It never contains
// database or internal messages: Error is a stable machine readable code and
// Message is safe to display.
type ErrorBody struct {
	Error   string                `json:"error"`
	Message string                `json:"message"`
	Fields  []apperror.FieldError `json:"fields,omitempty"`
}

// PageBody wraps a collection with its pagination metadata.
type PageBody struct {
	Items any             `json:"items"`
	Meta  pagination.Meta `json:"meta"`
}

// JSON writes payload with the given status.
func JSON(c *fiber.Ctx, status int, payload any) error {
	return c.Status(status).JSON(payload)
}

// OK writes a 200 response.
func OK(c *fiber.Ctx, payload any) error { return JSON(c, fiber.StatusOK, payload) }

// Created writes a 201 response.
func Created(c *fiber.Ctx, payload any) error { return JSON(c, fiber.StatusCreated, payload) }

// NoContent writes a 204 response.
func NoContent(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) }

// Page writes a 200 response including pagination metadata.
func Page(c *fiber.Ctx, items any, meta pagination.Meta) error {
	return JSON(c, fiber.StatusOK, PageBody{Items: items, Meta: meta})
}

// ParseBody decodes the JSON request body into dst, turning malformed input
// into a validation error instead of a 500.
func ParseBody(c *fiber.Ctx, dst any) error {
	if err := c.BodyParser(dst); err != nil {
		return apperror.Validation("invalid_body", "the request body is not valid JSON").WithCause(err)
	}
	return nil
}

// PaginationParams reads the page/limit query parameters.
func PaginationParams(c *fiber.Ctx) pagination.Params {
	return pagination.Parse(c.Query("page"), c.Query("limit"))
}

// Fail renders err and logs it. Server side failures are logged with their
// cause and answered with a generic message; client errors are logged at
// debug level to keep a small box quiet.
func Fail(c *fiber.Ctx, logger *slog.Logger, err error) error {
	appErr := toAppError(err)
	status := appErr.Kind.HTTPStatus()

	if logger != nil {
		attrs := []any{
			slog.String("code", appErr.Code),
			slog.Int("status", status),
			slog.String("method", c.Method()),
			slog.String("path", c.Path()),
		}
		if status >= fiber.StatusInternalServerError {
			logger.ErrorContext(c.UserContext(), "request failed", append(attrs, slog.Any("error", err))...)
		} else {
			logger.DebugContext(c.UserContext(), "request rejected", attrs...)
		}
	}

	return JSON(c, status, ErrorBody{
		Error:   appErr.Code,
		Message: appErr.Message,
		Fields:  appErr.Fields,
	})
}

// StatusFor reports the HTTP status err will be rendered with. The access log
// uses it because middleware runs before the central error handler, so at that
// point the response still carries its default status.
func StatusFor(err error) int {
	if err == nil {
		return fiber.StatusOK
	}
	return toAppError(err).Kind.HTTPStatus()
}

// ErrorHandler is the Fiber level fallback for errors that escape a handler
// (unmatched routes, body limit, parser panics turned into errors...).
func ErrorHandler(logger *slog.Logger) fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		return Fail(c, logger, err)
	}
}

// toAppError normalises anything Fiber or a module may return.
func toAppError(err error) *apperror.Error {
	if err == nil {
		return apperror.Internal(errors.New("nil error"))
	}

	var appErr *apperror.Error
	if errors.As(err, &appErr) {
		return appErr
	}

	var fiberErr *fiber.Error
	if errors.As(err, &fiberErr) {
		switch fiberErr.Code {
		case fiber.StatusNotFound:
			return apperror.NotFound("route_not_found", "the requested endpoint does not exist").WithCause(err)
		case fiber.StatusMethodNotAllowed:
			return apperror.NotFound("method_not_allowed", "the method is not allowed on this endpoint").WithCause(err)
		case fiber.StatusRequestEntityTooLarge:
			return apperror.Validation("payload_too_large", "the request body is too large").WithCause(err)
		default:
			if fiberErr.Code < fiber.StatusInternalServerError {
				return apperror.Validation("bad_request", fiberErr.Message).WithCause(err)
			}
		}
	}

	return apperror.Internal(err)
}
