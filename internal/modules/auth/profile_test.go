package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/modules/auth"
	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
)

func TestServiceSaveProfile(t *testing.T) {
	t.Parallel()

	valid := auth.Profile{
		Name:      "Jorge",
		Gender:    auth.GenderMale,
		BirthDate: time.Date(1990, 3, 14, 0, 0, 0, 0, time.UTC),
	}

	t.Run("stores the identity and reports it complete", func(t *testing.T) {
		t.Parallel()

		user := verifiedUser()
		h := newHarness(t, harnessOptions{user: &user})

		saved, err := h.service.SaveProfile(context.Background(), 42, valid)
		if err != nil {
			t.Fatalf("save profile: %v", err)
		}
		if !saved.Profile.Complete() {
			t.Fatal("the profile came back incomplete")
		}
		if saved.Profile != valid {
			t.Fatalf("got profile %+v, want %+v", saved.Profile, valid)
		}
	})

	t.Run("a new account has no profile", func(t *testing.T) {
		t.Parallel()

		if unverifiedUser().Profile.Complete() {
			t.Fatal("a fresh account reports a complete profile")
		}
	})

	t.Run("trims the name and keeps the rest", func(t *testing.T) {
		t.Parallel()

		user := verifiedUser()
		h := newHarness(t, harnessOptions{user: &user})

		saved, err := h.service.SaveProfile(context.Background(), 42, auth.Profile{
			Name: "  Jorge  ", Gender: auth.GenderNeutral,
		})
		if err != nil {
			t.Fatalf("save profile: %v", err)
		}
		if saved.Profile.Name != "Jorge" {
			t.Fatalf("got name %q, want it trimmed", saved.Profile.Name)
		}
		// A birthday is optional and must stay absent, not become an epoch.
		if !saved.Profile.BirthDate.IsZero() {
			t.Fatalf("got birth date %v, want the zero value", saved.Profile.BirthDate)
		}
	})

	t.Run("keeps unicode names intact", func(t *testing.T) {
		t.Parallel()

		// The app is about Japanese: rejecting non-ASCII names would be a
		// spectacular own goal.
		for _, name := range []string{"ホルヘ", "Jörg", "海斗", "Éowyn"} {
			user := verifiedUser()
			h := newHarness(t, harnessOptions{user: &user})

			saved, err := h.service.SaveProfile(context.Background(), 42,
				auth.Profile{Name: name, Gender: auth.GenderNeutral})
			if err != nil {
				t.Fatalf("rejected %q: %v", name, err)
			}
			if saved.Profile.Name != name {
				t.Fatalf("got name %q, want %q", saved.Profile.Name, name)
			}
		}
	})

	t.Run("does not count as activity", func(t *testing.T) {
		t.Parallel()

		// Filling in a form is not practising. If it moved the streak, the
		// streak would stop meaning what it says.
		user := verifiedUser()
		h := newHarness(t, harnessOptions{user: &user})

		if _, err := h.service.SaveProfile(context.Background(), 42, valid); err != nil {
			t.Fatalf("save profile: %v", err)
		}
		if h.repo.touchCalls != 0 {
			t.Fatalf("TouchActivity called %d times, want 0", h.repo.touchCalls)
		}
	})

	tests := []struct {
		name    string
		profile auth.Profile
	}{
		{name: "no name", profile: auth.Profile{Gender: auth.GenderMale}},
		{name: "a blank name", profile: auth.Profile{Name: "   ", Gender: auth.GenderMale}},
		{
			name:    "a name past the column width",
			profile: auth.Profile{Name: strings.Repeat("x", auth.MaxProfileNameLength+1), Gender: auth.GenderMale},
		},
		{
			// It would end up in an email header or a heading.
			name:    "a name with a line break",
			profile: auth.Profile{Name: "Jorge\nBcc: someone@example.com", Gender: auth.GenderMale},
		},
		{name: "no gender", profile: auth.Profile{Name: "Jorge"}},
		{name: "a gender this server does not store", profile: auth.Profile{Name: "Jorge", Gender: "hombre"}},
		{
			name: "a birthday in the future",
			profile: auth.Profile{Name: "Jorge", Gender: auth.GenderMale,
				BirthDate: fixedNow.AddDate(0, 0, 1)},
		},
		{
			name: "a birthday that is obviously a typo",
			profile: auth.Profile{Name: "Jorge", Gender: auth.GenderMale,
				BirthDate: time.Date(1799, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
	}

	for _, tc := range tests {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			t.Parallel()

			user := verifiedUser()
			h := newHarness(t, harnessOptions{user: &user})

			_, err := h.service.SaveProfile(context.Background(), 42, tc.profile)
			if kind := apperror.KindOf(err); err == nil || kind != apperror.KindValidation {
				t.Fatalf("got %v (kind %v), want a validation error", err, kind)
			}
			if len(h.repo.savedProfiles) != 0 {
				t.Fatalf("a rejected profile was written: %+v", h.repo.savedProfiles)
			}
		})
	}

	t.Run("reports a missing account", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, harnessOptions{})
		h.repo.saveProfileErr = auth.ErrUserNotFound

		if _, err := h.service.SaveProfile(context.Background(), 42, valid); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("got error %v, want %v", err, auth.ErrUserNotFound)
		}
	})
}

// Today's birthday is the edge the "not in the future" check must not eat.
func TestServiceSaveProfileAcceptsTodayAsBirthday(t *testing.T) {
	t.Parallel()

	user := verifiedUser()
	h := newHarness(t, harnessOptions{user: &user})

	saved, err := h.service.SaveProfile(context.Background(), 42, auth.Profile{
		Name: "Recién nacido", Gender: auth.GenderNeutral, BirthDate: fixedNow,
	})
	if err != nil {
		t.Fatalf("today should be a valid birthday: %v", err)
	}
	if !saved.Profile.BirthDate.Equal(auth.UTCDay(fixedNow)) {
		t.Fatalf("got %v, want today truncated to a day", saved.Profile.BirthDate)
	}
}
