package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/modules/auth"
	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// These tests exercise the use cases against in-memory doubles. They never
// touch a database, so `go test ./...` stays fast and Docker free.

// testEmail is the account every case operates on.
const testEmail = "learner@example.com"

// fixedNow is the frozen clock: 2026-08-21 at 23:30 UTC. Late in the day on
// purpose, so a bug that used local time would show up.
var fixedNow = time.Date(2026, 8, 21, 23, 30, 0, 0, time.UTC)

func frozenClock() auth.Clock { return func() time.Time { return fixedNow } }

// --- test doubles ---------------------------------------------------------

type stubRepository struct {
	createFn      func(ctx context.Context, user auth.User) (auth.User, error)
	findByEmailFn func(ctx context.Context, email valueobject.Email) (auth.User, error)
	findByIDFn    func(ctx context.Context, id valueobject.ID) (auth.User, error)
	touchFn       func(ctx context.Context, id valueobject.ID, day time.Time) (auth.User, error)

	markVerifiedFn   func(id valueobject.ID, at time.Time) (auth.User, error)
	updatePasswordFn func(id valueobject.ID, passwordHash string) error

	// state is the stored account. The real repository re-reads the row
	// after every write, so a double that rebuilds a user from scratch
	// would hide bugs the real one cannot have: keep one record and mutate
	// it, the way the table does.
	state *auth.User

	createCalls      int
	touchCalls       int
	verifiedCalls    int
	lastTouchDay     time.Time
	lastCreated      auth.User
	updatedPasswords []string
	savedProfiles    []auth.Profile
	saveProfileErr   error
}

// stubSecretStore backs both the verification codes and the reset tokens: one
// in-memory row each, which is exactly the shape the real tables have.
type stubSecretStore struct {
	verification    auth.EmailVerification
	hasVerification bool
	reset           auth.PasswordReset
	hasReset        bool

	putVerificationCalls int
	attemptCalls         int
	consumedVerification bool
	consumedReset        bool
}

func (s *stubSecretStore) PutVerification(_ context.Context, verification auth.EmailVerification) error {
	s.putVerificationCalls++
	s.verification = verification
	s.hasVerification = true
	s.consumedVerification = false
	return nil
}

func (s *stubSecretStore) FindVerification(_ context.Context, _ valueobject.ID) (auth.EmailVerification, error) {
	if !s.hasVerification {
		return auth.EmailVerification{}, auth.ErrVerificationNotFound
	}
	return s.verification, nil
}

func (s *stubSecretStore) RecordVerificationAttempt(_ context.Context, _ valueobject.ID) error {
	s.attemptCalls++
	s.verification.Attempts++
	return nil
}

func (s *stubSecretStore) ConsumeVerification(_ context.Context, _ valueobject.ID, at time.Time) error {
	s.consumedVerification = true
	s.verification.ConsumedAt = at
	return nil
}

func (s *stubSecretStore) PutReset(_ context.Context, reset auth.PasswordReset) error {
	s.reset = reset
	s.hasReset = true
	s.consumedReset = false
	return nil
}

func (s *stubSecretStore) FindResetByHash(_ context.Context, tokenHash string) (auth.PasswordReset, error) {
	if !s.hasReset || s.reset.TokenHash != tokenHash {
		return auth.PasswordReset{}, auth.ErrResetNotFound
	}
	return s.reset, nil
}

func (s *stubSecretStore) ConsumeReset(_ context.Context, _ valueobject.ID, at time.Time) error {
	s.consumedReset = true
	s.reset.ConsumedAt = at
	return nil
}

// stubNotifier records what would have been emailed.
type stubNotifier struct {
	err error

	codes  []string
	tokens []string
	to     []string
}

func (s *stubNotifier) SendVerificationCode(_ context.Context, email, code string) error {
	s.to = append(s.to, email)
	s.codes = append(s.codes, code)
	return s.err
}

