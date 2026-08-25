package progress_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jorgeampuero/japo-backend/internal/modules/progress"
)

// The grading endpoints are the part of the curriculum the server took over,
// so the scoring rules and the thresholds are pinned down here.

// storedDocument decodes what the repository double holds.
func storedDocument(t *testing.T, repo *stubRepository) map[string]json.RawMessage {
	t.Helper()

	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(repo.stored, &doc); err != nil {
		t.Fatalf("stored document is not a JSON object (%s): %v", repo.stored, err)
	}
	return doc
}

func storedSkills(t *testing.T, repo *stubRepository, kana string) progress.Skills {
	t.Helper()

	skills := map[string]progress.Skills{}
	if raw, ok := storedDocument(t, repo)["skillsByKana"]; ok {
		if err := json.Unmarshal(raw, &skills); err != nil {
			t.Fatalf("skillsByKana is malformed: %v", err)
		}
	}
	return skills[kana]
}

func storedMastery(t *testing.T, repo *stubRepository, kana string) string {
	t.Helper()

	mastery := map[string]string{}
	if raw, ok := storedDocument(t, repo)["masteryByKana"]; ok {
		if err := json.Unmarshal(raw, &mastery); err != nil {
			t.Fatalf("masteryByKana is malformed: %v", err)
		}
	}
	return mastery[kana]
}

// answer applies one attempt and fails the test if it is rejected.
func answer(t *testing.T, service progress.Service, kana string, skill progress.Skill, correct bool) {
	t.Helper()

	if _, err := service.RecordAnswer(context.Background(), 42,
		progress.Answer{Kana: kana, Skill: skill, Correct: correct}); err != nil {
		t.Fatalf("record answer: %v", err)
	}
}

func TestServiceRecordAnswerScoring(t *testing.T) {
	t.Parallel()

	const kana = "あ"
	everySkill := []progress.Skill{
		progress.SkillRecognition,
		progress.SkillWriting,
		progress.SkillListening,
		progress.SkillReading,
	}

	t.Run("a hit moves one skill and leaves the others alone", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepository{}
		service := newService(repo, nil)

		answer(t, service, kana, progress.SkillRecognition, true)

		got := storedSkills(t, repo, kana)
		want := progress.Skills{Recognition: 20}
		if got != want {
			t.Fatalf("got skills %+v, want %+v", got, want)
		}
		// One hit out of four skills averages 5: seen, not learned yet.
		if state := storedMastery(t, repo, kana); state != progress.MasteryDiscovered {
			t.Fatalf("got mastery %q, want %q", state, progress.MasteryDiscovered)
		}
	})

	t.Run("a mistake costs more than a hit gains", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepository{}
		service := newService(repo, nil)

		answer(t, service, kana, progress.SkillWriting, true)
		answer(t, service, kana, progress.SkillWriting, true)
		answer(t, service, kana, progress.SkillWriting, false)

		if got := storedSkills(t, repo, kana).Writing; got != 10 {
			t.Fatalf("got writing score %d, want 10 (20 + 20 - 30)", got)
		}
	})

	t.Run("scores are clamped to 0 and 100", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepository{}
		service := newService(repo, nil)

		for range 8 {
			answer(t, service, kana, progress.SkillReading, true)
		}
		if got := storedSkills(t, repo, kana).Reading; got != 100 {
			t.Fatalf("got reading score %d, want it capped at 100", got)
		}

		answer(t, service, kana, progress.SkillListening, false)
		if got := storedSkills(t, repo, kana).Listening; got != 0 {
			t.Fatalf("got listening score %d, want it floored at 0", got)
		}
	})

	t.Run("mastery needs every skill, and can be lost again", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepository{}
		service := newService(repo, nil)

		// Four hits per skill puts each of them at 80: average 80.
		for _, skill := range everySkill {
			for range 4 {
				answer(t, service, kana, skill, true)
			}
		}
		if state := storedMastery(t, repo, kana); state != progress.MasteryMastered {
			t.Fatalf("got mastery %q, want %q", state, progress.MasteryMastered)
		}

		// One mistake drops writing to 50, so the average falls to 72.
		answer(t, service, kana, progress.SkillWriting, false)
		if state := storedMastery(t, repo, kana); state != progress.MasteryFamiliar {
			t.Fatalf("got mastery %q, want %q after a mistake", state, progress.MasteryFamiliar)
		}
	})

	t.Run("each kana is scored independently", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepository{}
		service := newService(repo, nil)

		answer(t, service, "あ", progress.SkillRecognition, true)
		answer(t, service, "い", progress.SkillRecognition, false)

		if got := storedSkills(t, repo, "あ").Recognition; got != 20 {
			t.Fatalf("got あ recognition %d, want 20", got)
		}
		if got := storedSkills(t, repo, "い").Recognition; got != 0 {
			t.Fatalf("got い recognition %d, want 0", got)
		}
	})
}

