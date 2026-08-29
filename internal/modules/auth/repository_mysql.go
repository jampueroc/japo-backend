package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// mysqlDuplicateEntry is the MariaDB/MySQL error number for a unique key
// violation (ER_DUP_ENTRY).
const mysqlDuplicateEntry = 1062

// dateLayout is how a UTC calendar day is sent to a DATE column. Passing the
// day as a string keeps the comparison unambiguous: no DATE/DATETIME
// coercion, no driver timezone surprises.
const dateLayout = "2006-01-02"

// MySQLRepository is the MariaDB implementation of Repository. It is the only
// file of the module that knows SQL exists.
type MySQLRepository struct {
	db *sql.DB
}

// NewMySQLRepository builds the repository on top of an existing pool.
func NewMySQLRepository(db *sql.DB) *MySQLRepository {
	return &MySQLRepository{db: db}
}

// Compile time checks that the implementation satisfies every port of the
// module. One type backs all three: they are separate interfaces so that
// callers depend only on what they use.
var (
	_ Repository              = (*MySQLRepository)(nil)
	_ VerificationRepository  = (*MySQLRepository)(nil)
	_ PasswordResetRepository = (*MySQLRepository)(nil)
)

const (
	selectUserColumns = `id, email, password_hash, email_verified_at, name, gender, birth_date,
		timezone, last_active_date, distinct_login_days, streak_days, created_at, updated_at`

	insertUserQuery = `
		INSERT INTO users (email, password_hash, last_active_date, distinct_login_days, streak_days)
		VALUES (?, ?, ?, ?, ?)`

	selectUserByIDQuery = `SELECT ` + selectUserColumns + ` FROM users WHERE id = ?`

	selectUserByEmailQuery = `SELECT ` + selectUserColumns + ` FROM users WHERE email = ?`

	// Idempotent within a calendar day. The assignments read last_active_date
	// before it is overwritten: MariaDB evaluates the SET list left to
	// right, so the column must be assigned last.
	//
	// The comparison is >= rather than an equality: a stored day that is
	// AHEAD of today is not a gap. It happens when someone flies west, or
	// edits their timezone to one further behind, and treating it as a gap
	// would reset a streak the user did nothing to lose. Counting it as
	// "already active today" is the conservative reading: no free days
	// either. GREATEST keeps the stored day from walking backwards.
	touchActivityQuery = `
		UPDATE users
		SET distinct_login_days = distinct_login_days
		        + IF(last_active_date IS NOT NULL AND last_active_date >= ?, 0, 1),
		    streak_days = CASE
		        WHEN last_active_date IS NOT NULL AND last_active_date >= ? THEN streak_days
		        WHEN last_active_date <=> DATE_SUB(?, INTERVAL 1 DAY) THEN streak_days + 1
		        ELSE 1
		    END,
		    last_active_date = GREATEST(COALESCE(last_active_date, ?), ?)
		WHERE id = ?`
)

// Create inserts the user and reads it back so the caller gets the database
// generated id and timestamps.
func (r *MySQLRepository) Create(ctx context.Context, user User) (User, error) {
	var lastActive any
	if !user.Activity.LastActiveDate.IsZero() {
		lastActive = user.Activity.LastActiveDate.UTC().Format(dateLayout)
	}

	result, err := r.db.ExecContext(ctx, insertUserQuery,
		user.Email.String(),
		user.PasswordHash,
		lastActive,
		user.Activity.DistinctLoginDays,
		user.Activity.StreakDays,
	)
	if err != nil {
		if isDuplicateEntry(err) {
			return User{}, ErrEmailAlreadyExists
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("read inserted user id: %w", err)
	}

	userID, err := valueobject.NewID(id)
	if err != nil {
		return User{}, fmt.Errorf("inserted user id: %w", err)
	}

	return r.FindByID(ctx, userID)
}

// FindByEmail loads a user by its unique email.
func (r *MySQLRepository) FindByEmail(ctx context.Context, email valueobject.Email) (User, error) {
	return r.queryOne(ctx, selectUserByEmailQuery, email.String())
}

// FindByID loads a user by its primary key.
func (r *MySQLRepository) FindByID(ctx context.Context, id valueobject.ID) (User, error) {
	return r.queryOne(ctx, selectUserByIDQuery, id.Int64())
}

// TouchActivity advances the activity counters for the given day and returns
// the updated user.
func (r *MySQLRepository) TouchActivity(ctx context.Context, id valueobject.ID, day time.Time) (User, error) {
	today := day.UTC().Format(dateLayout)

	result, err := r.db.ExecContext(ctx, touchActivityQuery, today, today, today, today, today, id.Int64())
	if err != nil {
		return User{}, fmt.Errorf("update user activity: %w", err)
	}

	// RowsAffected is 0 both for a missing user and for a same day no-op,
	// so the read below is what tells them apart.
	if _, err := result.RowsAffected(); err != nil {
		return User{}, fmt.Errorf("read updated rows: %w", err)
	}

	return r.FindByID(ctx, id)
}

func (r *MySQLRepository) queryOne(ctx context.Context, query string, args ...any) (User, error) {
	var (
		rawID      int64
		rawEmail   string
		verifiedAt sql.NullTime
		name       sql.NullString
		gender     sql.NullString
		birthDate  sql.NullTime
		timezone   sql.NullString
		lastActive sql.NullTime
		user       User
	)

	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&rawID,
		&rawEmail,
		&user.PasswordHash,
		&verifiedAt,
		&name,
		&gender,
		&birthDate,
		&timezone,
		&lastActive,
		&user.Activity.DistinctLoginDays,
		&user.Activity.StreakDays,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return User{}, ErrUserNotFound
	case err != nil:
		return User{}, fmt.Errorf("select user: %w", err)
	}

	if user.ID, err = valueobject.NewID(rawID); err != nil {
		return User{}, fmt.Errorf("stored user id: %w", err)
	}
	if user.Email, err = valueobject.NewEmail(rawEmail); err != nil {
		return User{}, fmt.Errorf("stored user email: %w", err)
	}
	if verifiedAt.Valid {
		user.EmailVerifiedAt = verifiedAt.Time.UTC()
	}
	user.Profile = Profile{
		Name:     name.String,
		Gender:   Gender(gender.String),
		Timezone: timezone.String,
	}
	if birthDate.Valid {
		user.Profile.BirthDate = UTCDay(birthDate.Time)
	}
	if lastActive.Valid {
		user.Activity.LastActiveDate = UTCDay(lastActive.Time)
	}

	return user, nil
}

func isDuplicateEntry(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry
}