func (s *stubNotifier) SendPasswordReset(_ context.Context, email, token string) error {
	s.to = append(s.to, email)
	s.tokens = append(s.tokens, token)
	return s.err
}

// stubSecrets makes the generated secrets predictable so a test can redeem
// them.
type stubSecrets struct {
	code  string
	token string
	err   error
}

func (s stubSecrets) VerificationCode() (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.code == "" {
		return "123456", nil
	}
	return s.code, nil
}

func (s stubSecrets) ResetToken() (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.token == "" {
		return "reset-token", nil
	}
	return s.token, nil
}

func (s *stubRepository) Create(ctx context.Context, user auth.User) (auth.User, error) {
	s.createCalls++
	s.lastCreated = user
	if s.createFn == nil {
		user.ID = 42
		return user, nil
	}
	return s.createFn(ctx, user)
}

func (s *stubRepository) FindByEmail(ctx context.Context, email valueobject.Email) (auth.User, error) {
	if s.findByEmailFn == nil {
		return auth.User{}, auth.ErrUserNotFound
	}
	return s.findByEmailFn(ctx, email)
}

func (s *stubRepository) FindByID(ctx context.Context, id valueobject.ID) (auth.User, error) {
	if s.findByIDFn == nil {
		return auth.User{}, auth.ErrUserNotFound
	}
	return s.findByIDFn(ctx, id)
}

func (s *stubRepository) MarkEmailVerified(_ context.Context, id valueobject.ID, at time.Time) (auth.User, error) {
	s.verifiedCalls++
	if s.markVerifiedFn != nil {
		return s.markVerifiedFn(id, at)
	}
	if s.state != nil {
		s.state.EmailVerifiedAt = at
		return *s.state, nil
	}

	user := existingUserWithActivity(auth.Activity{
		LastActiveDate:    auth.UTCDay(at),
		DistinctLoginDays: 1,
		StreakDays:        1,
	})
	user.EmailVerifiedAt = at
	return user, nil
}

func (s *stubRepository) SaveProfile(_ context.Context, _ valueobject.ID, profile auth.Profile) (auth.User, error) {
	if s.saveProfileErr != nil {
		return auth.User{}, s.saveProfileErr
	}
	s.savedProfiles = append(s.savedProfiles, profile)
	if s.state != nil {
		s.state.Profile = profile
		return *s.state, nil
	}
	user := existingUserWithActivity(auth.Activity{})
	user.Profile = profile
	return user, nil
}

func (s *stubRepository) UpdatePassword(_ context.Context, id valueobject.ID, passwordHash string) error {
	s.updatedPasswords = append(s.updatedPasswords, passwordHash)
	if s.updatePasswordFn != nil {
		return s.updatePasswordFn(id, passwordHash)
	}
	return nil
}

func (s *stubRepository) TouchActivity(ctx context.Context, id valueobject.ID, day time.Time) (auth.User, error) {
	s.touchCalls++
	s.lastTouchDay = day
	if s.touchFn != nil {
		return s.touchFn(ctx, id, day)
	}
	if s.state != nil {
		s.state.Activity = auth.Activity{LastActiveDate: day, DistinctLoginDays: 2, StreakDays: 2}
		return *s.state, nil
	}
	return existingUserWithActivity(auth.Activity{
		LastActiveDate:    day,
		DistinctLoginDays: 2,
		StreakDays:        2,
	}), nil
}

type stubHasher struct {
	hashFn    func(plain string) (string, error)
	compareFn func(hash, plain string) error
}

func (s stubHasher) Hash(plain string) (string, error) {
	if s.hashFn == nil {
		return "hashed:" + plain, nil
	}
	return s.hashFn(plain)
}

func (s stubHasher) Compare(hash, plain string) error {
	if s.compareFn == nil {
		if hash != "hashed:"+plain {
			return errors.New("mismatch")
		}
		return nil
	}
	return s.compareFn(hash, plain)
}

