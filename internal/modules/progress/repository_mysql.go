package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"

	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// MariaDB error numbers we translate into domain errors.
const (
	mysqlNoReferencedRow  = 1216 // ER_NO_REFERENCED_ROW
	mysqlNoReferencedRow2 = 1452 // ER_NO_REFERENCED_ROW_2
)

// MySQLRepository is the MariaDB implementation of Repository.
type MySQLRepository struct {
	db *sql.DB
}

// NewMySQLRepository builds the repository on top of an existing pool.
func NewMySQLRepository(db *sql.DB) *MySQLRepository {
	return &MySQLRepository{db: db}
}

// Compile time check that the implementation satisfies the port.
var _ Repository = (*MySQLRepository)(nil)

const (
	selectProgressByUserQuery = `
		SELECT id, user_id, data, created_at, updated_at
		FROM progress
		WHERE user_id = ?`

	// The unique key on user_id is what turns this INSERT into an upsert.
	upsertProgressQuery = `
		INSERT INTO progress (user_id, data)
		VALUES (?, ?)
		ON DUPLICATE KEY UPDATE data = VALUES(data), updated_at = CURRENT_TIMESTAMP`

	// FOR UPDATE holds the row (or the gap, when there is none yet) for the
	// duration of the transaction, which is what makes a read-modify-write
	// safe against a second answer arriving at the same time.
	lockProgressQuery = `SELECT data FROM progress WHERE user_id = ? FOR UPDATE`
)

// FindByUserID loads the progress row of a user.
func (r *MySQLRepository) FindByUserID(ctx context.Context, userID valueobject.ID) (Progress, error) {
	var (
		rawID     int64
		rawUserID int64
		rawData   []byte
		result    Progress
	)

	err := r.db.QueryRowContext(ctx, selectProgressByUserQuery, userID.Int64()).Scan(
		&rawID,
		&rawUserID,
		&rawData,
		&result.CreatedAt,
		&result.UpdatedAt,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Progress{}, ErrProgressNotFound
	case err != nil:
		return Progress{}, fmt.Errorf("select progress: %w", err)
	}

	if result.ID, err = valueobject.NewID(rawID); err != nil {
		return Progress{}, fmt.Errorf("stored progress id: %w", err)
	}
	if result.UserID, err = valueobject.NewID(rawUserID); err != nil {
		return Progress{}, fmt.Errorf("stored progress user id: %w", err)
	}
	result.Data = json.RawMessage(rawData)

	return result, nil
}

// Upsert writes the document and reads the row back so the caller always
// sees the timestamps the database assigned.
func (r *MySQLRepository) Upsert(ctx context.Context, userID valueobject.ID, data json.RawMessage) (Progress, error) {
	if _, err := r.db.ExecContext(ctx, upsertProgressQuery, userID.Int64(), []byte(data)); err != nil {
		if isMissingReference(err) {
			return Progress{}, ErrUnknownOwner
		}
		return Progress{}, fmt.Errorf("upsert progress: %w", err)
	}
	return r.FindByUserID(ctx, userID)
}

// Mutate applies fn to the stored document inside a locked transaction.
// Unlike Upsert, which is last write wins, concurrent calls here serialise:
// the second one sees what the first one wrote.
func (r *MySQLRepository) Mutate(
	ctx context.Context,
	userID valueobject.ID,
	fn func(current json.RawMessage) (json.RawMessage, error),
) (Progress, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Progress{}, fmt.Errorf("begin progress transaction: %w", err)
	}
	// Rolled back unless the commit below succeeds first.
	defer func() { _ = tx.Rollback() }()

	var stored []byte
	err = tx.QueryRowContext(ctx, lockProgressQuery, userID.Int64()).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No document yet: fn is called with nil and creates the first one.
		stored = nil
	case err != nil:
		return Progress{}, fmt.Errorf("lock progress: %w", err)
	}

	updated, err := fn(json.RawMessage(stored))
	if err != nil {
		// The caller's error travels untouched so the service can map it.
		return Progress{}, err
	}

	if _, err := tx.ExecContext(ctx, upsertProgressQuery, userID.Int64(), []byte(updated)); err != nil {
		if isMissingReference(err) {
			return Progress{}, ErrUnknownOwner
		}
		return Progress{}, fmt.Errorf("write progress: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Progress{}, fmt.Errorf("commit progress transaction: %w", err)
	}

	return r.FindByUserID(ctx, userID)
}

func isMissingReference(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == mysqlNoReferencedRow || mysqlErr.Number == mysqlNoReferencedRow2
}
