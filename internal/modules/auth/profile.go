package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/valueobject"
)

// SaveProfile stores the identity captured during onboarding. It is
// idempotent: sending the same values twice changes nothing.
//
// It deliberately does NOT count as activity, for the same reason the
// progress patch does not: filling in a form is not practising, and letting
// it extend a streak would make the streak mean something else.
func (s *service) SaveProfile(ctx context.Context, userID int64, profile Profile) (User, error) {
	id, err := valueobject.NewID(userID)
	if err != nil {
		return User{}, err
	}

	normalised, err := normaliseProfile(profile, s.clock())
	if err != nil {
		return User{}, err
	}

	saved, err := s.repo.SaveProfile(ctx, id, normalised)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("save profile: %w", err)
	}

	s.logger.InfoContext(ctx, "profile saved",
		slog.Int64("user_id", saved.ID.Int64()),
		slog.Bool("has_birth_date", !saved.Profile.BirthDate.IsZero()),
	)
	return saved, nil
}

// normaliseProfile trims and validates the onboarding payload. The service
// checks it independently of the DTO rules so the use case is safe whoever
// calls it.
func normaliseProfile(profile Profile, now time.Time) (Profile, error) {
	name := strings.TrimSpace(profile.Name)

	switch {
	case name == "":
		return Profile{}, invalidProfile("the name is required")
	case len([]rune(name)) > MaxProfileNameLength:
		return Profile{}, invalidProfile(
			fmt.Sprintf("the name must be at most %d characters long", MaxProfileNameLength))
	case hasControlCharacters(name):
		// A line break in a display name ends up in emails and headings.
		return Profile{}, invalidProfile("the name must not contain control characters")
	}

	if !profile.Gender.Valid() {
		return Profile{}, invalidProfile("that is not a gender this server stores")
	}

	timezone, err := normaliseTimezone(profile.Timezone)
	if err != nil {
		return Profile{}, err
	}

	normalised := Profile{Name: name, Gender: profile.Gender, Timezone: timezone}
	if profile.BirthDate.IsZero() {
		return normalised, nil
	}

	birthDate := UTCDay(profile.BirthDate)
	switch {
	case birthDate.After(UTCDay(now)):
		return Profile{}, invalidProfile("the birthday cannot be in the future")
	case birthDate.Year() < MinBirthYear:
		return Profile{}, invalidProfile(
			fmt.Sprintf("the birthday must be after %d", MinBirthYear))
	}

	normalised.BirthDate = birthDate
	return normalised, nil
}

// normaliseTimezone checks the IANA name against the actual database rather
// than a regular expression: the only thing that matters is whether this
// binary can resolve it later, and that is exactly what LoadLocation answers.
func normaliseTimezone(timezone string) (string, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return "", nil
	}

	if len(timezone) > MaxTimezoneLength {
		return "", invalidProfile("the timezone name is too long")
	}
	// "Local" resolves to whatever zone the SERVER happens to be in, which
	// would silently make one account's streak depend on where the box is.
	if strings.EqualFold(timezone, "Local") {
		return "", invalidProfile("the timezone must be a specific IANA zone, such as America/Lima")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", invalidProfile("that is not a timezone this server knows")
	}

	return timezone, nil
}

// invalidProfile builds a client facing rejection. The messages are written
// to be shown as they are, which is why they are built here rather than
// wrapped around a sentinel and unwrapped at the edge.
func invalidProfile(message string) error {
	return apperror.Validation("invalid_profile", message)
}

// hasControlCharacters reports whether s carries anything that is not
// printable text, line breaks included.
func hasControlCharacters(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