type stubTokenIssuer struct {
	issueFn func(userID int64) (string, time.Time, error)
}

func (s stubTokenIssuer) Issue(userID int64) (string, time.Time, error) {
	if s.issueFn == nil {
		return "token-for-user", fixedNow.Add(time.Hour), nil
	}
	return s.issueFn(userID)
}

type stubSnapshot struct {
	document json.RawMessage
	found    bool
	err      error
	calls    int
}

func (s *stubSnapshot) Snapshot(context.Context, int64) (json.RawMessage, bool, error) {
	s.calls++
	return s.document, s.found, s.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newService(repo auth.Repository, hasher stubHasher, tokens stubTokenIssuer) auth.Service {
	return auth.NewService(auth.ServiceDeps{
		Repo:          repo,
		Verifications: &stubSecretStore{},
		Resets:        &stubSecretStore{},
		Hasher:        hasher,
		Tokens:        tokens,
		Notifier:      &stubNotifier{},
		Secrets:       stubSecrets{},
		Clock:         frozenClock(),
		Logger:        discardLogger(),
	})
}

func existingUser(t *testing.T, hash string) auth.User {
	t.Helper()
	return existingUserWithActivity(auth.Activity{
		LastActiveDate:    auth.UTCDay(fixedNow),
		DistinctLoginDays: 1,
		StreakDays:        1,
	}, hash)
}

func existingUserWithActivity(activity auth.Activity, hash ...string) auth.User {
	passwordHash := "hashed:whatever"
	if len(hash) > 0 {
		passwordHash = hash[0]
	}
	return auth.User{
		ID:           valueobject.ID(42),
		Email:        valueobject.MustEmail(testEmail),
		PasswordHash: passwordHash,
		Activity:     activity,
	}
}

// --- Register -------------------------------------------------------------

func TestServiceRegister(t *testing.T) {
	t.Parallel()

	const validPassword = "supersecret1"

	tests := []struct {
		name string
		// creds is the input of the use case.
		creds  auth.Credentials
		repo   func(t *testing.T) *stubRepository
		hasher stubHasher
		// Exactly one expectation per case: a domain sentinel, a
		// validation error, an opaque internal failure, or success.
		wantSentinel   error
		wantValidation bool
		wantOpaque     bool
		wantEmail      string
	}{
		{
			name:  "creates the user with a hashed password",
			creds: auth.Credentials{Email: "  Learner@Example.COM ", Password: validPassword},
			repo: func(t *testing.T) *stubRepository {
				return &stubRepository{
					createFn: func(_ context.Context, user auth.User) (auth.User, error) {
						if user.PasswordHash == validPassword {
							t.Fatal("the plain password reached the repository")
						}
						if user.PasswordHash != "hashed:"+validPassword {
							t.Fatalf("unexpected hash %q", user.PasswordHash)
						}
						user.ID = 7
						return user, nil
					},
				}
			},
			wantEmail: testEmail,
		},
		{
			name:           "rejects an invalid email",
			creds:          auth.Credentials{Email: "not-an-email", Password: validPassword},
			repo:           func(*testing.T) *stubRepository { return &stubRepository{} },
			wantValidation: true,
		},
		{
			name:           "rejects a short password",
			creds:          auth.Credentials{Email: testEmail, Password: "abc1"},
			repo:           func(*testing.T) *stubRepository { return &stubRepository{} },
			wantValidation: true,
		},
		{
			name:           "rejects a password longer than bcrypt allows",
			creds:          auth.Credentials{Email: testEmail, Password: string(make([]byte, 100))},
			repo:           func(*testing.T) *stubRepository { return &stubRepository{} },
			wantValidation: true,
		},
		{
			name:  "rejects an email that already exists",
			creds: auth.Credentials{Email: testEmail, Password: validPassword},
			repo: func(t *testing.T) *stubRepository {
				return &stubRepository{
					findByEmailFn: func(context.Context, valueobject.Email) (auth.User, error) {
						return existingUser(t, "hashed:whatever"), nil
					},
				}
			},
			wantSentinel: auth.ErrEmailAlreadyExists,
		},
		{
			name:  "maps a unique violation raised by the repository",
			creds: auth.Credentials{Email: testEmail, Password: validPassword},
			repo: func(*testing.T) *stubRepository {
				return &stubRepository{
					createFn: func(context.Context, auth.User) (auth.User, error) {
						return auth.User{}, auth.ErrEmailAlreadyExists
					},
				}
			},
			wantSentinel: auth.ErrEmailAlreadyExists,
		},
		{
			name:  "wraps an unexpected repository failure",
			creds: auth.Credentials{Email: testEmail, Password: validPassword},
			repo: func(*testing.T) *stubRepository {
				return &stubRepository{
					findByEmailFn: func(context.Context, valueobject.Email) (auth.User, error) {
						return auth.User{}, errors.New("connection refused")
					},
				}
			},
			wantOpaque: true,
		},
		{
			name:       "wraps a hashing failure",
			creds:      auth.Credentials{Email: testEmail, Password: validPassword},
			repo:       func(*testing.T) *stubRepository { return &stubRepository{} },
			hasher:     stubHasher{hashFn: func(string) (string, error) { return "", errors.New("bcrypt exploded") }},
			wantOpaque: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := tc.repo(t)
			service := newService(repo, tc.hasher, stubTokenIssuer{})

			session, err := service.Register(context.Background(), tc.creds)

			switch {
			case tc.wantSentinel != nil:
				if !errors.Is(err, tc.wantSentinel) {
					t.Fatalf("got error %v, want %v", err, tc.wantSentinel)
				}
				return
			case tc.wantValidation:
				if got := apperror.KindOf(err); err == nil || got != apperror.KindValidation {
					t.Fatalf("got %v (kind %v), want a validation error", err, got)
				}
				return
			case tc.wantOpaque:
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				var appErr *apperror.Error
				if errors.As(err, &appErr) {
					t.Fatalf("infrastructure failures must stay opaque, got %v", appErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := session.User.Email.String(); got != tc.wantEmail {
				t.Fatalf("got email %q, want %q", got, tc.wantEmail)
			}
			// Registering opens a session, the client does not chain a login.
			if session.Token == "" {
				t.Fatal("register did not return an access token")
			}
			if repo.createCalls != 1 {
				t.Fatalf("Create called %d times, want 1", repo.createCalls)
			}
		})
	}
}

// Day one of an account already counts as an active day, otherwise the very
// first login would leave the counters at zero.
func TestServiceRegisterSeedsActivity(t *testing.T) {
	t.Parallel()

	repo := &stubRepository{}
	service := newService(repo, stubHasher{}, stubTokenIssuer{})

	if _, err := service.Register(context.Background(), auth.Credentials{
		Email:    testEmail,
		Password: "supersecret1",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	got := repo.lastCreated.Activity
	want := auth.Activity{
		LastActiveDate:    auth.UTCDay(fixedNow),
		DistinctLoginDays: 1,
		StreakDays:        1,
	}
	if got != want {
		t.Fatalf("got activity %+v, want %+v", got, want)
	}
}

// --- Login ----------------------------------------------------------------

func TestServiceLogin(t *testing.T) {
	t.Parallel()

	const password = "supersecret1"

	tests := []struct {
		name   string
		creds  auth.Credentials
		repo   func(t *testing.T) *stubRepository
		hasher stubHasher
		tokens stubTokenIssuer
		// Exactly one expectation per case, as above.
		wantSentinel error
		wantOpaque   bool
		wantToken    string
	}{
		{
			name:  "returns a token for valid credentials",
			creds: auth.Credentials{Email: "LEARNER@example.com", Password: password},
			repo: func(t *testing.T) *stubRepository {
				return &stubRepository{
					findByEmailFn: func(_ context.Context, got valueobject.Email) (auth.User, error) {
						if got.String() != testEmail {
							t.Fatalf("repository received %q, want the normalised %q", got.String(), testEmail)
						}
						return existingUser(t, "hashed:"+password), nil
					},
				}
			},
			tokens: stubTokenIssuer{
				issueFn: func(userID int64) (string, time.Time, error) {
					if userID != 42 {
						return "", time.Time{}, errors.New("unexpected user id")
					}
					return "signed-token", fixedNow.Add(time.Hour), nil
				},
			},
			wantToken: "signed-token",
		},
		{
			name:  "hides an unknown email behind invalid credentials",
			creds: auth.Credentials{Email: testEmail, Password: password},
			repo: func(*testing.T) *stubRepository {
				return &stubRepository{
					findByEmailFn: func(context.Context, valueobject.Email) (auth.User, error) {
						return auth.User{}, auth.ErrUserNotFound
					},
				}
			},
			wantSentinel: auth.ErrInvalidCredentials,
		},
		{
			name:  "rejects a wrong password",
			creds: auth.Credentials{Email: testEmail, Password: "wrong-password1"},
			repo: func(t *testing.T) *stubRepository {
				return &stubRepository{
					findByEmailFn: func(context.Context, valueobject.Email) (auth.User, error) {
						return existingUser(t, "hashed:"+password), nil
					},
				}
			},
			wantSentinel: auth.ErrInvalidCredentials,
		},
		{
			name:         "hides a malformed email behind invalid credentials",
			creds:        auth.Credentials{Email: "nope", Password: password},
			repo:         func(*testing.T) *stubRepository { return &stubRepository{} },
			wantSentinel: auth.ErrInvalidCredentials,
		},
		{
			name:  "wraps an unexpected repository failure",
			creds: auth.Credentials{Email: testEmail, Password: password},
			repo: func(*testing.T) *stubRepository {
				return &stubRepository{
					findByEmailFn: func(context.Context, valueobject.Email) (auth.User, error) {
						return auth.User{}, errors.New("connection refused")
					},
				}
			},
			wantOpaque: true,
		},
		{
			name:  "wraps a token signing failure",
			creds: auth.Credentials{Email: testEmail, Password: password},
			repo: func(t *testing.T) *stubRepository {
				return &stubRepository{
					findByEmailFn: func(context.Context, valueobject.Email) (auth.User, error) {
						return existingUser(t, "hashed:"+password), nil
					},
				}
			},
			tokens: stubTokenIssuer{
				issueFn: func(int64) (string, time.Time, error) {
					return "", time.Time{}, errors.New("no signing key")
				},
			},
			wantOpaque: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service := newService(tc.repo(t), tc.hasher, tc.tokens)

			session, err := service.Login(context.Background(), tc.creds)

			if tc.wantSentinel != nil {
				if !errors.Is(err, tc.wantSentinel) {
					t.Fatalf("got error %v, want %v", err, tc.wantSentinel)
				}
				return
			}
			if tc.wantOpaque {
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				if errors.Is(err, auth.ErrInvalidCredentials) {
					t.Fatal("an infrastructure failure must not look like bad credentials")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if session.Token != tc.wantToken {
				t.Fatalf("got token %q, want %q", session.Token, tc.wantToken)
			}
			if session.ExpiresAt.Before(fixedNow) {
				t.Fatalf("token already expired at %v", session.ExpiresAt)
			}
			if session.User.Email.String() != testEmail {
				t.Fatalf("got user %q, want %q", session.User.Email.String(), testEmail)
			}
		})
	}
}

// A successful login is activity: it must be recorded for the UTC day of the
// injected clock, and the session must carry the refreshed counters.
func TestServiceLoginRecordsActivity(t *testing.T) {
	t.Parallel()

	const password = "supersecret1"

	repo := &stubRepository{
		findByEmailFn: func(context.Context, valueobject.Email) (auth.User, error) {
			return existingUserWithActivity(auth.Activity{
				LastActiveDate:    auth.UTCDay(fixedNow).AddDate(0, 0, -1),
				DistinctLoginDays: 1,
				StreakDays:        1,
			}, "hashed:"+password), nil
		},
	}
	service := newService(repo, stubHasher{}, stubTokenIssuer{})

	session, err := service.Login(context.Background(), auth.Credentials{Email: testEmail, Password: password})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if repo.touchCalls != 1 {
		t.Fatalf("TouchActivity called %d times, want 1", repo.touchCalls)
	}
	if want := auth.UTCDay(fixedNow); !repo.lastTouchDay.Equal(want) {
		t.Fatalf("recorded day %v, want %v", repo.lastTouchDay, want)
	}
	// The session must expose the counters as they are after the touch,
	// not the stale ones read before it.
	if session.User.Activity.DistinctLoginDays != 2 || session.User.Activity.StreakDays != 2 {
		t.Fatalf("got activity %+v, want the refreshed counters", session.User.Activity)
	}
}

// --- Me -------------------------------------------------------------------

func TestServiceMe(t *testing.T) {
	t.Parallel()

	user := existingUserWithActivity(auth.Activity{
		LastActiveDate:    auth.UTCDay(fixedNow),
		DistinctLoginDays: 3,
		StreakDays:        2,
	})
	repoWithUser := func() *stubRepository {
		return &stubRepository{
			findByIDFn: func(context.Context, valueobject.ID) (auth.User, error) { return user, nil },
		}
	}

	t.Run("embeds the progress document", func(t *testing.T) {
		t.Parallel()

		snapshot := &stubSnapshot{document: json.RawMessage(`{"schemaVersion":1}`), found: true}
		service := auth.NewService(auth.ServiceDeps{
			Repo: repoWithUser(), Verifications: &stubSecretStore{}, Resets: &stubSecretStore{},
			Hasher: stubHasher{}, Tokens: stubTokenIssuer{}, Notifier: &stubNotifier{},
			Secrets: stubSecrets{}, Progress: snapshot, Clock: frozenClock(), Logger: discardLogger(),
		})

		me, err := service.Me(context.Background(), 42)
		if err != nil {
			t.Fatalf("me: %v", err)
		}
		if string(me.Progress) != `{"schemaVersion":1}` {
			t.Fatalf("got progress %s, want the stored document", me.Progress)
		}
		if me.User.Activity.DistinctLoginDays != 3 {
			t.Fatalf("got activity %+v, want the stored counters", me.User.Activity)
		}
	})

	t.Run("leaves progress empty when nothing was saved", func(t *testing.T) {
		t.Parallel()

		snapshot := &stubSnapshot{found: false}
		service := auth.NewService(auth.ServiceDeps{
			Repo: repoWithUser(), Verifications: &stubSecretStore{}, Resets: &stubSecretStore{},
			Hasher: stubHasher{}, Tokens: stubTokenIssuer{}, Notifier: &stubNotifier{},
			Secrets: stubSecrets{}, Progress: snapshot, Clock: frozenClock(), Logger: discardLogger(),
		})

		me, err := service.Me(context.Background(), 42)
		if err != nil {
			t.Fatalf("me: %v", err)
		}
		if me.Progress != nil {
			t.Fatalf("got progress %s, want nil", me.Progress)
		}
		if snapshot.calls != 1 {
			t.Fatalf("Snapshot called %d times, want 1", snapshot.calls)
		}
	})

	t.Run("reports a missing account", func(t *testing.T) {
		t.Parallel()

		service := newService(&stubRepository{}, stubHasher{}, stubTokenIssuer{})
		if _, err := service.Me(context.Background(), 42); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("got error %v, want %v", err, auth.ErrUserNotFound)
		}
	})
}
