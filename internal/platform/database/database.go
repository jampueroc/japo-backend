// Package database owns the *sql.DB lifecycle: DSN building, pool tuning for
// a small box, connection retries at boot and the health check.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/jorgeampuero/japo-backend/internal/platform/config"
)

// DSN builds the go-sql-driver connection string for the given settings.
// It is exported so tools and tests can reuse the exact same options.
func DSN(cfg config.Database) string {
	c := mysql.NewConfig()
	c.Net = "tcp"
	c.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	c.User = cfg.User
	c.Passwd = cfg.Password
	c.DBName = cfg.Name
	c.ParseTime = true                // scan DATETIME/TIMESTAMP into time.Time
	c.Loc = time.UTC                  // store and read timestamps in UTC
	c.InterpolateParams = true        // fewer round trips, no server side prepares
	c.Timeout = cfg.DialTimeout       // dial timeout
	c.ReadTimeout = cfg.ReadTimeout   // i/o read deadline
	c.WriteTimeout = cfg.WriteTimeout // i/o write deadline
	c.Params = map[string]string{"charset": "utf8mb4"}
	return c.FormatDSN()
}

// Open creates the pool without touching the network. database/sql is lazy,
// so this never fails because MariaDB is still booting.
func Open(cfg config.Database) (*sql.DB, error) {
	db, err := sql.Open("mysql", DSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("open mysql connection: %w", err)
	}
	Tune(db, cfg)
	return db, nil
}

// Tune applies the pool limits. Defaults are deliberately small: a Raspberry
// Pi has little RAM and MariaDB pays ~ a few MB per connection.
func Tune(db *sql.DB, cfg config.Database) {
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
}

// Connect opens the pool and waits until the server answers, retrying with a
// fixed backoff. Useful when the API and MariaDB start at the same time.
func Connect(ctx context.Context, cfg config.Database, logger *slog.Logger) (*sql.DB, error) {
	db, err := Open(cfg)
	if err != nil {
		return nil, err
	}

	attempts := cfg.ConnectRetries
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
		lastErr = db.PingContext(pingCtx)
		cancel()

		if lastErr == nil {
			logger.InfoContext(ctx, "database connected",
				slog.String("host", cfg.Host),
				slog.Int("port", cfg.Port),
				slog.String("database", cfg.Name),
				slog.Int("max_open_conns", cfg.MaxOpenConns),
			)
			return db, nil
		}

		logger.WarnContext(ctx, "database not ready, retrying",
			slog.Int("attempt", attempt),
			slog.Int("max_attempts", attempts),
			slog.Any("error", lastErr),
		)

		select {
		case <-ctx.Done():
			_ = db.Close()
			return nil, fmt.Errorf("connect to database: %w", ctx.Err())
		case <-time.After(cfg.ConnectBackoff):
		}
	}

	_ = db.Close()
	return nil, fmt.Errorf("connect to database after %d attempts: %w", attempts, lastErr)
}

// Health pings the database. It is what GET /health reports on.
func Health(ctx context.Context, db *sql.DB) error {
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

// Close drains the pool, giving in flight queries a chance to finish.
func Close(db *sql.DB, logger *slog.Logger) {
	if db == nil {
		return
	}
	if err := db.Close(); err != nil {
		logger.Error("closing database pool", slog.Any("error", err))
		return
	}
	logger.Info("database pool closed")
}
