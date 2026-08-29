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

// --- timezone --------------------------------------------------------------

// The whole point of storing a zone: the calendar day has to turn over at the
// user's midnight, not at the server's.
func TestDayInCutsAtTheUsersMidnight(t *testing.T) {
	t.Parallel()

	lima, err := time.LoadLocation("America/Lima") // UTC-5, no DST
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	// 21:30 on the 25th in Lima is already 02:30 on the 26th in UTC. This
	// is exactly the case that was inflating streaks: an evening session
	// counted as the next day.
	evening := time.Date(2026, 8, 26, 2, 30, 0, 0, time.UTC)

	if got := auth.UTCDay(evening); got.Day() != 26 {
		t.Fatalf("got UTC day %v, want the 26th", got)
	}
	if got := auth.DayIn(evening, lima); got.Day() != 25 {
		t.Fatalf("got Lima day %v, want the 25th: an evening session must not count as tomorrow", got)
	}
}

func TestProfileLocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		timezone string
		want     string
	}{
		{name: "no zone falls back to UTC", timezone: "", want: "UTC"},
		{name: "a real zone", timezone: "Europe/Madrid", want: "Europe/Madrid"},
		// A zone dropped by a tzdata update must not stop someone
		// practising; it degrades to UTC instead of failing.
		{name: "an unknown zone degrades to UTC", timezone: "Mars/Olympus_Mons", want: "UTC"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			profile := auth.Profile{Timezone: tc.timezone}
			if got := profile.Location().String(); got != tc.want {
				t.Fatalf("got location %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServiceSaveProfileTimezone(t *testing.T) {
	t.Parallel()

	t.Run("stores a real IANA zone", func(t *testing.T) {
		t.Parallel()

		user := verifiedUser()
		h := newHarness(t, harnessOptions{user: &user})

		saved, err := h.service.SaveProfile(context.Background(), 42, auth.Profile{
			Name: "Jorge", Gender: auth.GenderMale, Timezone: " America/Lima ",
		})
		if err != nil {
			t.Fatalf("save profile: %v", err)
		}
		if saved.Profile.Timezone != "America/Lima" {
			t.Fatalf("got timezone %q, want it trimmed and stored", saved.Profile.Timezone)
		}
	})

	for _, tc := range []struct {
		name     string
		timezone string
	}{
		{name: "an invented zone", timezone: "Mars/Olympus_Mons"},
		{name: "an abbreviation rather than an IANA name", timezone: "PST"},
		// "Local" resolves to wherever the server happens to be, which
		// would tie one account's streak to the box's own clock.
		{name: "Local", timezone: "Local"},
		{name: "one that is too long", timezone: strings.Repeat("a", auth.MaxTimezoneLength+1)},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			t.Parallel()

			user := verifiedUser()
			h := newHarness(t, harnessOptions{user: &user})

			_, err := h.service.SaveProfile(context.Background(), 42, auth.Profile{
				Name: "Jorge", Gender: auth.GenderMale, Timezone: tc.timezone,
			})
			if kind := apperror.KindOf(err); err == nil || kind != apperror.KindValidation {
				t.Fatalf("got %v (kind %v), want a validation error", err, kind)
			}
		})
	}
}

// Login has to record the day in the account's zone, not the server's.
func TestServiceLoginRecordsTheDayInTheUsersZone(t *testing.T) {
	t.Parallel()

	const password = "supersecret1"

	// fixedNow is 23:30 UTC on the 21st, which in Tokyo is already the 22nd.
	user := unverifiedUser()
	user.EmailVerifiedAt = fixedNow
	user.PasswordHash = "hashed:" + password
	user.Profile = auth.Profile{Name: "Jorge", Gender: auth.GenderMale, Timezone: "Asia/Tokyo"}

	h := newHarness(t, harnessOptions{user: &user})

	if _, err := h.service.Login(context.Background(),
		auth.Credentials{Email: testEmail, Password: password}); err != nil {
		t.Fatalf("login: %v", err)
	}

	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	want := auth.DayIn(fixedNow, tokyo)
	if !h.repo.lastTouchDay.Equal(want) {
		t.Fatalf("recorded day %v, want %v (the account's zone, not UTC %v)",
			h.repo.lastTouchDay, want, auth.UTCDay(fixedNow))
	}
}
