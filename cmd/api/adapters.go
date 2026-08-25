package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/jorgeampuero/japo-backend/internal/platform/config"
	"github.com/jorgeampuero/japo-backend/internal/platform/mail"
	"github.com/jorgeampuero/japo-backend/internal/platform/middleware"
)

// The auth and progress modules never import each other. They meet here, in
// the composition root, through the ports each of them declares:
//
//	progress.ActivityRecorder -> implemented by the auth service
//	auth.ProgressSnapshot     -> implemented by the progress service
//
// Those two ports close a cycle in the wiring (never in the package graph),
// so one side resolves its dependency lazily through a closure. Both are
// fully wired before the server accepts a single request.

// activityRecorderFunc adapts a plain function to progress.ActivityRecorder.
type activityRecorderFunc func(ctx context.Context, userID int64) error

func (f activityRecorderFunc) RecordActivity(ctx context.Context, userID int64) error {
	return f(ctx, userID)
}

// progressSnapshotFunc adapts a plain function to auth.ProgressSnapshot.
type progressSnapshotFunc func(ctx context.Context, userID int64) (json.RawMessage, bool, error)

func (f progressSnapshotFunc) Snapshot(ctx context.Context, userID int64) (json.RawMessage, bool, error) {
	return f(ctx, userID)
}

// rateLimit builds the throttle for one endpoint, or nothing at all when rate
// limiting is switched off. Returning a slice lets the modules take zero or
// more middlewares without knowing which.
func rateLimit(cfg config.RateLimit, limit int, window time.Duration, byEmail bool, logger *slog.Logger) []fiber.Handler {
	if !cfg.Enabled {
		return nil
	}
	return []fiber.Handler{middleware.RateLimit(middleware.RateLimitConfig{
		Max:     limit,
		Window:  window,
		ByEmail: byEmail,
	}, logger)}
}

// newMailer picks the transport. The log driver is the development default:
// it prints the message, verification code included, so the signup flow can
// be exercised without an SMTP server anywhere in sight.
func newMailer(cfg config.Mail, logger *slog.Logger) mail.Mailer {
	if cfg.Driver != config.MailDriverSMTP {
		logger.Warn("emails are written to the log, not sent",
			slog.String("driver", cfg.Driver))
		return mail.NewLogMailer(logger)
	}

	return mail.NewSMTPMailer(mail.SMTPConfig{
		Host:     cfg.Host,
		Port:     cfg.Port,
		Username: cfg.Username,
		Password: cfg.Password,
		From:     cfg.From,
		TLS:      cfg.TLS,
		Timeout:  cfg.Timeout,
	})
}
