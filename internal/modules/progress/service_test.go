package progress_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/modules/progress"
	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

type stubRepository struct {
	findFn   func(ctx context.Context, userID valueobject.ID) (progress.Progress, error)
	upsertFn func(ctx context.Context, userID valueobject.ID, data json.RawMessage) (progress.Progress, error)

	// stored is the in-memory document the Mutate double reads and writes,
	// which is what lets the grading tests assert on the result.
	stored    json.RawMessage
	mutateErr error

	upsertCalls int
	mutateCalls int
}

func (s *stubRepository) FindByUserID(ctx context.Context, userID valueobject.ID) (progress.Progress, error) {
	if s.findFn == nil {
		return progress.Progress{}, progress.ErrProgressNotFound
	}
	return s.findFn(ctx, userID)
}

func (s *stubRepository) Upsert(ctx context.Context, userID valueobject.ID, data json.RawMessage) (progress.Progress, error) {
	s.upsertCalls++
	if s.upsertFn == nil {
		return progress.Progress{UserID: userID, Data: data}, nil
	}
	return s.upsertFn(ctx, userID, data)
}

// stubRecorder stands in for the auth module, which owns the user's
// activity counters and is reached through a port wired at the composition
// root.
type stubRecorder struct {
	err   error
	calls int
	users []int64
}

func (s *stubRecorder) RecordActivity(_ context.Context, userID int64) error {
	s.calls++
	s.users = append(s.users, userID)
	return s.err
}

func (s *stubRepository) Mutate(
	_ context.Context,
	userID valueobject.ID,
	fn func(current json.RawMessage) (json.RawMessage, error),
) (progress.Progress, error) {
	s.mutateCalls++
	if s.mutateErr != nil {
		return progress.Progress{}, s.mutateErr
	}

	updated, err := fn(s.stored)
	if err != nil {
		return progress.Progress{}, err
	}
	s.stored = updated
	return progress.Progress{UserID: userID, Data: updated}, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fixedNow is the frozen clock used by the grading tests.
var fixedNow = time.Date(2026, 8, 21, 23, 30, 0, 0, time.UTC)

func newService(repo progress.Repository, recorder progress.ActivityRecorder) progress.Service {
	return progress.NewService(progress.ServiceDeps{
		Repo:     repo,
		Activity: recorder,
		Clock:    func() time.Time { return fixedNow },
		Logger:   discardLogger(),
	})
}

func TestServiceGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		userID       int64
		repo         *stubRepository
		wantSentinel error
		wantKind     apperror.Kind
		wantOpaque   bool
		wantData     string
	}{
		{
			name:   "returns the stored document",
			userID: 42,
			repo: &stubRepository{
				findFn: func(_ context.Context, userID valueobject.ID) (progress.Progress, error) {
					if userID.Int64() != 42 {
						t.Fatalf("repository received user %d, want 42", userID.Int64())
					}
					return progress.Progress{UserID: userID, Data: json.RawMessage(`{"lesson":3}`)}, nil
				},
			},
			wantData: `{"lesson":3}`,
		},
		{
			name:         "reports a missing document",
			userID:       42,
			repo:         &stubRepository{},
			wantSentinel: progress.ErrProgressNotFound,
		},
		{
			name:     "rejects a non positive user id",
			userID:   0,
			repo:     &stubRepository{},
			wantKind: apperror.KindValidation,
		},
		{
			name:   "wraps an unexpected repository failure",
			userID: 42,
			repo: &stubRepository{
				findFn: func(context.Context, valueobject.ID) (progress.Progress, error) {
					return progress.Progress{}, errors.New("connection refused")
				},
			},
			wantOpaque: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service := progress.NewService(progress.ServiceDeps{Repo: tc.repo, Logger: discardLogger()})
			got, err := service.Get(context.Background(), tc.userID)

			switch {
			case tc.wantSentinel != nil:
				if !errors.Is(err, tc.wantSentinel) {
					t.Fatalf("got error %v, want %v", err, tc.wantSentinel)
				}
				return
			case tc.wantKind == apperror.KindValidation:
				if kind := apperror.KindOf(err); err == nil || kind != tc.wantKind {
					t.Fatalf("got %v (kind %v), want a validation error", err, kind)
				}
				return
			case tc.wantOpaque:
				var appErr *apperror.Error
				if err == nil || errors.As(err, &appErr) {
					t.Fatalf("infrastructure failures must stay opaque, got %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got.Data) != tc.wantData {
				t.Fatalf("got data %s, want %s", got.Data, tc.wantData)
			}
		})
	}
}

