// Package auth is the self contained authentication module: entities and
// interfaces (this file), use cases (service.go), the MariaDB repository
// (repository_mysql.go) and the Fiber transport (handler.go, routes.go,
// dto.go). Only the transport files know about Fiber.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// Password policy. The lower bound counts characters; the upper bound counts
// BYTES, because it is bcrypt's own limit. The two differ for anything
// outside ASCII, which in this app is not a corner case.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 72
)

// Domain sentinel errors. The transport layer maps them to HTTP statuses;
// the service layer never returns database errors as they are.
var (
	// ErrUserNotFound means no user matches the given identity.
	ErrUserNotFound = errors.New("user not found")
	// ErrEmailAlreadyExists means the email is already registered.
	ErrEmailAlreadyExists = errors.New("email already registered")
	// ErrInvalidCredentials is returned for both an unknown email and a
	// wrong password, so the API does not leak which accounts exist.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrInvalidVerificationCode covers every way a code can fail: wrong,
	// expired, already used, too many guesses, or for an account that does
	// not exist. They are deliberately indistinguishable.
	ErrInvalidVerificationCode = errors.New("invalid or expired verification code")
	// ErrEmailAlreadyVerified means there is nothing left to verify.
	ErrEmailAlreadyVerified = errors.New("email already verified")
	// ErrInvalidResetToken covers a reset link that is wrong, expired or
	// already used.
	ErrInvalidResetToken = errors.New("invalid or expired password reset token")
	// ErrEmailNotVerified is returned when the gate is on and the account
	// has not confirmed its address yet.
	ErrEmailNotVerified = errors.New("email not verified")
	// ErrVerificationNotFound means the account has no live code.
	ErrVerificationNotFound = errors.New("verification code not found")
	// ErrResetNotFound means there is no live reset token for that hash.
	ErrResetNotFound = errors.New("password reset token not found")
)

// VerificationCodeDigits is the length of the code sent by email. Short
// enough to type from a phone, which is why it needs an attempt cap.
const VerificationCodeDigits = 6

// Activity is the server authoritative usage record of an account. Every
// field is derived from the server clock on UTC calendar days, so a client
// cannot forge a streak or an unlock.
type Activity struct {
	// LastActiveDate is midnight UTC of the last day the user was seen.
	// The zero value means the user has never been active.
	LastActiveDate time.Time
	// DistinctLoginDays counts the calendar days with at least one
	// activity, ever. It never decreases.
	DistinctLoginDays int
	// StreakDays counts consecutive active days; a gap resets it to 1.
	StreakDays int
}

// Gender is how the user asked to be addressed. The values are English
// tokens, like every other value in this API: what the interface shows is a
// translation the client owns, and storing display text would tie the
// database to whatever language the app happened to speak that year.
type Gender string

// The values a profile accepts.
const (
	GenderMale    Gender = "male"
	GenderFemale  Gender = "female"
	GenderNeutral Gender = "neutral"
)

// Valid reports whether g is one this server stores.
func (g Gender) Valid() bool {
	switch g {
	case GenderMale, GenderFemale, GenderNeutral:
		return true
	default:
		return false
	}
}

// Profile is the identity the user fills in during onboarding. It is empty
// until they do.
type Profile struct {
	Name   string
	Gender Gender
	// BirthDate is the zero value when the user did not give one.
	BirthDate time.Time
}

// Complete reports whether onboarding has been done. The name is what marks
// it: an account with no name has not been through it.
func (p Profile) Complete() bool { return p.Name != "" }

// Profile limits.
const (
	// MaxProfileNameLength matches the users.name column.
	MaxProfileNameLength = 80
	// MinBirthYear rejects a date that is obviously a typo rather than a
	// birthday.
	MinBirthYear = 1900
)

