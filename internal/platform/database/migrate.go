package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/pressly/goose/v3"
)

// migrationsDir is the directory inside the embedded FS. The migrations
// package embeds its own *.sql files, so they live at the FS root.
const migrationsDir = "."

// Migrate applies every pending goose migration from the embedded FS. It runs
// at API startup so a fresh Raspberry Pi deploy needs no extra step.
func Migrate(ctx context.Context, db *sql.DB, migrations fs.FS, logger *slog.Logger) error {
	if err := prepare(migrations, logger); err != nil {
		return err
	}

	before, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}

	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	after, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}

	logger.InfoContext(ctx, "migrations applied",
		slog.Int64("from_version", before),
		slog.Int64("to_version", after),
	)
	return nil
}

// MigrateDown rolls back the most recent migration. Used by `make migrate-down`.
func MigrateDown(ctx context.Context, db *sql.DB, migrations fs.FS, logger *slog.Logger) error {
	if err := prepare(migrations, logger); err != nil {
		return err
	}
	if err := goose.DownContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("roll back migration: %w", err)
	}
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	logger.InfoContext(ctx, "migration rolled back", slog.Int64("version", version))
	return nil
}

// MigrationVersion reports the currently applied schema version.
func MigrationVersion(ctx context.Context, db *sql.DB) (int64, error) {
	if err := goose.SetDialect("mysql"); err != nil {
		return 0, fmt.Errorf("set goose dialect: %w", err)
	}
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("read migration version: %w", err)
	}
	return version, nil
}

func prepare(migrations fs.FS, logger *slog.Logger) error {
	goose.SetBaseFS(migrations)
	goose.SetLogger(gooseLogger{logger: logger})
	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	return nil
}

// gooseLogger adapts slog to the goose.Logger interface so migration output
// joins the structured log stream instead of going to the default logger.
type gooseLogger struct {
	logger *slog.Logger
}

func (l gooseLogger) Printf(format string, v ...any) {
	l.logger.Info("goose: " + trimNewline(fmt.Sprintf(format, v...)))
}

func (l gooseLogger) Fatalf(format string, v ...any) {
	message := trimNewline(fmt.Sprintf(format, v...))
	l.logger.Error("goose: " + message)
	panic("goose: " + message)
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
