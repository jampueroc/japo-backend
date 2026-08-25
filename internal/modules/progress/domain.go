// Package progress is the self contained module that stores the learning
// progress of a user as a JSON document, one row per user.
//
// The document is opaque on the write-everything path (PUT /progress): the
// client owns its schema and the server only checks that it is valid JSON
// within the size budget. The two grading endpoints are the exception, and
// the only reason mastery.go exists: to recalculate a score server side the
// server has to understand the v1 layout.
package progress

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// MaxDataSize caps the JSON document. The app is personal and the box is
// small: a megabyte of progress would be a bug, not a feature. 256 KiB fits
// both syllabaries with per skill scores and a long attempt window with room
// to spare.
const MaxDataSize = 256 * 1024

// Domain sentinel errors.
var (
	// ErrProgressNotFound means the user has never saved any progress.
	ErrProgressNotFound = errors.New("progress not found")
	// ErrUnknownOwner means the referenced user does not exist, which the
	// repository detects through the foreign key.
	ErrUnknownOwner = errors.New("unknown progress owner")
	// ErrUnreadableDocument means the stored document cannot be parsed, so
	// the server cannot safely recalculate anything from it.
	ErrUnreadableDocument = errors.New("unreadable progress document")
	// ErrUnsupportedSchema means the document was written by a newer client
	// than this server understands. Refusing to touch it is better than
	// rewriting it with the wrong rules.
	ErrUnsupportedSchema = errors.New("unsupported progress schema version")
	// ErrInvalidAnswer means the kana or the skill is not one this server
	// can score.
	ErrInvalidAnswer = errors.New("invalid answer")
	// ErrInvalidLesson means the lesson identifier is malformed.
	ErrInvalidLesson = errors.New("invalid lesson identifier")
	// ErrProtectedField means a patch tried to write a member the server
	// computes itself.
	ErrProtectedField = errors.New("protected progress field")
	// ErrEmptyPatch means the patch carries no members to merge.
	ErrEmptyPatch = errors.New("empty progress patch")
	// ErrUnreadablePatch means the request body is not a JSON object. It is
	// deliberately distinct from ErrUnreadableDocument: one blames the
	// caller, the other blames what is already stored, and answering a bad
	// request with "your stored document is broken" sends whoever is
	// debugging it in exactly the wrong direction.
	ErrUnreadablePatch = errors.New("unreadable progress patch")
)

// MaxLessonIDLength bounds a lesson identifier. The catalogue of lessons
// itself stays in the client: an unknown id is harmless (it sits in the
// document and the client ignores it), whereas a hardcoded list here would go
// stale every time the curriculum grows.
const MaxLessonIDLength = 64

// Answer is one graded attempt at a kana.
type Answer struct {
	Kana    string
	Skill   Skill
	Correct bool
	// Chosen is what the learner picked, and it is optional. It is only
	// meaningful on a wrong answer, where it records WHAT they confused the
	// kana with. It is free text because the options are not always kana:
	// a "read this character" question offers romaji.
	Chosen string
}

// MaxChosenLength bounds the recorded option. It is a quiz answer, not a
// document.
const MaxChosenLength = 32

// Clock returns the current time. Injected so the timestamps written into the
// document are deterministic in tests.
type Clock func() time.Time

// Progress is the entity: the whole document belonging to one user.
type Progress struct {
	ID        valueobject.ID
	UserID    valueobject.ID
	Data      json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ActivityRecorder marks the owner of a document as active today. Saving
// progress is activity, and the activity counters live on the user, which is
// owned by another module: the implementation is injected at the composition
// root so no module imports another.
type ActivityRecorder interface {
	RecordActivity(ctx context.Context, userID int64) error
}

// Repository is the persistence port.
type Repository interface {
	// FindByUserID returns ErrProgressNotFound when the user has no row.
	FindByUserID(ctx context.Context, userID valueobject.ID) (Progress, error)
	// Upsert inserts the row or overwrites the existing one for that user
	// (INSERT ... ON DUPLICATE KEY UPDATE) and returns the stored result.
	Upsert(ctx context.Context, userID valueobject.ID, data json.RawMessage) (Progress, error)
	// Mutate applies fn to the stored document inside a transaction that
	// locks the row, so two concurrent answers cannot overwrite each
	// other. fn receives nil when there is no document yet.
	Mutate(ctx context.Context, userID valueobject.ID, fn func(current json.RawMessage) (json.RawMessage, error)) (Progress, error)
}

// Service is the use case port consumed by the handlers. It takes the raw
// user id coming from the access token and validates it.
type Service interface {
	Get(ctx context.Context, userID int64) (Progress, error)
	// Save replaces the whole document with what the client sends.
	Save(ctx context.Context, userID int64, data json.RawMessage) (Progress, error)
	// RecordAnswer grades one attempt server side: it moves the skill
	// score, recomputes the mastery of that kana and appends to the
	// attempt window.
	RecordAnswer(ctx context.Context, userID int64, answer Answer) (Progress, error)
	// CompleteLesson adds a lesson to the completed list, atomically and
	// without duplicates.
	CompleteLesson(ctx context.Context, userID int64, lessonID string) (Progress, error)
	// Merge replaces the given top level members of the document and
	// leaves every other one alone. It is how a client keeps state of its
	// own next to the parts the server owns.
	Merge(ctx context.Context, userID int64, patch json.RawMessage) (Progress, error)
}
