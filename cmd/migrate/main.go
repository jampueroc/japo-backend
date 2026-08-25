// Command migrate applies or rolls back the embedded goose migrations
// against the database described by the environment. The API already
// migrates on startup; this binary exists for `make migrate-up` /
// `make migrate-down` and for troubleshooting a deployed box.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/platform/config"
	"github.com/jorgeampuero/japo-backend/internal/platform/database"
	"github.com/jorgeampuero/japo-backend/internal/platform/logger"
	"github.com/jorgeampuero/japo-backend/migrations"
)

const commandTimeout = 2 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: migrate <up|down|version>")
	}
	command := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger.New(cfg.Log, cfg.App)

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	db, err := database.Connect(ctx, cfg.Database, log)
	if err != nil {
		return err
	}
	defer database.Close(db, log)

	switch command {
	case "up":
		return database.Migrate(ctx, db, migrations.FS, log)
	case "down":
		return database.MigrateDown(ctx, db, migrations.FS, log)
	case "version":
		version, err := database.MigrationVersion(ctx, db)
		if err != nil {
			return err
		}
		log.Info("schema version", slog.Int64("version", version))
		return nil
	default:
		return fmt.Errorf("unknown command %q: use up, down or version", command)
	}
}
