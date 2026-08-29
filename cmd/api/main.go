// Command api is the composition root of the service: it loads the
// configuration, builds every dependency by hand, wires the modules into the
// HTTP server and owns the graceful shutdown.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	// The timezone database, compiled into the binary. Streak days are cut
	// in the user's own zone, so time.LoadLocation has to work wherever
	// this runs, including a runtime image with no /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/gofiber/fiber/v2"

	"github.com/jorgeampuero/japo-backend/internal/modules/auth"
	"github.com/jorgeampuero/japo-backend/internal/modules/progress"
	platformauth "github.com/jorgeampuero/japo-backend/internal/platform/auth"
	"github.com/jorgeampuero/japo-backend/internal/platform/config"
	"github.com/jorgeampuero/japo-backend/internal/platform/database"
	"github.com/jorgeampuero/japo-backend/internal/platform/logger"
	"github.com/jorgeampuero/japo-backend/internal/platform/mail"
	"github.com/jorgeampuero/japo-backend/internal/platform/middleware"
	"github.com/jorgeampuero/japo-backend/internal/platform/server"
	"github.com/jorgeampuero/japo-backend/internal/shared/validatorx"
	"github.com/jorgeampuero/japo-backend/migrations"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	// migrationTimeout bounds the startup migration so a locked table
	// cannot hang the boot forever.
	migrationTimeout = 2 * time.Minute
	// apiPrefix is the mount point of every application route. /health is
	// deliberately outside it: it is infrastructure, not API surface.
	apiPrefix = "/api"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.Log, cfg.App)
	log.Info("starting api", slog.String("version", version))

	// Cancelled on SIGINT/SIGTERM: this is what triggers the drain.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Connect(ctx, cfg.Database, log)
	if err != nil {
		return err
	}
	// Runs after the HTTP server has drained.
	defer database.Close(db, log)

	if cfg.Database.AutoMigrate {
		migrateCtx, cancel := context.WithTimeout(ctx, migrationTimeout)
		defer cancel()
		if err := database.Migrate(migrateCtx, db, migrations.FS, log); err != nil {
			return err
		}
	}

	validate, err := validatorx.New()
	if err != nil {
		return fmt.Errorf("build validator: %w", err)
	}

	// Platform primitives shared by the modules.
	hasher := platformauth.NewPasswordHasher(cfg.Auth.BcryptCost)
	tokens := platformauth.NewJWTManager(cfg.Auth.JWTSecret, cfg.Auth.JWTIssuer, cfg.Auth.JWTTTL)
	secrets := platformauth.NewSecretGenerator()
	notifier := mail.NewNotifier(
		newMailer(cfg.Mail, log),
		cfg.Mail.AppURL,
		cfg.Mail.ResetPath,
		cfg.Auth.VerificationCodeTTL,
		cfg.Auth.ResetTokenTTL,
	)

	// Declared before the modules because the two adapters below close a
	// wiring cycle: see cmd/api/adapters.go.
	var authService auth.Service

	// progress module: repository -> service -> handler.
	progressService := progress.NewService(progress.ServiceDeps{
		Repo: progress.NewMySQLRepository(db),
		Activity: activityRecorderFunc(func(ctx context.Context, userID int64) error {
			_, err := authService.RecordActivity(ctx, userID)
			return err
		}),
		Logger: log,
	})
	progressHandler := progress.NewHandler(progressService, validate, log)

	// auth module: repository -> service -> handler.
	authRepo := auth.NewMySQLRepository(db)
	authService = auth.NewService(auth.ServiceDeps{
		Repo:          authRepo,
		Verifications: authRepo,
		Resets:        authRepo,
		Hasher:        hasher,
		Tokens:        tokens,
		Notifier:      notifier,
		Secrets:       secrets,
		Policy: auth.Policy{
			RequireVerifiedEmail:    cfg.Auth.RequireVerifiedEmail,
			VerificationCodeTTL:     cfg.Auth.VerificationCodeTTL,
			MaxVerificationAttempts: cfg.Auth.MaxVerificationAttempts,
			ResetTokenTTL:           cfg.Auth.ResetTokenTTL,
		},
		Progress: progressSnapshotFunc(func(ctx context.Context, userID int64) (json.RawMessage, bool, error) {
			document, err := progressService.Get(ctx, userID)
			switch {
			case errors.Is(err, progress.ErrProgressNotFound):
				return nil, false, nil
			case err != nil:
				return nil, false, err
			default:
				return document.Data, true, nil
			}
		}),
		Logger: log,
	})
	authHandler := auth.NewHandler(authService, validate, log)

	srv := server.New(cfg, log)
	srv.RegisterHealth(func(ctx context.Context) error {
		return database.Health(ctx, db)
	})

	// The JWT guard and the throttles are injected here, so the modules know
	// nothing about how either is implemented.
	guard := middleware.JWTAuth(tokens, log)

	api := srv.App().Group(apiPrefix)
	auth.RegisterRoutes(api, authHandler, auth.RouteGuards{
		Authenticated: []fiber.Handler{guard},
		Login:         rateLimit(cfg.RateLimit, cfg.RateLimit.LoginMax, cfg.RateLimit.LoginWindow, false, log),
		Register:      rateLimit(cfg.RateLimit, cfg.RateLimit.RegisterMax, cfg.RateLimit.RegisterWindow, false, log),
		// Keyed by destination as well as by IP: limiting only by IP would
		// still let one attacker flood a single victim's inbox.
		Email:  rateLimit(cfg.RateLimit, cfg.RateLimit.EmailMax, cfg.RateLimit.EmailWindow, true, log),
		Verify: rateLimit(cfg.RateLimit, cfg.RateLimit.LoginMax, cfg.RateLimit.LoginWindow, false, log),
	})
	progress.RegisterRoutes(api, progressHandler, guard)

	log.Info("api ready",
		slog.String("address", cfg.HTTP.Address()),
		slog.String("env", cfg.App.Env),
		slog.String("prefix", apiPrefix),
		slog.Duration("token_ttl", cfg.Auth.JWTTTL),
		slog.String("mail_driver", cfg.Mail.Driver),
		slog.Bool("require_verified_email", cfg.Auth.RequireVerifiedEmail),
		slog.Bool("rate_limit", cfg.RateLimit.Enabled),
	)

	return srv.Run(ctx)
}
