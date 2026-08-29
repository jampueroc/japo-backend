package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// Policy groups the tunables that come from configuration, so the service
// never reads the environment itself.
type Policy struct {
	// RequireVerifiedEmail turns verification into a gate: registering
	// stops opening a session, and the token is only issued once the code
	// is confirmed.
	RequireVerifiedEmail bool
	// VerificationCodeTTL is how long a code stays usable.
	VerificationCodeTTL time.Duration
	// MaxVerificationAttempts kills a code after too many wrong guesses.
	MaxVerificationAttempts int
	// ResetTokenTTL is how long a reset link stays usable.
	ResetTokenTTL time.Duration
}

// withDefaults fills in the values a zero Policy would leave unusable, which
// is what keeps the unit tests from having to spell all of them out.
func (p Policy) withDefaults() Policy {
	if p.VerificationCodeTTL <= 0 {
		p.VerificationCodeTTL = 15 * time.Minute
	}
	if p.MaxVerificationAttempts <= 0 {
		p.MaxVerificationAttempts = 5
	}
	if p.ResetTokenTTL <= 0 {
		p.ResetTokenTTL = time.Hour
	}
	return p
}

// ServiceDeps groups the ports the auth service needs. A struct keeps the
// constructor readable and forces the composition root to name what it wires.
type ServiceDeps struct {
	Repo          Repository
	Verifications VerificationRepository
	Resets        PasswordResetRepository
	Hasher        PasswordHasher
	Tokens        TokenIssuer
	Notifier      Notifier
	Secrets       SecretGenerator
	// Progress is optional: without it GET /me answers with a null
	// document instead of failing.
	Progress ProgressSnapshot
	Policy   Policy
	// Clock is optional and defaults to time.Now.
	Clock  Clock
	Logger *slog.Logger
}

// service implements Service. It depends only on the ports declared in
// domain.go, which is what makes the unit tests trivial to write.
type service struct {
	repo          Repository
	verifications VerificationRepository
	resets        PasswordResetRepository
	hasher        PasswordHasher
	tokens        TokenIssuer
	notifier      Notifier
	secrets       SecretGenerator
	progress      ProgressSnapshot
	policy        Policy
	clock         Clock
	logger        *slog.Logger
}

// NewService wires the use cases. Dependencies are injected by hand from the
// composition root; there is no DI container in this project.
func NewService(deps ServiceDeps) Service {
	clock := deps.Clock
	if clock == nil {
		clock = time.Now
	}
	return &service{
		repo:          deps.Repo,
		verifications: deps.Verifications,
		resets:        deps.Resets,
		hasher:        deps.Hasher,
		tokens:        deps.Tokens,
		notifier:      deps.Notifier,
		secrets:       deps.Secrets,
		progress:      deps.Progress,
		policy:        deps.Policy.withDefaults(),
		clock:         clock,
		logger:        deps.Logger,
	}
}

// Register creates an account with a bcrypt hashed password and opens a
// session for it. Day one already counts as an active day.
func (s *service) Register(ctx context.Context, creds Credentials) (Session, error) {
	email, err := valueobject.NewEmail(creds.Email)
	if err != nil {
		return Session{}, err
	}
	if err := checkPasswordLength(creds.Password); err != nil {
		return Session{}, err
	}

	// Cheap pre-check for a friendly error. The unique index is still the
	// source of truth: Create maps a duplicate key to the same error.
	if _, err := s.repo.FindByEmail(ctx, email); err == nil {
		return Session{}, ErrEmailAlreadyExists
	} else if !errors.Is(err, ErrUserNotFound) {
		return Session{}, fmt.Errorf("register: look up email: %w", err)
	}

	hash, err := s.hasher.Hash(creds.Password)
	if err != nil {
		return Session{}, fmt.Errorf("register: hash password: %w", err)
	}

	today := UTCDay(s.clock())
	created, err := s.repo.Create(ctx, User{
		Email:        email,
		PasswordHash: hash,
		Activity: Activity{
			LastActiveDate:    today,
			DistinctLoginDays: 1,
			StreakDays:        1,
		},
	})
	if err != nil {
		if errors.Is(err, ErrEmailAlreadyExists) {
			return Session{}, ErrEmailAlreadyExists
		}
		return Session{}, fmt.Errorf("register: create user: %w", err)
	}

	s.logger.InfoContext(ctx, "user registered", slog.Int64("user_id", created.ID.Int64()))

	if err := s.issueVerificationCode(ctx, created); err != nil {
		// The account exists; a failure here only means no code went out,
		// and the client can ask for another one.
		s.logger.ErrorContext(ctx, "could not issue the first verification code",
			slog.Int64("user_id", created.ID.Int64()), slog.Any("error", err))
	}

	if s.policy.RequireVerifiedEmail {
		// The gate: no token until the address is confirmed. The caller
		// gets the account back so it can show the code screen.
		return Session{User: created}, nil
	}

	return s.openSession(created)
}

