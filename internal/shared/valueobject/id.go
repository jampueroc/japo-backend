package valueobject

import (
	"strconv"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
)

// ID is a positive database identifier.
type ID int64

// NewID validates that v is a usable identifier.
func NewID(v int64) (ID, error) {
	if v <= 0 {
		return 0, apperror.Validation("invalid_id", "identifier must be positive")
	}
	return ID(v), nil
}

// ParseID validates and converts the string representation of an identifier.
func ParseID(raw string) (ID, error) {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, apperror.Validation("invalid_id", "identifier must be a number").WithCause(err)
	}
	return NewID(v)
}

// Int64 returns the underlying value, ready for the database layer.
func (id ID) Int64() int64 { return int64(id) }

// String renders the identifier in base 10.
func (id ID) String() string { return strconv.FormatInt(int64(id), 10) }

// IsZero reports whether the identifier is unset.
func (id ID) IsZero() bool { return id <= 0 }
