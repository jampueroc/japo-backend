package progress

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Schema versions the server understands.
//
// v1 stored each attempt as {correct, at}. v2 adds the kana, the skill and
// the option the learner picked, which is what makes "which characters do you
// confuse with which" computable at all.
//
// Reading accepts the whole range and writing always stamps the newest: a v1
// document is upgraded in place the first time it is touched. Old entries are
// left alone rather than rewritten, because the information they lack cannot
// be invented; they age out on their own as the rolling window turns over.
const (
	// SupportedSchemaVersion is the layout the server writes.
	SupportedSchemaVersion = 2
	// MinSupportedSchemaVersion is the oldest layout it can still read.
	MinSupportedSchemaVersion = 1
)

// MaxRecentAttempts bounds the rolling window kept by the server. Without it
// the document would grow until it hit MaxDataSize and writes would start
// failing weeks later, which is a nasty way to find out about a limit.
const MaxRecentAttempts = 200

// Document field names.
const (
	fieldSchemaVersion = "schemaVersion"
	fieldMastery       = "masteryByKana"
	fieldSkills        = "skillsByKana"
	fieldLessons       = "completedLessonIds"
	fieldAttempts      = "recentAttempts"
)

// protectedFields are the members the server computes. A patch that carried
// them would silently overwrite a mastery or an attempt window the client had
// no business recalculating, usually because it sent a copy it had gone stale
// on. Rejecting them turns that mistake into an error instead of data loss.
//
// This is a guard against accidents, not a security boundary: PUT still
// replaces the whole document, because the guest to account migration has to
// be able to write all of it.
var protectedFields = map[string]struct{}{
	fieldSchemaVersion: {},
	fieldMastery:       {},
	fieldSkills:        {},
	fieldAttempts:      {},
}

// merge replaces each member of the patch, leaving the rest of the document
// untouched. The replacement is per top level key: sending srsByKana swaps
// the whole map, which is what makes removing an entry expressible at all.
func (d document) merge(patch document) error {
	if len(patch) == 0 {
		return fmt.Errorf("%w: nothing to merge", ErrEmptyPatch)
	}

	for field := range patch {
		if _, protected := protectedFields[field]; protected {
			return fmt.Errorf("%w: %s is maintained by the server", ErrProtectedField, field)
		}
	}

	for field, value := range patch {
		d[field] = value
	}
	return nil
}

// Attempt is one answer, as stored in the rolling window. Kana, Skill and
// Chosen are omitted when empty so that a v1 entry, read and written back
// untouched, does not sprout meaningless keys.
type Attempt struct {
	Kana    string `json:"kana,omitempty"`
	Skill   Skill  `json:"skill,omitempty"`
	Correct bool   `json:"correct"`
	// Chosen is the option the learner picked when they got it wrong. It is
	// what turns "failed on し" into "confuses し with つ".
	Chosen string    `json:"chosen,omitempty"`
	At     time.Time `json:"at"`
}

// document is the client's JSON object kept as raw members. Mutations rewrite
// only the keys the server owns, so any field the client adds later survives
// the round trip instead of being silently dropped by a typed struct.
type document map[string]json.RawMessage

// parseDocument reads a stored document. A nil or empty input yields a fresh
// one, which is what the first answer of a new account writes.
func parseDocument(raw json.RawMessage) (document, error) {
	if len(raw) == 0 {
		return document{}, nil
	}

	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadableDocument, err)
	}
	if doc == nil {
		return nil, fmt.Errorf("%w: the document is not a JSON object", ErrUnreadableDocument)
	}

	version, err := doc.schemaVersion()
	if err != nil {
		return nil, err
	}
	if version < MinSupportedSchemaVersion || version > SupportedSchemaVersion {
		return nil, fmt.Errorf("%w: got version %d, this server reads %d to %d",
			ErrUnsupportedSchema, version, MinSupportedSchemaVersion, SupportedSchemaVersion)
	}

	return doc, nil
}

// schemaVersion defaults to the oldest layout: a document written by a plain
// PUT before the client started stamping it predates versioning, so it is
// read as v1 and upgraded on the next write.
func (d document) schemaVersion() (int, error) {
	raw, ok := d[fieldSchemaVersion]
	if !ok {
		return MinSupportedSchemaVersion, nil
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil {
		return 0, fmt.Errorf("%w: %s is not a number", ErrUnreadableDocument, fieldSchemaVersion)
	}
	return version, nil
}

func (d document) skills() (map[string]Skills, error) {
	raw, ok := d[fieldSkills]
	if !ok {
		return map[string]Skills{}, nil
	}
	skills := map[string]Skills{}
	if err := json.Unmarshal(raw, &skills); err != nil {
		return nil, fmt.Errorf("%w: %s is malformed", ErrUnreadableDocument, fieldSkills)
	}
	return skills, nil
}

func (d document) mastery() (map[string]string, error) {
	raw, ok := d[fieldMastery]
	if !ok {
		return map[string]string{}, nil
	}
	mastery := map[string]string{}
	if err := json.Unmarshal(raw, &mastery); err != nil {
		return nil, fmt.Errorf("%w: %s is malformed", ErrUnreadableDocument, fieldMastery)
	}
	return mastery, nil
}

func (d document) lessons() ([]string, error) {
	raw, ok := d[fieldLessons]
	if !ok {
		return nil, nil
	}
	var lessons []string
	if err := json.Unmarshal(raw, &lessons); err != nil {
		return nil, fmt.Errorf("%w: %s is malformed", ErrUnreadableDocument, fieldLessons)
	}
	return lessons, nil
}

func (d document) attempts() ([]Attempt, error) {
	raw, ok := d[fieldAttempts]
	if !ok {
		return nil, nil
	}
	var attempts []Attempt
	if err := json.Unmarshal(raw, &attempts); err != nil {
		return nil, fmt.Errorf("%w: %s is malformed", ErrUnreadableDocument, fieldAttempts)
	}
	return attempts, nil
}

// set replaces one member, keeping every other key untouched.
func (d document) set(field string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", field, err)
	}
	d[field] = encoded
	return nil
}

// appendAttempt pushes one attempt and trims the window to the newest
// MaxRecentAttempts entries.
func (d document) appendAttempt(attempt Attempt) error {
	attempts, err := d.attempts()
	if err != nil {
		return err
	}

	attempts = append(attempts, attempt)
	if len(attempts) > MaxRecentAttempts {
		attempts = attempts[len(attempts)-MaxRecentAttempts:]
	}
	return d.set(fieldAttempts, attempts)
}

// marshal renders the document back to JSON, always stamping the newest
// schema version: anything the server writes is v2, so a v1 document that
// gets an answer or a completed lesson is upgraded in place.
func (d document) marshal() (json.RawMessage, error) {
	if err := d.set(fieldSchemaVersion, SupportedSchemaVersion); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("encode progress document: %w", err)
	}
	return encoded, nil
}

// sortedUnique keeps completedLessonIds free of duplicates and in a stable
// order, so replaying the same lesson twice is a no-op.
func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, done := seen[value]; done {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}