// Login verifies the credentials, records the activity of the day and issues
// a short lived access token.
func (s *service) Login(ctx context.Context, creds Credentials) (Session, error) {
	email, err := valueobject.NewEmail(creds.Email)
	if err != nil {
		// Do not tell the caller whether the address merely looks wrong.
		return Session{}, ErrInvalidCredentials
	}

	user, err := s.repo.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return Session{}, ErrInvalidCredentials
		}
		return Session{}, fmt.Errorf("login: look up email: %w", err)
	}

	if err := s.hasher.Compare(user.PasswordHash, creds.Password); err != nil {
		s.logger.DebugContext(ctx, "password mismatch", slog.Int64("user_id", user.ID.Int64()))
		return Session{}, ErrInvalidCredentials
	}

	if s.policy.RequireVerifiedEmail && !user.EmailVerified() {
		// Correct credentials, but the address is still unconfirmed. This
		// is a distinct answer from bad credentials on purpose: the caller
		// already proved it owns the account, so telling it to go and
		// verify leaks nothing it did not know.
		return Session{}, ErrEmailNotVerified
	}

	// A successful login is activity: it may extend the streak and unlock
	// content that depends on distinct login days. The day is cut in the
	// user's own zone, so their midnight is the one that counts.
	updated, err := s.repo.TouchActivity(ctx, user.ID, DayIn(s.clock(), user.Profile.Location()))
	if err != nil {
		return Session{}, fmt.Errorf("login: record activity: %w", err)
	}

	s.logger.InfoContext(ctx, "user logged in",
		slog.Int64("user_id", updated.ID.Int64()),
		slog.Int("distinct_login_days", updated.Activity.DistinctLoginDays),
		slog.Int("streak_days", updated.Activity.StreakDays),
	)

	return s.openSession(updated)
}

// FindByID loads an account by its identifier.
func (s *service) FindByID(ctx context.Context, userID int64) (User, error) {
	id, err := valueobject.NewID(userID)
	if err != nil {
		return User{}, err
	}

	user, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("find user: %w", err)
	}
	return user, nil
}

// Me returns the caller's identity together with the progress document, so
// the client boots with one request instead of two.
func (s *service) Me(ctx context.Context, userID int64) (Me, error) {
	user, err := s.FindByID(ctx, userID)
	if err != nil {
		return Me{}, err
	}

	result := Me{User: user}
	if s.progress == nil {
		return result, nil
	}

	document, found, err := s.progress.Snapshot(ctx, userID)
	if err != nil {
		return Me{}, fmt.Errorf("me: read progress: %w", err)
	}
	if found {
		result.Progress = document
	}
	return result, nil
}

// RecordActivity marks the user as active today. It is idempotent within a
// calendar day, so callers may invoke it on every write.
//
// The account is read first because the calendar day depends on the zone
// stored on it: without that read the streak would be cut in UTC for someone
// who told us where they are.
func (s *service) RecordActivity(ctx context.Context, userID int64) (User, error) {
	id, err := valueobject.NewID(userID)
	if err != nil {
		return User{}, err
	}

	current, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("record activity: look up account: %w", err)
	}

	user, err := s.repo.TouchActivity(ctx, id, DayIn(s.clock(), current.Profile.Location()))
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("record activity: %w", err)
	}
	return user, nil
}

func (s *service) openSession(user User) (Session, error) {
	token, expiresAt, err := s.tokens.Issue(user.ID.Int64())
	if err != nil {
		return Session{}, fmt.Errorf("issue token: %w", err)
	}
	return Session{Token: token, ExpiresAt: expiresAt, User: user}, nil
}

// checkPasswordLength guards the service independently of the DTO rules, so
// the use case is safe no matter who calls it.
func checkPasswordLength(password string) error {
	switch {
	case len(password) < MinPasswordLength:
		return apperror.Validation("weak_password",
			fmt.Sprintf("the password must be at least %d characters long", MinPasswordLength))
	case len(password) > MaxPasswordLength:
		// Bytes, not characters: this is bcrypt's own limit.
		return apperror.Validation("weak_password",
			fmt.Sprintf("the password must be at most %d bytes long", MaxPasswordLength))
	default:
		return nil
	}
}
