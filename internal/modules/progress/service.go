package progress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// ServiceDeps groups the ports the progress service needs. A struct keeps the
// constructor readable and forces the composition root to name what it wires.
type ServiceDeps struct {
	Repo Repository
	// Activity is optional: without it, writing progress does not touch
	// the user's streak counters.
	Activity ActivityRecorder
	// Clock is optional and defaults to time.Now.
	Clock  Clock
	Logger *slog.Logger
}

// service implements Service on top of the Repository port.
type service struct {
	repo     Repository
	activity ActivityRecorder
	clock    Clock
	logger   *slog.Logger
}

// NewService wires the use cases.
func NewService(deps ServiceDeps) Service {
	clock := deps.Clock
	if clock == nil {
		clock = time.Now
	}
	return &service{repo: deps.Repo, activity: deps.Activity, clock: clock, logger: deps.Logger}
}

// Get returns the progress document of the given user.
func (s *service) Get(ctx context.Context, userID int64) (Progress, error) {
	id, err := valueobject.NewID(userID)
	if err != nil {
		return Progress{}, err
	}

	found, err := s.repo.FindByUserID(ctx, id)
	if err != nil {
		// Known errors travel untouched so the handler can map them.
		if isKnown(err) {
			return Progress{}, err
		}
		return Progress{}, fmt.Errorf("get progress: %w", err)
	}
	return found, nil
}

// Save creates or replaces the progress document of the given user.
func (s *service) Save(ctx context.Context, userID int64, data json.RawMessage) (Progress, error) {
	id, err := valueobject.NewID(userID)
	if err != nil {
		return Progress{}, err
	}
	if err := validateData(data); err != nil {
		return Progress{}, err
	}

	saved, err := s.repo.Upsert(ctx, id, data)
	if err != nil {
		if isKnown(err) {
			return Progress{}, err
		}
		return Progress{}, fmt.Errorf("save progress: %w", err)
	}

	s.recordActivity(ctx, id)

	s.logger.InfoContext(ctx, "progress saved",
		slog.Int64("user_id", id.Int64()),
		slog.Int("bytes", len(data)),
	)
	return saved, nil
}

// RecordAnswer grades one attempt: it moves the score of that skill,
// recomputes the mastery of the kana from the four skills and appends to the
// rolling attempt window. The whole read-modify-write runs inside a locked
// transaction, so two answers sent at the same time cannot lose each other.
func (s *service) RecordAnswer(ctx context.Context, userID int64, answer Answer) (Progress, error) {
	id, err := valueobject.NewID(userID)
	if err != nil {
		return Progress{}, err
	}
	if err := validateAnswer(answer); err != nil {
		return Progress{}, err
	}

	saved, err := s.mutate(ctx, id, func(doc document) error {
		skills, err := doc.skills()
		if err != nil {
			return err
		}
		mastery, err := doc.mastery()
		if err != nil {
			return err
		}

		current := skills[answer.Kana]
		current.setScore(answer.Skill, applyAnswer(current.score(answer.Skill), answer.Correct))
		skills[answer.Kana] = current
		mastery[answer.Kana] = masteryFor(current)

		if err := doc.set(fieldSkills, skills); err != nil {
			return err
		}
		if err := doc.set(fieldMastery, mastery); err != nil {
			return err
		}
		return doc.appendAttempt(Attempt{
			Kana:    answer.Kana,
			Skill:   answer.Skill,
			Correct: answer.Correct,
			Chosen:  answer.Chosen,
			At:      s.clock().UTC(),
		})
	})
	if err != nil {
		return Progress{}, err
	}

	s.recordActivity(ctx, id)

	s.logger.InfoContext(ctx, "answer recorded",
		slog.Int64("user_id", id.Int64()),
		slog.String("kana", answer.Kana),
		slog.String("skill", string(answer.Skill)),
		slog.Bool("correct", answer.Correct),
		slog.String("chosen", answer.Chosen),
	)
	return saved, nil
}

// CompleteLesson adds a lesson to the completed list. It is idempotent: the
// list is kept sorted and free of duplicates, so replaying a lesson changes
// nothing.
func (s *service) CompleteLesson(ctx context.Context, userID int64, lessonID string) (Progress, error) {
	id, err := valueobject.NewID(userID)
	if err != nil {
		return Progress{}, err
	}

	lessonID = strings.TrimSpace(lessonID)
	if err := validateLessonID(lessonID); err != nil {
		return Progress{}, err
	}

	saved, err := s.mutate(ctx, id, func(doc document) error {
		lessons, err := doc.lessons()
		if err != nil {
			return err
		}
		return doc.set(fieldLessons, sortedUnique(append(lessons, lessonID)))
	})
	if err != nil {
		return Progress{}, err
	}

	s.recordActivity(ctx, id)

	s.logger.InfoContext(ctx, "lesson completed",
		slog.Int64("user_id", id.Int64()),
		slog.String("lesson_id", lessonID),
	)
	return saved, nil
}

