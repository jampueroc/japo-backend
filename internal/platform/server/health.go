package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/jorgeampuero/japo-backend/internal/shared/httpx"
)

// HealthPath is the public liveness endpoint.
const HealthPath = "/health"

// healthCheckTimeout bounds the database ping so a stuck dependency cannot
// pile up requests.
const healthCheckTimeout = 2 * time.Second

// healthResponse is the payload of GET /health.
type healthResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
	Uptime   string `json:"uptime"`
}

// RegisterHealth publishes GET /health. It always reports the process as
// alive and degrades to 503 when the database ping fails.
func (s *Server) RegisterHealth(check HealthChecker) {
	startedAt := time.Now()

	s.app.Get(HealthPath, func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), healthCheckTimeout)
		defer cancel()

		body := healthResponse{
			Status:   "ok",
			Database: "up",
			Uptime:   time.Since(startedAt).Truncate(time.Second).String(),
		}

		if err := check(ctx); err != nil {
			s.logger.WarnContext(ctx, "health check failed", slog.Any("error", err))
			body.Status = "degraded"
			body.Database = "down"
			return httpx.JSON(c, fiber.StatusServiceUnavailable, body)
		}

		return httpx.OK(c, body)
	})
}
