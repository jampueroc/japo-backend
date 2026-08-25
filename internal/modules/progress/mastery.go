package progress

// This file is the part of the curriculum the server had to take over in
// order to recalculate progress itself: the scoring rules and the mastery
// thresholds. The catalogue of lessons stays in the client, and kana are
// validated by their Unicode blocks rather than by a hardcoded list, so
// neither can go stale when the curriculum grows.

// Skill is one of the four ways a kana is practised.
type Skill string

// The skills the client tracks per kana.
const (
	SkillRecognition Skill = "recognition"
	SkillWriting     Skill = "writing"
	SkillListening   Skill = "listening"
	SkillReading     Skill = "reading"
)

// Valid reports whether s is a skill the server knows how to score.
func (s Skill) Valid() bool {
	switch s {
	case SkillRecognition, SkillWriting, SkillListening, SkillReading:
		return true
	default:
		return false
	}
}

// Mastery states, mirrored from the client's model.
const (
	MasteryLocked     = "locked"
	MasteryDiscovered = "discovered"
	MasteryLearning   = "learning"
	MasteryFamiliar   = "familiar"
	MasteryMastered   = "mastered"
)

// Scoring rules. A mistake costs more than a hit gains: that asymmetry is
// what keeps the score close to what the learner actually retains instead of
// drifting up with lucky guesses.
const (
	scoreOnCorrect = 20
	scoreOnMistake = -30
	minScore       = 0
	maxScore       = 100
)

// Mastery thresholds, applied to the average of the four skills. A kana that
// is absent from the document is "locked"; one with at least one attempt is
// always at least "discovered".
const (
	thresholdLearning = 25
	thresholdFamiliar = 50
	thresholdMastered = 80
)

// Skills holds the 0..100 score of every skill for one kana.
type Skills struct {
	Recognition int `json:"recognition"`
	Writing     int `json:"writing"`
	Listening   int `json:"listening"`
	Reading     int `json:"reading"`
}

func (s Skills) score(skill Skill) int {
	switch skill {
	case SkillRecognition:
		return s.Recognition
	case SkillWriting:
		return s.Writing
	case SkillListening:
		return s.Listening
	case SkillReading:
		return s.Reading
	default:
		return 0
	}
}

func (s *Skills) setScore(skill Skill, value int) {
	switch skill {
	case SkillRecognition:
		s.Recognition = value
	case SkillWriting:
		s.Writing = value
	case SkillListening:
		s.Listening = value
	case SkillReading:
		s.Reading = value
	}
}

// average is the mean of the four skills, which is what mastery is derived
// from: being perfect at recognition alone does not make a kana mastered.
func (s Skills) average() int {
	return (s.Recognition + s.Writing + s.Listening + s.Reading) / 4
}

// applyAnswer moves a single score after one attempt, clamped to 0..100.
func applyAnswer(current int, correct bool) int {
	delta := scoreOnMistake
	if correct {
		delta = scoreOnCorrect
	}
	return clamp(current + delta)
}

func clamp(score int) int {
	switch {
	case score < minScore:
		return minScore
	case score > maxScore:
		return maxScore
	default:
		return score
	}
}

// masteryFor derives the mastery state of a kana that has been practised at
// least once. Because it is recomputed from the scores every time, a kana can
// also fall back when the learner starts missing it.
func masteryFor(skills Skills) string {
	switch average := skills.average(); {
	case average >= thresholdMastered:
		return MasteryMastered
	case average >= thresholdFamiliar:
		return MasteryFamiliar
	case average >= thresholdLearning:
		return MasteryLearning
	default:
		return MasteryDiscovered
	}
}

// maxKanaRunes bounds a single character unit: a base kana, an optional small
// ya/yu/yo to make a digraph, and an optional chōonpu.
const maxKanaRunes = 3

// chōonpu is the katakana long vowel mark, ー.
const chōonpu = 'ー'

// validKana reports whether s is ONE character of the syllabary: a single
// kana, or a digraph such as きゃ or ちょ.
//
// Counting kana runes is not enough to decide this. きゃ and つき are both two
// kana, but the first is one character and the second is a word: what tells
// them apart is that a digraph ends in a SMALL ya/yu/yo. Accepting つき would
// silently create a mastery entry keyed on a word, which nothing in the
// client would ever look up.
//
// Validating by structure instead of against a catalogue means this never
// goes stale when the curriculum adds a character.
func validKana(s string) bool {
	runes := []rune(s)
	if len(runes) == 0 || len(runes) > maxKanaRunes {
		return false
	}

	if !isKanaBase(runes[0]) {
		// A small kana on its own (ゃ, っ) is still a character of the
		// syllabary and worth practising.
		return len(runes) == 1 && isKana(runes[0])
	}

	rest := runes[1:]
	if len(rest) > 0 && isSmallYaYuYo(rest[0]) {
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0] == chōonpu {
		rest = rest[1:]
	}

	// Anything left over means this was a sequence, not a character.
	return len(rest) == 0
}

// isKanaBase reports whether r can open a character unit: any kana that is
// not itself a modifier.
func isKanaBase(r rune) bool {
	return isKana(r) && !isSmallYaYuYo(r) && r != chōonpu
}

// isSmallYaYuYo reports whether r is one of the small kana that turn the
// preceding one into a digraph.
func isSmallYaYuYo(r rune) bool {
	switch r {
	case 'ゃ', 'ゅ', 'ょ', 'ャ', 'ュ', 'ョ':
		return true
	default:
		return false
	}
}

func isKana(r rune) bool {
	switch {
	case r >= 0x3041 && r <= 0x3096: // hiragana
		return true
	case r >= 0x30A1 && r <= 0x30FA: // katakana
		return true
	case r == chōonpu:
		return true
	default:
		return false
	}
}
