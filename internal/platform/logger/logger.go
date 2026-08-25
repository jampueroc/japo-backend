// Package logger builds the single *slog.Logger injected everywhere. There
// are no package level loggers in this codebase.
package logger

import (
	"log/slog"
	"os"
	"strings"

	"github.com/jorgeampuero/japo-backend/internal/platform/config"
)

// New builds a logger from the configuration. "json" is the default format
// because the Raspberry Pi ships its logs to the Docker json-file driver;
// "text" is nicer while developing on macOS.
func New(cfg config.Log, app config.App) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Level)}

	var handler slog.Handler
	if strings.EqualFold(cfg.Format, "text") {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(
		slog.String("service", app.Name),
		slog.String("env", app.Env),
	)
}

func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
