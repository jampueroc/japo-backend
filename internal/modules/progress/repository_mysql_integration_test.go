//go:build integration

package progress_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/jorgeampuero/japo-backend/internal/modules/progress"
	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
	"github.com/jorgeampuero/japo-backend/internal/testsupport"
)

// testDB points at the ephemeral MariaDB started for this package.
var testDB *sql.DB

func TestMain(m *testing.M) {
	os.Exit(testsupport.RunWithMariaDB(m, func(db *sql.DB) { testDB = db }))
}

// seedUser inserts an owner directly: the progress module must not depend on
// the auth module, not even in its tests.
func seedUser(t *testing.T, email string) valueobject.ID {
	t.Helper()

	result, err := testDB.ExecContext(context.Background(),
		`INSERT INTO users (email, password_hash) VALUES (?, ?)`, email, "$2a$10$fakehashfakehashfakeha")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	rawID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read seeded user id: %v", err)
	}
	id, err := valueobject.NewID(rawID)
	if err != nil {
		t.Fatalf("seeded user id: %v", err)
	}
	return id
}

func TestMySQLRepositoryUpsertRoundTrip(t *testing.T) {
	testsupport.Truncate(t, testDB, "progress", "users")

	ctx := context.Background()
	repo := progress.NewMySQLRepository(testDB)
	userID := seedUser(t, "learner@example.com")

	t.Run("no document yet", func(t *testing.T) {
		_, err := repo.FindByUserID(ctx, userID)
		if !errors.Is(err, progress.ErrProgressNotFound) {
			t.Fatalf("got error %v, want %v", err, progress.ErrProgressNotFound)
		}
	})

	first := json.RawMessage(`{"lesson": 3, "streak": 7}`)
	inserted, err := repo.Upsert(ctx, userID, first)
	if err != nil {
		t.Fatalf("insert progress: %v", err)
	}
	if inserted.UserID != userID {
		t.Fatalf("got owner %d, want %d", inserted.UserID.Int64(), userID.Int64())
	}
	assertSameJSON(t, inserted.Data, first)

	// The second write must update the same row (ON DUPLICATE KEY UPDATE),
	// never insert a second one.
	second := json.RawMessage(`{"lesson": 9, "streak": 1, "notes": "kanji"}`)
	updated, err := repo.Upsert(ctx, userID, second)
	if err != nil {
		t.Fatalf("update progress: %v", err)
	}
	if updated.ID != inserted.ID {
		t.Fatalf("upsert created a new row: got id %d, want %d", updated.ID.Int64(), inserted.ID.Int64())
	}
	assertSameJSON(t, updated.Data, second)

	var rows int
	if err := testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM progress WHERE user_id = ?`, userID.Int64()).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("got %d rows for the user, want exactly 1", rows)
	}

	t.Run("read back", func(t *testing.T) {
		found, err := repo.FindByUserID(ctx, userID)
		if err != nil {
			t.Fatalf("find progress: %v", err)
		}
		assertSameJSON(t, found.Data, second)
	})

	t.Run("foreign key rejects an unknown owner", func(t *testing.T) {
		_, err := repo.Upsert(ctx, valueobject.ID(999999), json.RawMessage(`{}`))
		if !errors.Is(err, progress.ErrUnknownOwner) {
			t.Fatalf("got error %v, want %v", err, progress.ErrUnknownOwner)
		}
	})
}

// assertSameJSON compares documents by value, not byte by byte: the database
// may normalise the whitespace of a JSON column.
func assertSameJSON(t *testing.T, got, want json.RawMessage) {
	t.Helper()

	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("stored document is not valid JSON (%s): %v", got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("expected document is not valid JSON (%s): %v", want, err)
	}

	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("got document %s, want %s", gotJSON, wantJSON)
	}
}

// Mutate exists so the grading endpoints do not lose writes the way the
// last-write-wins PUT would. This fires concurrent read-modify-writes at the
// same row and checks that every single one survives.
func TestMySQLRepositoryMutateSerialisesConcurrentWriters(t *testing.T) {
	testsupport.Truncate(t, testDB, "progress", "users")

	ctx := context.Background()
	repo := progress.NewMySQLRepository(testDB)
	userID := seedUser(t, "concurrent@example.com")

	const writers = 20

	var wg sync.WaitGroup
	errs := make(chan error, writers)

	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			_, err := repo.Mutate(ctx, userID, func(current json.RawMessage) (json.RawMessage, error) {
				counter := 0
				if len(current) > 0 {
					var doc struct {
						Counter int `json:"counter"`
					}
					if err := json.Unmarshal(current, &doc); err != nil {
						return nil, fmt.Errorf("decode document: %w", err)
					}
					counter = doc.Counter
				}
				return json.RawMessage(fmt.Sprintf(`{"schemaVersion":1,"counter":%d}`, counter+1)), nil
			})
			errs <- err
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent mutate: %v", err)
		}
	}

	stored, err := repo.FindByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	var final struct {
		Counter int `json:"counter"`
	}
	if err := json.Unmarshal(stored.Data, &final); err != nil {
		t.Fatalf("decode stored document: %v", err)
	}
	if final.Counter != writers {
		t.Fatalf("counter is %d after %d concurrent writers, want %d: writes were lost",
			final.Counter, writers, writers)
	}
}

// A rejected mutation must leave the stored document exactly as it was.
func TestMySQLRepositoryMutateRollsBackOnError(t *testing.T) {
	testsupport.Truncate(t, testDB, "progress", "users")

	ctx := context.Background()
	repo := progress.NewMySQLRepository(testDB)
	userID := seedUser(t, "rollback@example.com")

	original := json.RawMessage(`{"schemaVersion":1,"lesson":1}`)
	if _, err := repo.Upsert(ctx, userID, original); err != nil {
		t.Fatalf("seed document: %v", err)
	}

	wanted := errors.New("the service said no")
	if _, err := repo.Mutate(ctx, userID, func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"schemaVersion":1,"lesson":99}`), wanted
	}); !errors.Is(err, wanted) {
		t.Fatalf("got error %v, want %v", err, wanted)
	}

	stored, err := repo.FindByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	assertSameJSON(t, stored.Data, original)
}
