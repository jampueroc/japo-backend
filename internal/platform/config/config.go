// Package config loads the whole application configuration from environment
// variables (12 factor). Nothing else in the codebase reads os.Getenv.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment names.
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Config is the fully resolved configuration tree.
type Config struct {
	App       App
	HTTP      HTTP
	Database  Database
	Auth      Auth
	CORS      CORS
	Log       Log
	RateLimit RateLimit
	Mail      Mail
}

// Mail configures the transactional emails. The "log" driver writes them to
// the log instead of sending, which is what development uses: no SMTP server
// needed and the verification code shows up in the terminal.
type Mail struct {
	Driver   string // log | smtp
	From     string
	Host     string
	Port     int
	Username string
	Password string
	TLS      string // starttls | tls | none
	Timeout  time.Duration
	// AppURL is the public base URL of the web client, used to build the
	// password reset link.
	AppURL string
	// ResetPath is the route in the web client that handles a reset link.
	ResetPath string
}

// Mail drivers.
const (
	MailDriverLog  = "log"
	MailDriverSMTP = "smtp"
)

// RateLimit bounds how often the unauthenticated endpoints can be hit. The
// store is in memory, which is the right call for a single instance on a Pi.
type RateLimit struct {
	Enabled bool
	// Login guards the password check, which is deliberately expensive.
	LoginMax    int
	LoginWindow time.Duration
	// Register guards account creation.
	RegisterMax    int
	RegisterWindow time.Duration
	// Email guards everything that makes the server send a message, and is
	// keyed by destination as well as by IP so nobody can be mail bombed.
	EmailMax    int
	EmailWindow time.Duration
}

// App holds process level settings.
type App struct {
	Name string
	Env  string
}

// IsProduction reports whether the process runs with production defaults.
func (a App) IsProduction() bool { return a.Env == EnvProduction }

// HTTP holds the Fiber server settings.
type HTTP struct {
	Host            string
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	BodyLimit       int
	// Concurrency caps the number of concurrent connections fasthttp will
	// accept. The library default (256k) pre-allocates far more than a
	// Raspberry Pi needs.
	Concurrency int
	// TrustedProxies lists the addresses allowed to set X-Forwarded-For.
	// It matters for the rate limiter: behind a reverse proxy every
	// request arrives from the proxy, so without this the per IP limit
	// would be a single global limit for everyone.
	TrustedProxies []string
}

// Address is the listen address for Fiber.
func (h HTTP) Address() string { return fmt.Sprintf("%s:%d", h.Host, h.Port) }

// Database holds the MariaDB connection and pool settings. Defaults are sized
// for a Raspberry Pi class machine.
type Database struct {
	Host            string
	Port            int
	Name            string
	User            string
	Password        string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	DialTimeout     time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ConnectRetries  int
	ConnectBackoff  time.Duration
	AutoMigrate     bool
}

// Auth holds token, password hashing and email verification settings.
type Auth struct {
	JWTSecret  string
	JWTIssuer  string
	JWTTTL     time.Duration
	BcryptCost int
	// RequireVerifiedEmail turns email verification into a gate: with it
	// on, registering does not open a session and the token is only issued
	// once the code is confirmed. With it off, verification is an extra
	// that never blocks anyone.
	RequireVerifiedEmail bool
	// VerificationCodeTTL is how long a six digit code stays usable.
	VerificationCodeTTL time.Duration
	// MaxVerificationAttempts kills a code after too many wrong guesses.
	// A six digit code is only a million possibilities.
	MaxVerificationAttempts int
	// ResetTokenTTL is how long a password reset link stays usable.
	ResetTokenTTL time.Duration
}

// CORS holds the browser access rules.
type CORS struct {
	AllowOrigins     string
	AllowMethods     string
	AllowHeaders     string
	AllowCredentials bool
}

// Log holds the slog settings.
type Log struct {
	Level  string
	Format string // json or text
}

// Minimum length accepted for the signing secret.
const minJWTSecretLength = 32

