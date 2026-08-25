// Package middleware wires the cross cutting HTTP concerns: request id,
// structured logging, panic recovery, CORS and JWT authentication. It lives
// in /platform because it is infrastructure and it is allowed to import
// Fiber.
package middleware

import (
	"context"
	"log/slog"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/utils"

	"github.com/jorgeampuero/japo-backend/internal/platform/config"
)

type contextKey string

const (
	// requestIDContextKey carries the request id down to services and
	// repositories through context.Context.
	requestIDContextKey contextKey = "request_id"
	// requestIDLocalsKey is the Fiber locals key for the same value.
	requestIDLocalsKey = "request_id"
)

// RequestID assigns (or reuses) an identifier for every request and puts it
// in the response headers, in Fiber locals and in the user context.
func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Get(fiber.HeaderXRequestID)
		if id == "" {
			id = utils.UUIDv4()
		}
		c.Locals(requestIDLocalsKey, id)
		c.Set(fiber.HeaderXRequestID, id)
		c.SetUserContext(context.WithValue(c.UserContext(), requestIDContextKey, id))
		return c.Next()
	}
}

// RequestIDFrom reads the request id from a Fiber context.
func RequestIDFrom(c *fiber.Ctx) string {
	if id, ok := c.Locals(requestIDLocalsKey).(string); ok {
		return id
	}
	return ""
}

// RequestIDFromContext reads the request id from a plain context, which is
// what services and repositories receive.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDContextKey).(string); ok {
		return id
	}
	return ""
}

// Recover turns a panic into a 500 handled by the central error handler and
// logs the stack trace once.
func Recover(logger *slog.Logger) fiber.Handler {
	return recover.New(recover.Config{
		EnableStackTrace: true,
		StackTraceHandler: func(c *fiber.Ctx, e any) {
			logger.Error("panic recovered",
				slog.String("request_id", RequestIDFrom(c)),
				slog.String("method", c.Method()),
				slog.String("path", c.Path()),
				slog.Any("panic", e),
				slog.String("stack", string(stackTrace())),
			)
		},
	})
}

// CORS applies the configured browser access rules.
func CORS(cfg config.CORS) fiber.Handler {
	return cors.New(cors.Config{
		AllowOrigins:     cfg.AllowOrigins,
		AllowMethods:     cfg.AllowMethods,
		AllowHeaders:     cfg.AllowHeaders,
		AllowCredentials: cfg.AllowCredentials,
	})
}