// User is the account entity.
type User struct {
	ID           valueobject.ID
	Email        valueobject.Email
	PasswordHash string
	Profile      Profile
	Activity     Activity
	// EmailVerifiedAt is the zero value until the address is confirmed.
	EmailVerifiedAt time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// EmailVerified reports whether the address has been confirmed.
func (u User) EmailVerified() bool { return !u.EmailVerifiedAt.IsZero() }

// EmailVerification is the live code for one account. Only its hash is ever
// stored, so a dump of the table does not let anyone verify an address.
type EmailVerification struct {
	UserID     valueobject.ID
	CodeHash   string
	Attempts   int
	ExpiresAt  time.Time
	ConsumedAt time.Time
}

// Usable reports whether the code can still be checked at the given time.
func (v EmailVerification) Usable(now time.Time, maxAttempts int) bool {
	return v.ConsumedAt.IsZero() && now.Before(v.ExpiresAt) && v.Attempts < maxAttempts
}

// PasswordReset is the live reset token for one account, stored hashed.
type PasswordReset struct {
	UserID     valueobject.ID
	TokenHash  string
	ExpiresAt  time.Time
	ConsumedAt time.Time
}

// Usable reports whether the token can still be redeemed.
func (r PasswordReset) Usable(now time.Time) bool {
	return r.ConsumedAt.IsZero() && now.Before(r.ExpiresAt)
}

// Credentials is the input of both register and login.
type Credentials struct {
	Email    string
	Password string
}

// Session is the result of a successful register or login: a stateless
// access token and its expiry. There is no refresh token by design.
type Session struct {
	Token     string
	ExpiresAt time.Time
	User      User
}

// Me is the aggregate behind GET /me: everything the client needs to boot in
// a single round trip.
type Me struct {
	User User
	// Progress is the raw document owned by another module. It is nil when
	// the user has not saved anything yet.
	Progress json.RawMessage
}

// ProgressSnapshot reads the caller's progress document. The auth module
// treats it as opaque bytes and never parses it; the implementation is
// injected at the composition root so this module does not import the one
// that owns progress.
type ProgressSnapshot interface {
	Snapshot(ctx context.Context, userID int64) (document json.RawMessage, found bool, err error)
}

// Clock returns the current time. It is injected so the calendar day logic
// is testable without waiting for tomorrow.
type Clock func() time.Time

// Repository is the persistence port. It is implemented by
// repository_mysql.go and by the mocks in the unit tests.
type Repository interface {
	// Create stores a new user, including its initial Activity, and
	// returns it with the generated id and timestamps. It returns
	// ErrEmailAlreadyExists on a unique violation.
	Create(ctx context.Context, user User) (User, error)
	// FindByEmail returns ErrUserNotFound when there is no match.
	FindByEmail(ctx context.Context, email valueobject.Email) (User, error)
	// FindByID returns ErrUserNotFound when there is no match.
	FindByID(ctx context.Context, id valueobject.ID) (User, error)
	// TouchActivity records that the user was active on day (midnight UTC)
	// and returns the updated user. It is idempotent within a day: calling
	// it twice on the same day changes nothing.
	TouchActivity(ctx context.Context, id valueobject.ID, day time.Time) (User, error)
	// MarkEmailVerified stamps the confirmation and returns the user.
	MarkEmailVerified(ctx context.Context, id valueobject.ID, at time.Time) (User, error)
	// UpdatePassword replaces the stored hash.
	UpdatePassword(ctx context.Context, id valueobject.ID, passwordHash string) error
	// SaveProfile stores the onboarding identity and returns the user.
	SaveProfile(ctx context.Context, id valueobject.ID, profile Profile) (User, error)
}

// VerificationRepository stores the live email verification codes.
type VerificationRepository interface {
	// PutVerification stores the code, replacing any previous one for that
	// account: asking for a new code invalidates the old.
	PutVerification(ctx context.Context, verification EmailVerification) error
	// FindVerification returns ErrVerificationNotFound when there is none.
	FindVerification(ctx context.Context, userID valueobject.ID) (EmailVerification, error)
	// RecordVerificationAttempt increments the guess counter.
	RecordVerificationAttempt(ctx context.Context, userID valueobject.ID) error
	// ConsumeVerification marks the code as used.
	ConsumeVerification(ctx context.Context, userID valueobject.ID, at time.Time) error
}

// PasswordResetRepository stores the live password reset tokens.
type PasswordResetRepository interface {
	// PutReset stores the token, replacing any previous one for that
	// account.
	PutReset(ctx context.Context, reset PasswordReset) error
	// FindResetByHash returns ErrResetNotFound when there is no match.
	FindResetByHash(ctx context.Context, tokenHash string) (PasswordReset, error)
	// ConsumeReset marks the token as used.
	ConsumeReset(ctx context.Context, userID valueobject.ID, at time.Time) error
}

// Notifier delivers the transactional emails. It takes plain strings so the
// module stays unaware of how a message is composed or sent; the
// implementation lives in /platform/mail.
type Notifier interface {
	SendVerificationCode(ctx context.Context, email, code string) error
	SendPasswordReset(ctx context.Context, email, token string) error
}

// SecretGenerator produces the codes and tokens that travel by email.
// Injected so the tests are deterministic; the real one is crypto/rand.
type SecretGenerator interface {
	VerificationCode() (string, error)
	ResetToken() (string, error)
}

// PasswordHasher is the hashing port, implemented by /platform/auth.
type PasswordHasher interface {
	Hash(plain string) (string, error)
	Compare(hash, plain string) error
}

// TokenIssuer is the access token port, implemented by /platform/auth.
type TokenIssuer interface {
	Issue(userID int64) (token string, expiresAt time.Time, err error)
}

// Service is the use case port consumed by the handlers.
type Service interface {
	// Register creates the account and opens a session straight away, so
	// the client does not have to chain a login call.
	Register(ctx context.Context, creds Credentials) (Session, error)
	Login(ctx context.Context, creds Credentials) (Session, error)
	// FindByID backs the endpoints that need the caller's identity.
	FindByID(ctx context.Context, userID int64) (User, error)
	// Me returns the identity plus the progress document.
	Me(ctx context.Context, userID int64) (Me, error)
	// SaveProfile stores the onboarding identity. It is idempotent and
	// does not count as activity.
	SaveProfile(ctx context.Context, userID int64, profile Profile) (User, error)
	// RecordActivity marks the user as active today. Other modules reach
	// it through a port of their own, wired at the composition root.
	RecordActivity(ctx context.Context, userID int64) (User, error)
	// VerifyEmail confirms an address with the code sent to it and opens a
	// session, which is what makes the gate work when it is switched on.
	VerifyEmail(ctx context.Context, email, code string) (Session, error)
	// ResendVerification issues a fresh code. It reports nothing about
	// whether the account exists.
	ResendVerification(ctx context.Context, email string) error
	// ForgotPassword emails a reset link. It reports nothing about whether
	// the account exists.
	ForgotPassword(ctx context.Context, email string) error
	// ResetPassword redeems a reset token and sets a new password.
	ResetPassword(ctx context.Context, token, newPassword string) error
}

// UTCDay truncates t to midnight UTC, which is the day boundary used by
// every activity counter.
func UTCDay(t time.Time) time.Time {
	utc := t.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}