// Merge replaces the given top level members and leaves the rest of the
// document alone. It exists because /answer only rewrites the members it
// understands: without it, a client with state of its own (a review
// schedule, say) would have to PUT the whole document and race with itself.
//
// It deliberately does NOT count as activity. A patch is a background sync of
// something the user already did, and the call that did it recorded the day
// already; counting it again would let a sync extend a streak on its own.
func (s *service) Merge(ctx context.Context, userID int64, patch json.RawMessage) (Progress, error) {
	id, err := valueobject.NewID(userID)
	if err != nil {
		return Progress{}, err
	}

	changes, err := parsePatch(patch)
	if err != nil {
		return Progress{}, err
	}

	saved, err := s.mutate(ctx, id, func(doc document) error {
		return doc.merge(changes)
	})
	if err != nil {
		return Progress{}, err
	}

	s.logger.InfoContext(ctx, "progress patched",
		slog.Int64("user_id", id.Int64()),
		slog.Int("fields", len(changes)),
	)
	return saved, nil
}

// parsePatch reads the partial document, rejecting anything that is not a
// JSON object with at least one member.
func parsePatch(patch json.RawMessage) (document, error) {
	if len(patch) == 0 {
		return nil, fmt.Errorf("%w: the request body is empty", ErrEmptyPatch)
	}
	if len(patch) > MaxDataSize {
		return nil, apperror.Validation("invalid_progress_data",
			fmt.Sprintf("the patch must be at most %d bytes", MaxDataSize))
	}

	var changes document
	if err := json.Unmarshal(patch, &changes); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadablePatch, err)
	}
	if changes == nil {
		return nil, fmt.Errorf("%w: the patch is not a JSON object", ErrUnreadablePatch)
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("%w: no members to merge", ErrEmptyPatch)
	}
	return changes, nil
}

// mutate runs a server side change through the repository transaction,
// translating the document between raw bytes and the parsed view.
func (s *service) mutate(ctx context.Context, id valueobject.ID, apply func(doc document) error) (Progress, error) {
	saved, err := s.repo.Mutate(ctx, id, func(current json.RawMessage) (json.RawMessage, error) {
		doc, err := parseDocument(current)
		if err != nil {
			return nil, err
		}
		if err := apply(doc); err != nil {
			return nil, err
		}

		encoded, err := doc.marshal()
		if err != nil {
			return nil, err
		}
		if err := validateData(encoded); err != nil {
			return nil, err
		}
		return encoded, nil
	})
	if err != nil {
		if isKnown(err) {
			return Progress{}, err
		}
		return Progress{}, fmt.Errorf("mutate progress: %w", err)
	}
	return saved, nil
}

// validateAnswer keeps the grading endpoints honest regardless of the DTO
// rules: an unknown kana or skill must never reach the document.
func validateAnswer(answer Answer) error {
	if !answer.Skill.Valid() {
		return fmt.Errorf("%w: %q is not a skill this server scores", ErrInvalidAnswer, answer.Skill)
	}
	if !validKana(answer.Kana) {
		return fmt.Errorf("%w: %q is not a kana", ErrInvalidAnswer, answer.Kana)
	}
	if len(answer.Chosen) > MaxChosenLength {
		return fmt.Errorf("%w: the chosen option is longer than %d bytes",
			ErrInvalidAnswer, MaxChosenLength)
	}
	return nil
}

func validateLessonID(lessonID string) error {
	if lessonID == "" {
		return fmt.Errorf("%w: it is empty", ErrInvalidLesson)
	}
	if len(lessonID) > MaxLessonIDLength {
		return fmt.Errorf("%w: longer than %d characters", ErrInvalidLesson, MaxLessonIDLength)
	}
	for _, r := range lessonID {
		isAllowed := r == '-' || r == '_' || r == '.' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !isAllowed {
			return fmt.Errorf("%w: %q contains unsupported characters", ErrInvalidLesson, lessonID)
		}
	}
	return nil
}

// recordActivity is best effort: the document is already persisted, so a
// failure to bump the streak must not turn a successful save into an error.
func (s *service) recordActivity(ctx context.Context, userID valueobject.ID) {
	if s.activity == nil {
		return
	}
	if err := s.activity.RecordActivity(ctx, userID.Int64()); err != nil {
		s.logger.WarnContext(ctx, "could not record user activity",
			slog.Int64("user_id", userID.Int64()),
			slog.Any("error", err),
		)
	}
}

// validateData guards the use case regardless of the caller: the payload must
// be a non empty, valid and reasonably sized JSON document.
func validateData(data json.RawMessage) error {
	switch {
	case len(data) == 0:
		return apperror.Validation("invalid_progress_data", "data is required")
	case len(data) > MaxDataSize:
		return apperror.Validation("invalid_progress_data",
			fmt.Sprintf("data must be at most %d bytes", MaxDataSize))
	case !json.Valid(data):
		return apperror.Validation("invalid_progress_data", "data must be a valid JSON document")
	default:
		return nil
	}
}

// isKnown reports whether err is already a domain sentinel or an application
// error, in which case it must reach the transport layer untouched instead of
// being wrapped into an opaque internal failure.
func isKnown(err error) bool {
	var appErr *apperror.Error
	return errors.Is(err, ErrProgressNotFound) ||
		errors.Is(err, ErrUnknownOwner) ||
		errors.Is(err, ErrProtectedField) ||
		errors.Is(err, ErrEmptyPatch) ||
		errors.Is(err, ErrUnreadablePatch) ||
		errors.Is(err, ErrUnreadableDocument) ||
		errors.Is(err, ErrUnsupportedSchema) ||
		errors.Is(err, ErrInvalidAnswer) ||
		errors.Is(err, ErrInvalidLesson) ||
		errors.As(err, &appErr)
}
