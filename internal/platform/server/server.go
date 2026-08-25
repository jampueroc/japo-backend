// Package server builds the Fiber application, wires the global middleware
// and owns the graceful shutdown sequence.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gofiber/fiber/v2"

	"github.com/jorgeampuero/japo-backend/internal/platform/config"
	"github.com/jorgeampuero/japo-backend/internal/platform/middleware"
	"github.com/jorgeampuero/japo-backend/internal/shared/httpx"
)

// HealthChecker reports whether a dependency is usable.
type HealthChecker func(ctx context.Context) error

// Server owns the Fiber app and its lifecycle.
type Server struct {
	app    *fiber.App
	cfg    config.HTTP
	logger *slog.Logger
}

// New builds the Fiber app with the project defaults and the global
// middleware chain: request id, recover, access log and CORS.
func New(cfg config.Config, logger *slog.Logger) *Server {
	app := fiber.New(fiber.Config{
		AppName:               cfg.App.Name,
		ReadTimeout:           cfg.HTTP.ReadTimeout,
		WriteTimeout:          cfg.HTTP.WriteTimeout,
		IdleTimeout:           cfg.HTTP.IdleTimeout,
		BodyLimit:             cfg.HTTP.BodyLimit,
		Concurrency:           cfg.HTTP.Concurrency,
		DisableStartupMessage: true,
		// Trade a little CPU for a smaller resident set: the target box is
		// a Raspberry Pi, not a server.
		ReduceMemoryUsage: true,
		ErrorHandler:      httpx.ErrorHandler(logger),
		// Behind the agreed reverse proxy every request arrives from the
		// proxy itself, so without this the per IP rate limit would be one
		// global limit shared by everyone. Only the listed proxies are
		// allowed to speak for someone else.
		EnableTrustedProxyCheck: len(cfg.HTTP.TrustedProxies) > 0,
		TrustedProxies:          cfg.HTTP.TrustedProxies,
		ProxyHeader:             proxyHeader(cfg.HTTP.TrustedProxies),
	})

	app.Use(middleware.RequestID())
	// The access log wraps Recover on purpose: a recovered panic comes back
	// as an error and still produces a log line with its real status.
	app.Use(middleware.RequestLogger(logger, middleware.RequestLoggerConfig{
		SkipPaths: []string{HealthPath},
	}))
	app.Use(middleware.Recover(logger))
	app.Use(middleware.CORS(cfg.CORS))

	return &Server{app: app, cfg: cfg.HTTP, logger: logger}
}

// proxyHeader only honours X-Forwarded-For when a trusted proxy is
// configured. Trusting it unconditionally would let any caller forge its own
// address and walk straight through the rate limiter.
func proxyHeader(trustedProxies []string) string {
	if len(trustedProxies) == 0 {
		return ""
	}
	return fiber.HeaderXForwardedFor
}

// App exposes the router so the composition root can register the modules.
// It is the only place where module packages meet the platform.
func (s *Server) App() *fiber.App { return s.app }

// Start blocks listening for connections.
func (s *Server) Start() error {
	s.logger.Info("http server listening", slog.String("address", s.cfg.Address()))
	if err := s.app.Listen(s.cfg.Address()); err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Address(), err)
	}
	return nil
}

// Shutdown drains in flight requests, bounded by the configured timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, s.cfg.ShutdownTimeout)
	defer cancel()

	s.logger.Info("http server shutting down", slog.Duration("timeout", s.cfg.ShutdownTimeout))
	if err := s.app.ShutdownWithContext(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}
	s.logger.Info("http server stopped")
	return nil
}

// Run starts the server and shuts it down as soon as ctx is cancelled, which
// is what the SIGINT/SIGTERM handler in main does.
func (s *Server) Run(ctx context.Context) error {
	errs := make(chan error, 1)

	go func() {
		if err := s.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		return s.Shutdown(context.WithoutCancel(ctx))
	}
}
