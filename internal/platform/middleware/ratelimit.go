package middleware

import (
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/httpx"
)

// RateLimitConfig describes one bucket.
type RateLimitConfig struct {
	// Max requests allowed inside Window.
	Max int
	// Window is the sliding window the count applies to.
	Window time.Duration
	// ByEmail also keys the bucket on the email in the request body. The
	// endpoints that send mail need it: limiting only by IP would still
	// let one attacker flood a single victim's inbox from many addresses,
	// and would let a whole household share one budget behind NAT.
	ByEmail bool
}

// RateLimit builds a limiter that answers with the API's own error shape
// instead of Fiber's default plain text. The store is in memory: a single
// instance on a small box needs nothing else, and a restart clearing the
// counters is not a meaningful weakness here.
func RateLimit(cfg RateLimitConfig, logger *slog.Logger) fiber.Handler {
	return limiter.New(limiter.Config{
		Max:                    cfg.Max,
		Expiration:             cfg.Window,
		LimiterMiddleware:      limiter.SlidingWindow{},
		SkipSuccessfulRequests: false,
		KeyGenerator: func(c *fiber.Ctx) string {
			key := c.IP()
			if cfg.ByEmail {
				key += "|" + emailFromBody(c)
			}
			return key
		},
		LimitReached: func(c *fiber.Ctx) error {
			logger.WarnContext(c.UserContext(), "rate limit reached",
				slog.String("request_id", RequestIDFrom(c)),
				slog.String("path", c.Path()),
				slog.String("ip", c.IP()),
			)
			return httpx.Fail(c, nil, apperror.TooManyRequests(
				"too_many_requests", "too many attempts, please wait a moment and try again"))
		},
	})
}

// emailFromBody peeks at the email of the request without consuming the body,
// so the handler can still parse it. A malformed body yields an empty key,
// which simply groups those requests together.
func emailFromBody(c *fiber.Ctx) string {
	var payload struct {
		Email string `json:"email"`
	}
	if err := c.App().Config().JSONDecoder(c.Body(), &payload); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(payload.Email))
}