// Load reads and validates the configuration from the process environment.
func Load() (Config, error) {
	cfg := Config{
		App: App{
			Name: env("APP_NAME", "japo-api"),
			Env:  env("APP_ENV", EnvDevelopment),
		},
		HTTP: HTTP{
			Host:            env("HTTP_HOST", "0.0.0.0"),
			Port:            envInt("HTTP_PORT", 8080),
			ReadTimeout:     envDuration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    envDuration("HTTP_WRITE_TIMEOUT", 10*time.Second),
			IdleTimeout:     envDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: envDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
			BodyLimit:       envInt("HTTP_BODY_LIMIT", 512*1024),
			Concurrency:     envInt("HTTP_CONCURRENCY", 256),
			TrustedProxies:  envList("HTTP_TRUSTED_PROXIES"),
		},
		Database: Database{
			Host:            env("DB_HOST", "localhost"),
			Port:            envInt("DB_PORT", 3306),
			Name:            env("DB_NAME", "japo"),
			User:            env("DB_USER", "japo"),
			Password:        env("DB_PASSWORD", ""),
			MaxOpenConns:    envInt("DB_MAX_OPEN_CONNS", 10),
			MaxIdleConns:    envInt("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: envDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute),
			ConnMaxIdleTime: envDuration("DB_CONN_MAX_IDLE_TIME", 2*time.Minute),
			DialTimeout:     envDuration("DB_DIAL_TIMEOUT", 5*time.Second),
			ReadTimeout:     envDuration("DB_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    envDuration("DB_WRITE_TIMEOUT", 10*time.Second),
			ConnectRetries:  envInt("DB_CONNECT_RETRIES", 10),
			ConnectBackoff:  envDuration("DB_CONNECT_BACKOFF", 2*time.Second),
			AutoMigrate:     envBool("DB_AUTO_MIGRATE", true),
		},
		Auth: Auth{
			JWTSecret:               env("JWT_SECRET", ""),
			JWTIssuer:               env("JWT_ISSUER", "japo-api"),
			JWTTTL:                  envDuration("JWT_TTL", time.Hour),
			BcryptCost:              envInt("BCRYPT_COST", 10),
			RequireVerifiedEmail:    envBool("AUTH_REQUIRE_VERIFIED_EMAIL", false),
			VerificationCodeTTL:     envDuration("AUTH_VERIFICATION_CODE_TTL", 15*time.Minute),
			MaxVerificationAttempts: envInt("AUTH_MAX_VERIFICATION_ATTEMPTS", 5),
			ResetTokenTTL:           envDuration("AUTH_RESET_TOKEN_TTL", time.Hour),
		},
		Mail: Mail{
			Driver:    env("MAIL_DRIVER", MailDriverLog),
			From:      env("MAIL_FROM", "japo-api@localhost"),
			Host:      env("SMTP_HOST", ""),
			Port:      envInt("SMTP_PORT", 587),
			Username:  env("SMTP_USERNAME", ""),
			Password:  env("SMTP_PASSWORD", ""),
			TLS:       env("SMTP_TLS", "starttls"),
			Timeout:   envDuration("MAIL_TIMEOUT", 10*time.Second),
			AppURL:    env("APP_PUBLIC_URL", "http://localhost:4400"),
			ResetPath: env("APP_RESET_PATH", "/reset-password"),
		},
		CORS: CORS{
			AllowOrigins:     env("CORS_ALLOW_ORIGINS", "*"),
			AllowMethods:     env("CORS_ALLOW_METHODS", "GET,POST,PUT,DELETE,OPTIONS"),
			AllowHeaders:     env("CORS_ALLOW_HEADERS", "Origin,Content-Type,Accept,Authorization"),
			AllowCredentials: envBool("CORS_ALLOW_CREDENTIALS", false),
		},
		Log: Log{
			Level:  env("LOG_LEVEL", "info"),
			Format: env("LOG_FORMAT", "json"),
		},
		RateLimit: RateLimit{
			Enabled:        envBool("RATE_LIMIT_ENABLED", true),
			LoginMax:       envInt("RATE_LIMIT_LOGIN_MAX", 10),
			LoginWindow:    envDuration("RATE_LIMIT_LOGIN_WINDOW", 15*time.Minute),
			RegisterMax:    envInt("RATE_LIMIT_REGISTER_MAX", 5),
			RegisterWindow: envDuration("RATE_LIMIT_REGISTER_WINDOW", time.Hour),
			EmailMax:       envInt("RATE_LIMIT_EMAIL_MAX", 5),
			EmailWindow:    envDuration("RATE_LIMIT_EMAIL_WINDOW", time.Hour),
		},
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	var problems []string

	if c.HTTP.Port <= 0 || c.HTTP.Port > 65535 {
		problems = append(problems, "HTTP_PORT must be between 1 and 65535")
	}
	if c.HTTP.Concurrency <= 0 {
		problems = append(problems, "HTTP_CONCURRENCY must be greater than 0")
	}
	if c.HTTP.BodyLimit <= 0 {
		problems = append(problems, "HTTP_BODY_LIMIT must be greater than 0")
	}
	if c.Database.Host == "" {
		problems = append(problems, "DB_HOST is required")
	}
	if c.Database.Name == "" {
		problems = append(problems, "DB_NAME is required")
	}
	if c.Database.User == "" {
		problems = append(problems, "DB_USER is required")
	}
	if c.Database.MaxOpenConns <= 0 {
		problems = append(problems, "DB_MAX_OPEN_CONNS must be greater than 0")
	}
	if c.Database.MaxIdleConns < 0 || c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		problems = append(problems, "DB_MAX_IDLE_CONNS must be between 0 and DB_MAX_OPEN_CONNS")
	}
	if len(c.Auth.JWTSecret) < minJWTSecretLength {
		problems = append(problems, fmt.Sprintf("JWT_SECRET is required and must be at least %d characters", minJWTSecretLength))
	}
	if c.Auth.JWTTTL <= 0 {
		problems = append(problems, "JWT_TTL must be a positive duration")
	}
	if c.Auth.BcryptCost < 4 || c.Auth.BcryptCost > 31 {
		problems = append(problems, "BCRYPT_COST must be between 4 and 31")
	}
	if c.App.IsProduction() && c.Database.Password == "" {
		problems = append(problems, "DB_PASSWORD is required when APP_ENV=production")
	}
	if c.Log.Format != "json" && c.Log.Format != "text" {
		problems = append(problems, `LOG_FORMAT must be "json" or "text"`)
	}
	switch c.Mail.Driver {
	case MailDriverLog:
	case MailDriverSMTP:
		if c.Mail.Host == "" {
			problems = append(problems, "SMTP_HOST is required when MAIL_DRIVER=smtp")
		}
		if c.Mail.Port <= 0 || c.Mail.Port > 65535 {
			problems = append(problems, "SMTP_PORT must be between 1 and 65535")
		}
		if c.Mail.From == "" {
			problems = append(problems, "MAIL_FROM is required when MAIL_DRIVER=smtp")
		}
	default:
		problems = append(problems, `MAIL_DRIVER must be "log" or "smtp"`)
	}
	if c.App.IsProduction() && c.Mail.Driver == MailDriverLog && c.Auth.RequireVerifiedEmail {
		// Nobody could ever finish signing up: the code would only exist
		// in the server log.
		problems = append(problems,
			"MAIL_DRIVER=log cannot be used in production while AUTH_REQUIRE_VERIFIED_EMAIL=true")
	}
	if c.Auth.VerificationCodeTTL <= 0 || c.Auth.ResetTokenTTL <= 0 {
		problems = append(problems, "AUTH_VERIFICATION_CODE_TTL and AUTH_RESET_TOKEN_TTL must be positive")
	}
	if c.Auth.MaxVerificationAttempts <= 0 {
		problems = append(problems, "AUTH_MAX_VERIFICATION_ATTEMPTS must be greater than 0")
	}
	if c.RateLimit.Enabled {
		if c.RateLimit.LoginMax <= 0 || c.RateLimit.RegisterMax <= 0 || c.RateLimit.EmailMax <= 0 {
			problems = append(problems, "the RATE_LIMIT_*_MAX values must be greater than 0")
		}
		if c.RateLimit.LoginWindow <= 0 || c.RateLimit.RegisterWindow <= 0 || c.RateLimit.EmailWindow <= 0 {
			problems = append(problems, "the RATE_LIMIT_*_WINDOW values must be positive durations")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

// envList reads a comma separated variable, trimming blanks.
func envList(key string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return v
}

// ErrMissing is returned by Require for an unset variable.
var ErrMissing = errors.New("required environment variable is not set")

// Require reads a mandatory variable. Useful for one off tools (migrations).
func Require(key string) (string, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return "", fmt.Errorf("%s: %w", key, ErrMissing)
	}
	return v, nil
}
