//go:build integration

// Package testsupport boots the ephemeral MariaDB used by the integration
// tests. It only compiles under the `integration` build tag, so the fast unit
// suite never pulls testcontainers or needs a Docker daemon.
package testsupport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	// Registers the "mysql" driver used by the container pool below.
	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mariadb"

	"github.com/jorgeampuero/japo-backend/internal/platform/database"
	"github.com/jorgeampuero/japo-backend/migrations"
)

// Container settings. The image matches the one used in docker-compose so
// dev, test and production run the very same engine.
const (
	image        = "mariadb:11.4"
	databaseName = "japo_test"
	databaseUser = "japo_test"
	databasePass = "japo_test"

	startTimeout = 3 * time.Minute
	stopTimeout  = 30 * time.Second
	dockerProbe  = 5 * time.Second
)

// ErrDockerUnavailable means no usable Docker daemon was found.
var ErrDockerUnavailable = errors.New("docker daemon is not available")

// MariaDB is a running container with the goose migrations already applied.
type MariaDB struct {
	DB        *sql.DB
	container *mariadb.MariaDBContainer
}

// StartMariaDB launches the container, opens a pool against it and migrates
// the schema. The caller owns the returned instance and must Stop it.
func StartMariaDB(ctx context.Context) (*MariaDB, error) {
	if err := checkDocker(ctx); err != nil {
		return nil, err
	}

	container, err := mariadb.Run(ctx, image,
		mariadb.WithDatabase(databaseName),
		mariadb.WithUsername(databaseUser),
		mariadb.WithPassword(databasePass),
	)
	if err != nil {
		return nil, fmt.Errorf("start mariadb container: %w", err)
	}

	dsn, err := container.ConnectionString(ctx, "parseTime=true", "loc=UTC", "charset=utf8mb4")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("build container dsn: %w", err)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("open container pool: %w", err)
	}
	// Keep the pool tiny: the tests are sequential and the container is small.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(time.Minute)

	if err := waitReady(ctx, db); err != nil {
		_ = db.Close()
		_ = container.Terminate(ctx)
		return nil, err
	}

	// Same embedded migrations the API runs at startup: dev/test/prod parity.
	if err := database.Migrate(ctx, db, migrations.FS, silentLogger()); err != nil {
		_ = db.Close()
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("migrate container schema: %w", err)
	}

	return &MariaDB{DB: db, container: container}, nil
}

// Stop closes the pool and removes the container.
func (m *MariaDB) Stop(ctx context.Context) error {
	var errs []error
	if m.DB != nil {
		if err := m.DB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close pool: %w", err))
		}
	}
	if m.container != nil {
		if err := m.container.Terminate(ctx); err != nil {
			errs = append(errs, fmt.Errorf("terminate container: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Truncate empties the given tables, foreign keys included. Call it from a
// test that needs a clean slate.
func Truncate(t *testing.T, db *sql.DB, tables ...string) {
	t.Helper()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("disable foreign key checks: %v", err)
	}
	for _, table := range tables {
		if _, err := db.ExecContext(ctx, "TRUNCATE TABLE "+table); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	if _, err := db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatalf("enable foreign key checks: %v", err)
	}
}

// RunWithMariaDB is the TestMain helper of every integration package: it
// boots one container for the whole package, hands the pool to the suite and
// always tears it down. When Docker is missing it prints a clear message and
// reports success instead of hanging or failing the build.
func RunWithMariaDB(m *testing.M, assign func(db *sql.DB)) int {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()

	instance, err := StartMariaDB(ctx)
	if err != nil {
		if errors.Is(err, ErrDockerUnavailable) {
			fmt.Fprintf(os.Stderr,
				"SKIP: integration tests need a running Docker daemon (testcontainers). Start Docker and retry: %v\n", err)
			return 0
		}
		fmt.Fprintf(os.Stderr, "integration setup failed: %v\n", err)
		return 1
	}

	assign(instance.DB)
	code := m.Run()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), stopTimeout)
	defer stopCancel()
	if err := instance.Stop(stopCtx); err != nil {
		fmt.Fprintf(os.Stderr, "integration teardown failed: %v\n", err)
	}

	return code
}

// checkDocker probes the daemon with a short timeout so a missing Docker is
// reported immediately instead of blocking on an image pull.
func checkDocker(ctx context.Context) error {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDockerUnavailable, err)
	}
	defer func() { _ = provider.Close() }()

	probeCtx, cancel := context.WithTimeout(ctx, dockerProbe)
	defer cancel()

	if err := provider.Health(probeCtx); err != nil {
		return fmt.Errorf("%w: %w", ErrDockerUnavailable, err)
	}
	return nil
}

func waitReady(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		lastErr = db.PingContext(pingCtx)
		cancel()
		if lastErr == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("mariadb container never became ready: %w", lastErr)
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
