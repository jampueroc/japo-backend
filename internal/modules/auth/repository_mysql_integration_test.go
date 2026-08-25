//go:build integration

package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/modules/auth"
	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
	"github.com/jorgeampuero/japo-backend/internal/testsupport"
)

// testDB points at the ephemeral MariaDB started for this package.
var testDB *sql.DB

func TestMain(m *testing.M) {
	os.Exit(testsupport.RunWithMariaDB(m, func(db *sql.DB) { testDB = db }))
}

func TestMySQLRepositoryCreateAndFind(t *testing.T) {
	testsupport.Truncate(t, testDB, "progress", "users")

	ctx := context.Background()
	repo := auth.NewMySQLRepository(testDB)
	email := valueobject.MustEmail("learner@example.com")

	created, err := repo.Create(ctx, auth.User{
		Email:        email,
		PasswordHash: "$2a$10$fakehashfakehashfakeha",
		Activity: auth.Activity{
			LastActiveDate:    auth.UTCDay(time.Now()),
			DistinctLoginDays: 1,
			StreakDays:        1,
		},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if created.ID.IsZero() {
		t.Fatal("the database did not assign an id")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("the database did not assign timestamps")
	}
	// Day one of an account already counts as an active day.
	if created.Activity.DistinctLoginDays != 1 || created.Activity.StreakDays != 1 {
		t.Fatalf("got activity %+v, want the seeded counters", created.Activity)
	}
	if !created.Activity.LastActiveDate.Equal(auth.UTCDay(time.Now())) {
		t.Fatalf("got last active date %v, want today (UTC)", created.Activity.LastActiveDate)
	}

	t.Run("fetch by email", func(t *testing.T) {
		found, err := repo.FindByEmail(ctx, email)
		if err != nil {
			t.Fatalf("find by email: %v", err)
		}
		if found.ID != created.ID {
			t.Fatalf("got id %d, want %d", found.ID.Int64(), created.ID.Int64())
		}
		if found.PasswordHash != created.PasswordHash {
			t.Fatalf("got hash %q, want %q", found.PasswordHash, created.PasswordHash)
		}
	})

	t.Run("fetch by id", func(t *testing.T) {
		found, err := repo.FindByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("find by id: %v", err)
		}
		if found.Email.String() != email.String() {
			t.Fatalf("got email %q, want %q", found.Email.String(), email.String())
		}
	})

	t.Run("unique constraint on email", func(t *testing.T) {
		_, err := repo.Create(ctx, auth.User{Email: email, PasswordHash: "another-hash"})
		if !errors.Is(err, auth.ErrEmailAlreadyExists) {
			t.Fatalf("got error %v, want %v", err, auth.ErrEmailAlreadyExists)
		}
	})

	t.Run("missing email reports the domain error", func(t *testing.T) {
		_, err := repo.FindByEmail(ctx, valueobject.MustEmail("nobody@example.com"))
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("got error %v, want %v", err, auth.ErrUserNotFound)
		}
	})

	t.Run("missing id reports the domain error", func(t *testing.T) {
		_, err := repo.FindByID(ctx, valueobject.ID(999999))
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("got error %v, want %v", err, auth.ErrUserNotFound)
		}
	})
}

// The activity counters are computed by a single UPDATE whose SET list reads
// last_active_date before overwriting it. That relies on MariaDB evaluating
// assignments left to right, which is exactly the kind of thing worth
// pinning down against the real engine instead of arguing about.
func TestMySQLRepositoryTouchActivity(t *testing.T) {
	testsupport.Truncate(t, testDB, "progress", "users")

	ctx := context.Background()
	repo := auth.NewMySQLRepository(testDB)

	// A user who has never been active: NULL last_active_date, zero counters.
	created, err := repo.Create(ctx, auth.User{
		Email:        valueobject.MustEmail("streak@example.com"),
		PasswordHash: "$2a$10$fakehashfakehashfakeha",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if !created.Activity.LastActiveDate.IsZero() {
		t.Fatalf("got last active date %v, want the zero value", created.Activity.LastActiveDate)
	}

	day1 := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		day          time.Time
		wantDistinct int
		wantStreak   int
	}{
		{name: "first day ever", day: day1, wantDistinct: 1, wantStreak: 1},
		{name: "same day is a no-op", day: day1, wantDistinct: 1, wantStreak: 1},
		{name: "next day extends the streak", day: day1.AddDate(0, 0, 1), wantDistinct: 2, wantStreak: 2},
		{name: "and again", day: day1.AddDate(0, 0, 2), wantDistinct: 3, wantStreak: 3},
		{name: "a gap resets the streak but not the total", day: day1.AddDate(0, 0, 6), wantDistinct: 4, wantStreak: 1},
		{name: "month boundary still counts as consecutive", day: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), wantDistinct: 5, wantStreak: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			updated, err := repo.TouchActivity(ctx, created.ID, tc.day)
			if err != nil {
				t.Fatalf("touch activity: %v", err)
			}
			if updated.Activity.DistinctLoginDays != tc.wantDistinct {
				t.Fatalf("got distinct login days %d, want %d", updated.Activity.DistinctLoginDays, tc.wantDistinct)
			}
			if updated.Activity.StreakDays != tc.wantStreak {
				t.Fatalf("got streak days %d, want %d", updated.Activity.StreakDays, tc.wantStreak)
			}
			if !updated.Activity.LastActiveDate.Equal(tc.day) {
				t.Fatalf("got last active date %v, want %v", updated.Activity.LastActiveDate, tc.day)
			}
		})
	}

	t.Run("consecutive day after a gap extends again", func(t *testing.T) {
		updated, err := repo.TouchActivity(ctx, created.ID, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("touch activity: %v", err)
		}
		if updated.Activity.StreakDays != 2 {
			t.Fatalf("got streak days %d, want 2", updated.Activity.StreakDays)
		}
	})

	t.Run("missing user reports the domain error", func(t *testing.T) {
		_, err := repo.TouchActivity(ctx, valueobject.ID(999999), day1)
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("got error %v, want %v", err, auth.ErrUserNotFound)
		}
	})
}
