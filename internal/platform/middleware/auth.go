package middleware

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v2"

	platformauth "github.com/jorgeampuero/japo-backend/internal/platform/auth"
	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/httpx"
)

const (
	// userIDContextKey carries the authenticated user id in context.Context.
	userIDContextKey contextKey = "user_id"
	// userIDLocalsKey is the Fiber locals key for the same value.
	userIDLocalsKey = "user_id"

	bearerPrefix = "Bearer "
)

// TokenVerifier is the narrow view the middleware needs of the JWT manager.
type TokenVerifier interface {
	Verify(token string) (*platformauth.Claims, error)
}

// JWTAuth rejects requests without a valid access token and exposes the user
// id to the handlers through Fiber locals and the request context.
func JWTAuth(verifier TokenVerifier, logger *slog.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get(fiber.HeaderAuthorization)
		if header == "" {
			return httpx.Fail(c, logger, apperror.Unauthorized("missing_token", "an access token is required"))
		}
		if !strings.HasPrefix(header, bearerPrefix) {
			return httpx.Fail(c, logger, apperror.Unauthorized("invalid_token", "the Authorization header must use the Bearer scheme"))
		}

		raw := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
		if raw == "" {
			return httpx.Fail(c, logger, apperror.Unauthorized("invalid_token", "the access token is empty"))
		}

		claims, err := verifier.Verify(raw)
		if err != nil {
			if errors.Is(err, platformauth.ErrTokenExpired) {
				return httpx.Fail(c, logger, apperror.Unauthorized("token_expired", "the access token has expired").WithCause(err))
			}
			return httpx.Fail(c, logger, apperror.Unauthorized("invalid_token", "the access token is not valid").WithCause(err))
		}

		c.Locals(userIDLocalsKey, claims.UserID)
		c.SetUserContext(context.WithValue(c.UserContext(), userIDContextKey, claims.UserID))
		return c.Next()
	}
}

// UserID returns the authenticated user id. Handlers behind JWTAuth can rely
// on it; anywhere else it reports an unauthorized error.
func UserID(c *fiber.Ctx) (int64, error) {
	id, ok := c.Locals(userIDLocalsKey).(int64)
	if !ok || id <= 0 {
		return 0, apperror.Unauthorized("missing_token", "an access token is required")
	}
	return id, nil
}

// UserIDFromContext returns the authenticated user id from a plain context.
func UserIDFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDContextKey).(int64)
	return id, ok && id > 0
}
