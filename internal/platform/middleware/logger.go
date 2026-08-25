package middleware

import (
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/jorgeampuero/japo-backend/internal/shared/httpx"
)

// RequestLoggerConfig tunes the access log.
type RequestLoggerConfig struct {
	// SkipPaths are not logged. The health endpoint is polled by Docker
	// every few seconds and would otherwise flood the log of a small box.
	SkipPaths []string
}

// RequestLogger emits one structured line per request, after the handler ran.
func RequestLogger(logger *slog.Logger, cfg RequestLoggerConfig) fiber.Handler {
	skip := make(map[string]struct{}, len(cfg.SkipPaths))
	for _, path := range cfg.SkipPaths {
		skip[path] = struct{}{}
	}

	return func(c *fiber.Ctx) error {
		if _, skipped := skip[c.Path()]; skipped {
			return c.Next()
		}

		start := time.Now()
		err := c.Next()
		elapsed := time.Since(start)

		// The central error handler has not written the response yet when a
		// handler returns an error, so derive the status from the error.
		status := c.Response().StatusCode()
		if err != nil {
			status = httpx.StatusFor(err)
		}

		level := slog.LevelInfo
		switch {
		case status >= fiber.StatusInternalServerError:
			level = slog.LevelError
		case status >= fiber.StatusBadRequest:
			level = slog.LevelWarn
		}

		attrs := []slog.Attr{
			slog.String("request_id", RequestIDFrom(c)),
			slog.String("method", c.Method()),
			slog.String("path", c.Path()),
			slog.Int("status", status),
			slog.Duration("duration", elapsed),
			slog.String("ip", c.IP()),
		}
		if err != nil {
			attrs = append(attrs, slog.Any("error", err))
		}

		logger.LogAttrs(c.UserContext(), level, "http request", attrs...)
		return err
	}
}