// The server rewrites only the keys it owns: anything the client adds later
// has to survive a grading call, otherwise a new client field would vanish
// the first time the user answers a question.
func TestServiceRecordAnswerPreservesUnknownFields(t *testing.T) {
	t.Parallel()

	repo := &stubRepository{stored: json.RawMessage(
		`{"schemaVersion":1,"masteryByKana":{},"settings":{"theme":"dark"},"futureField":[1,2,3]}`)}
	service := newService(repo, nil)

	answer(t, service, "か", progress.SkillRecognition, true)

	doc := storedDocument(t, repo)
	if got := string(doc["settings"]); got != `{"theme":"dark"}` {
		t.Fatalf("got settings %s, want the client's value untouched", got)
	}
	if got := string(doc["futureField"]); got != `[1,2,3]` {
		t.Fatalf("got futureField %s, want the client's value untouched", got)
	}
}

// The rolling window is what keeps the document from growing until it hits
// MaxDataSize weeks later.
func TestServiceRecordAnswerPrunesTheAttemptWindow(t *testing.T) {
	t.Parallel()

	repo := &stubRepository{}
	service := newService(repo, nil)

	for range progress.MaxRecentAttempts + 25 {
		answer(t, service, "さ", progress.SkillReading, true)
	}

	var attempts []progress.Attempt
	if err := json.Unmarshal(storedDocument(t, repo)["recentAttempts"], &attempts); err != nil {
		t.Fatalf("recentAttempts is malformed: %v", err)
	}
	if len(attempts) != progress.MaxRecentAttempts {
		t.Fatalf("kept %d attempts, want %d", len(attempts), progress.MaxRecentAttempts)
	}
	if !attempts[0].At.Equal(fixedNow) {
		t.Fatalf("got attempt time %v, want the injected clock", attempts[0].At)
	}
}

func TestServiceRecordAnswerRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stored  string
		answer  progress.Answer
		wantErr error
	}{
		{
			name:    "an unknown skill",
			answer:  progress.Answer{Kana: "あ", Skill: "telepathy", Correct: true},
			wantErr: progress.ErrInvalidAnswer,
		},
		{
			name:    "a character that is not kana",
			answer:  progress.Answer{Kana: "A", Skill: progress.SkillReading, Correct: true},
			wantErr: progress.ErrInvalidAnswer,
		},
		{
			name:    "a kanji",
			answer:  progress.Answer{Kana: "日", Skill: progress.SkillReading, Correct: true},
			wantErr: progress.ErrInvalidAnswer,
		},
		{
			name:    "a whole word",
			answer:  progress.Answer{Kana: "ありがとう", Skill: progress.SkillReading, Correct: true},
			wantErr: progress.ErrInvalidAnswer,
		},
		{
			name:    "an empty kana",
			answer:  progress.Answer{Kana: "", Skill: progress.SkillReading, Correct: true},
			wantErr: progress.ErrInvalidAnswer,
		},
		{
			name:    "a document from a newer client",
			stored:  `{"schemaVersion":99,"masteryByKana":{}}`,
			answer:  progress.Answer{Kana: "あ", Skill: progress.SkillReading, Correct: true},
			wantErr: progress.ErrUnsupportedSchema,
		},
		{
			name:    "a document that is not an object",
			stored:  `["not","a","document"]`,
			answer:  progress.Answer{Kana: "あ", Skill: progress.SkillReading, Correct: true},
			wantErr: progress.ErrUnreadableDocument,
		},
		{
			name:    "a malformed member",
			stored:  `{"schemaVersion":1,"skillsByKana":"nonsense"}`,
			answer:  progress.Answer{Kana: "あ", Skill: progress.SkillReading, Correct: true},
			wantErr: progress.ErrUnreadableDocument,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := &stubRepository{stored: json.RawMessage(tc.stored)}
			service := newService(repo, nil)

			if _, err := service.RecordAnswer(context.Background(), 42, tc.answer); !errors.Is(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// What counts as ONE character of the syllabary. Rune counting alone cannot
// decide this: きゃ and つき are both two kana, but the first is a character
// and the second is a word. Accepting a word would key the mastery map on
// something the client never looks up.
func TestServiceRecordAnswerAcceptsOnlySingleCharacters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		kana  string
		valid bool
	}{
		{name: "a plain kana", kana: "あ", valid: true},
		{name: "katakana", kana: "ア", valid: true},
		{name: "a digraph", kana: "きゃ", valid: true},
		{name: "another digraph", kana: "ちょ", valid: true},
		{name: "katakana digraph", kana: "キャ", valid: true},
		{name: "a small tsu on its own", kana: "ッ", valid: true},
		{name: "n", kana: "ん", valid: true},
		{name: "kana plus a long vowel mark", kana: "ラー", valid: true},
		{name: "digraph plus a long vowel mark", kana: "キャー", valid: true},

		{name: "a two kana word", kana: "つき", valid: false},
		{name: "another two kana word", kana: "ねこ", valid: false},
		{name: "three kana", kana: "ラーメ", valid: false},
		{name: "a whole word", kana: "ありがとう", valid: false},
		{name: "a kanji", kana: "日", valid: false},
		{name: "latin", kana: "A", valid: false},
		{name: "empty", kana: "", valid: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := &stubRepository{}
			service := newService(repo, nil)

			_, err := service.RecordAnswer(context.Background(), 42,
				progress.Answer{Kana: tc.kana, Skill: progress.SkillWriting, Correct: true})

			if tc.valid && err != nil {
				t.Fatalf("rejected %q, which is a single character: %v", tc.kana, err)
			}
			if !tc.valid && !errors.Is(err, progress.ErrInvalidAnswer) {
				t.Fatalf("accepted %q, which is not a single character (got %v)", tc.kana, err)
			}
		})
	}
}