func TestServiceSave(t *testing.T) {
	t.Parallel()

	tooBig := json.RawMessage(`{"x":"` + strings.Repeat("a", progress.MaxDataSize) + `"}`)

	tests := []struct {
		name         string
		userID       int64
		data         json.RawMessage
		repo         *stubRepository
		wantSentinel error
		wantKind     apperror.Kind
		wantUpserts  int
	}{
		{
			name:        "stores a valid document",
			userID:      42,
			data:        json.RawMessage(`{"lesson":3,"streak":7}`),
			repo:        &stubRepository{},
			wantUpserts: 1,
		},
		{
			name:     "rejects an empty document",
			userID:   42,
			data:     nil,
			repo:     &stubRepository{},
			wantKind: apperror.KindValidation,
		},
		{
			name:     "rejects malformed JSON",
			userID:   42,
			data:     json.RawMessage(`{"lesson":`),
			repo:     &stubRepository{},
			wantKind: apperror.KindValidation,
		},
		{
			name:     "rejects an oversized document",
			userID:   42,
			data:     tooBig,
			repo:     &stubRepository{},
			wantKind: apperror.KindValidation,
		},
		{
			name:     "rejects a non positive user id",
			userID:   -1,
			data:     json.RawMessage(`{}`),
			repo:     &stubRepository{},
			wantKind: apperror.KindValidation,
		},
		{
			name:   "surfaces an unknown owner",
			userID: 42,
			data:   json.RawMessage(`{}`),
			repo: &stubRepository{
				upsertFn: func(context.Context, valueobject.ID, json.RawMessage) (progress.Progress, error) {
					return progress.Progress{}, progress.ErrUnknownOwner
				},
			},
			wantSentinel: progress.ErrUnknownOwner,
			wantUpserts:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service := progress.NewService(progress.ServiceDeps{Repo: tc.repo, Logger: discardLogger()})
			saved, err := service.Save(context.Background(), tc.userID, tc.data)

			if tc.repo.upsertCalls != tc.wantUpserts {
				t.Fatalf("Upsert called %d times, want %d", tc.repo.upsertCalls, tc.wantUpserts)
			}

			switch {
			case tc.wantSentinel != nil:
				if !errors.Is(err, tc.wantSentinel) {
					t.Fatalf("got error %v, want %v", err, tc.wantSentinel)
				}
				return
			case tc.wantKind == apperror.KindValidation:
				if kind := apperror.KindOf(err); err == nil || kind != tc.wantKind {
					t.Fatalf("got %v (kind %v), want a validation error", err, kind)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(saved.Data) != string(tc.data) {
				t.Fatalf("got data %s, want %s", saved.Data, tc.data)
			}
			if saved.UserID.Int64() != tc.userID {
				t.Fatalf("got user %d, want %d", saved.UserID.Int64(), tc.userID)
			}
		})
	}
}

// Saving progress is activity: it must bump the user's counters, which live
// in another module and are reached through the ActivityRecorder port.
func TestServiceSaveRecordsActivity(t *testing.T) {
	t.Parallel()

	t.Run("records the owner as active", func(t *testing.T) {
		t.Parallel()

		recorder := &stubRecorder{}
		service := newService(&stubRepository{}, recorder)

		if _, err := service.Save(context.Background(), 42, json.RawMessage(`{"unit":1}`)); err != nil {
			t.Fatalf("save: %v", err)
		}
		if recorder.calls != 1 {
			t.Fatalf("RecordActivity called %d times, want 1", recorder.calls)
		}
		if recorder.users[0] != 42 {
			t.Fatalf("recorded user %d, want 42", recorder.users[0])
		}
	})

	t.Run("a failing recorder does not lose the document", func(t *testing.T) {
		t.Parallel()

		// The document is already persisted at this point: failing to bump
		// a streak must not turn a successful save into an error.
		recorder := &stubRecorder{err: errors.New("users table is on fire")}
		service := newService(&stubRepository{}, recorder)

		saved, err := service.Save(context.Background(), 42, json.RawMessage(`{"unit":1}`))
		if err != nil {
			t.Fatalf("save must succeed despite the recorder failing, got %v", err)
		}
		if string(saved.Data) != `{"unit":1}` {
			t.Fatalf("got data %s, want the saved document", saved.Data)
		}
	})

	t.Run("is skipped when the document is rejected", func(t *testing.T) {
		t.Parallel()

		recorder := &stubRecorder{}
		service := newService(&stubRepository{}, recorder)

		if _, err := service.Save(context.Background(), 42, json.RawMessage(`{"broken`)); err == nil {
			t.Fatal("expected a validation error")
		}
		if recorder.calls != 0 {
			t.Fatalf("RecordActivity called %d times for a rejected save, want 0", recorder.calls)
		}
	})
}
