package progress

import (
	"encoding/json"
	"strings"
)

// The wire format of this module is the progress document itself: the API
// stores an opaque JSON blob whose schema is owned by the client (it carries
// its own schemaVersion), so wrapping it here would only add a layer the
// client has to unwrap.
//
//	PUT /progress                 body: the document -> 200 the stored document
//	GET /progress                                    -> 200 the stored document
//
// The two grading endpoints do take a typed payload, because the server has
// to understand what it is being asked to score:
//
//	POST /progress/answer          {kana, skill, correct} -> 200 the document
//	POST /progress/lesson-complete {lessonId}             -> 200 the document

// AnswerRequest is the POST /progress/answer payload.
type AnswerRequest struct {
	Kana  string `json:"kana" validate:"required,max=12"`
	Skill string `json:"skill" validate:"required,oneof=recognition writing listening reading"`
	// Correct is a pointer on purpose: with a plain bool, an explicit
	// false would be indistinguishable from a missing field and the
	// `required` rule would reject a perfectly good wrong answer.
	Correct *bool `json:"correct" validate:"required"`
	// Chosen is optional: what the learner picked. Not validated as a kana,
	// because a "read this character" question offers romaji.
	Chosen string `json:"chosen" validate:"omitempty,maxbytes=32"`
}

// answer maps the DTO onto the use case input.
func (r AnswerRequest) answer() Answer {
	return Answer{
		Kana:    r.Kana,
		Skill:   Skill(r.Skill),
		Correct: r.Correct != nil && *r.Correct,
		Chosen:  strings.TrimSpace(r.Chosen),
	}
}

// LessonCompleteRequest is the POST /progress/lesson-complete payload.
type LessonCompleteRequest struct {
	LessonID string `json:"lessonId" validate:"required,max=64"`
}

// documentFromBody copies the request body. fasthttp reuses its buffers, so
// the bytes must not be handed to the service and the repository by
// reference.
func documentFromBody(body []byte) json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), body...)
}