func TestServiceCompleteLesson(t *testing.T) {
	t.Parallel()

	t.Run("adds the lesson once, however many times it is replayed", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepository{}
		service := newService(repo, nil)
		ctx := context.Background()

		for _, lessonID := range []string{"l2", "l1", "l2", "l1", "l3"} {
			if _, err := service.CompleteLesson(ctx, 42, lessonID); err != nil {
				t.Fatalf("complete lesson %q: %v", lessonID, err)
			}
		}

		var lessons []string
		if err := json.Unmarshal(storedDocument(t, repo)["completedLessonIds"], &lessons); err != nil {
			t.Fatalf("completedLessonIds is malformed: %v", err)
		}
		if want := []string{"l1", "l2", "l3"}; strings.Join(lessons, ",") != strings.Join(want, ",") {
			t.Fatalf("got lessons %v, want %v", lessons, want)
		}
	})

	t.Run("records the owner as active", func(t *testing.T) {
		t.Parallel()

		recorder := &stubRecorder{}
		service := newService(&stubRepository{}, recorder)

		if _, err := service.CompleteLesson(context.Background(), 42, "l1"); err != nil {
			t.Fatalf("complete lesson: %v", err)
		}
		if recorder.calls != 1 {
			t.Fatalf("RecordActivity called %d times, want 1", recorder.calls)
		}
	})

	t.Run("rejects malformed identifiers", func(t *testing.T) {
		t.Parallel()

		for _, lessonID := range []string{"", "   ", "l1; DROP TABLE users", "l/1", strings.Repeat("l", 65)} {
			repo := &stubRepository{}
			service := newService(repo, nil)

			_, err := service.CompleteLesson(context.Background(), 42, lessonID)
			if !errors.Is(err, progress.ErrInvalidLesson) {
				t.Fatalf("for %q got error %v, want %v", lessonID, err, progress.ErrInvalidLesson)
			}
			if repo.mutateCalls != 0 {
				t.Fatalf("for %q the repository was touched %d times, want 0", lessonID, repo.mutateCalls)
			}
		}
	})
}

// --- schema v2 -------------------------------------------------------------

// v2 is what makes "which characters do you confuse with which" computable:
// v1 stored only {correct, at}, which cannot answer it at all.
func TestServiceRecordAnswerStoresTheFullAttempt(t *testing.T) {
	t.Parallel()

	repo := &stubRepository{}
	service := newService(repo, nil)

	if _, err := service.RecordAnswer(context.Background(), 42, progress.Answer{
		Kana: "し", Skill: progress.SkillListening, Correct: false, Chosen: "つ",
	}); err != nil {
		t.Fatalf("record answer: %v", err)
	}

	attempts := storedAttempts(t, repo)
	if len(attempts) != 1 {
		t.Fatalf("stored %d attempts, want 1", len(attempts))
	}

	got := attempts[0]
	want := progress.Attempt{
		Kana: "し", Skill: progress.SkillListening, Correct: false, Chosen: "つ", At: fixedNow,
	}
	if got != want {
		t.Fatalf("got attempt %+v, want %+v", got, want)
	}

	if version := storedVersion(t, repo); version != progress.SupportedSchemaVersion {
		t.Fatalf("got schemaVersion %d, want %d", version, progress.SupportedSchemaVersion)
	}
}

func TestServiceRecordAnswerChosenIsOptional(t *testing.T) {
	t.Parallel()

	repo := &stubRepository{}
	service := newService(repo, nil)

	if _, err := service.RecordAnswer(context.Background(), 42, progress.Answer{
		Kana: "あ", Skill: progress.SkillReading, Correct: true,
	}); err != nil {
		t.Fatalf("record answer: %v", err)
	}

	// Absent rather than empty: a correct answer has nothing to confuse.
	if raw, ok := storedDocument(t, repo)["recentAttempts"]; !ok {
		t.Fatal("no attempts stored")
	} else if strings.Contains(string(raw), "chosen") {
		t.Fatalf("an empty chosen was serialised: %s", raw)
	}

	if _, err := service.RecordAnswer(context.Background(), 42, progress.Answer{
		Kana: "あ", Skill: progress.SkillReading, Correct: false,
		Chosen: strings.Repeat("x", progress.MaxChosenLength+1),
	}); !errors.Is(err, progress.ErrInvalidAnswer) {
		t.Fatalf("an oversized chosen was accepted: %v", err)
	}
}

// The regression that matters most in this migration: every document out
// there today is v1, and bumping the version with an equality check would
// have answered 409 to all of them.
func TestServiceRecordAnswerUpgradesV1Documents(t *testing.T) {
	t.Parallel()

	repo := &stubRepository{stored: json.RawMessage(
		`{"schemaVersion":1,"masteryByKana":{"あ":"discovered"},` +
			`"skillsByKana":{"あ":{"recognition":20,"writing":0,"listening":0,"reading":0}},` +
			`"recentAttempts":[{"correct":true,"at":"2026-08-01T10:00:00Z"}]}`)}
	service := newService(repo, nil)

	if _, err := service.RecordAnswer(context.Background(), 42, progress.Answer{
		Kana: "い", Skill: progress.SkillWriting, Correct: true,
	}); err != nil {
		t.Fatalf("a v1 document must still be writable: %v", err)
	}

	if version := storedVersion(t, repo); version != progress.SupportedSchemaVersion {
		t.Fatalf("got schemaVersion %d, want the document upgraded to %d",
			version, progress.SupportedSchemaVersion)
	}

	attempts := storedAttempts(t, repo)
	if len(attempts) != 2 {
		t.Fatalf("got %d attempts, want the old one kept alongside the new", len(attempts))
	}
	// The old entry keeps what it had and gains nothing invented: v1 simply
	// did not record which kana it was.
	if attempts[0].Kana != "" || attempts[0].Skill != "" || !attempts[0].Correct {
		t.Fatalf("the v1 entry was altered: %+v", attempts[0])
	}
	if attempts[1].Kana != "い" || attempts[1].Skill != progress.SkillWriting {
		t.Fatalf("the new entry is not v2: %+v", attempts[1])
	}
	// And the rest of the document survived the upgrade untouched.
	if got := storedSkills(t, repo, "あ").Recognition; got != 20 {
		t.Fatalf("got あ recognition %d, want the stored 20", got)
	}
}

func storedAttempts(t *testing.T, repo *stubRepository) []progress.Attempt {
	t.Helper()

	var attempts []progress.Attempt
	raw, ok := storedDocument(t, repo)["recentAttempts"]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, &attempts); err != nil {
		t.Fatalf("recentAttempts is malformed: %v", err)
	}
	return attempts
}

func storedVersion(t *testing.T, repo *stubRepository) int {
	t.Helper()

	var version int
	if err := json.Unmarshal(storedDocument(t, repo)["schemaVersion"], &version); err != nil {
		t.Fatalf("schemaVersion is malformed: %v", err)
	}
	return version
}
