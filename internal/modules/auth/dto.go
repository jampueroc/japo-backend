package auth

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
)

// dateLayoutJSON is how a calendar day is rendered to the client: a plain
// YYYY-MM-DD, not a timestamp, because that is what it means.
const dateLayoutJSON = "2006-01-02"

// RegisterRequest is the POST /auth/register payload.
type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email,max=255"`
	Password string `json:"password" validate:"required,min=8,maxbytes=72,password"`
}

// LoginRequest is the POST /auth/login payload.
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email,max=255"`
	Password string `json:"password" validate:"required,maxbytes=72"`
}

// credentials maps the DTO onto the use case input. The conversion is legal
// because only the struct tags differ, and it breaks the build if the two
// ever drift apart.
func (r RegisterRequest) credentials() Credentials { return Credentials(r) }

// credentials maps the DTO onto the use case input.
func (r LoginRequest) credentials() Credentials { return Credentials(r) }

// ProfileResponse is the identity captured during onboarding. It is null in
// the wire until the user has been through it.
type ProfileResponse struct {
	Name   string `json:"name"`
	Gender string `json:"gender"`
	// Birthday is a plain YYYY-MM-DD, or absent when not given.
	Birthday string `json:"birthday,omitempty"`
	// Timezone is the IANA name the streak's day is cut in, absent when
	// the user never sent one (the day is then cut in UTC).
	Timezone string `json:"timezone,omitempty"`
}

// ProfileRequest is the PUT /profile payload.
type ProfileRequest struct {
	Name   string `json:"name" validate:"required,notblank,min=1,max=80"`
	Gender string `json:"gender" validate:"required,oneof=male female neutral"`
	// Birthday is optional. An empty string and a missing field both mean
	// "not given".
	Birthday string `json:"birthday" validate:"omitempty,datetime=2006-01-02"`
	// Timezone is optional and set once at onboarding. It is validated
	// against the real IANA database, not a pattern.
	Timezone string `json:"timezone" validate:"omitempty,max=64"`
}

// profile maps the DTO onto the use case input.
func (r ProfileRequest) profile() (Profile, error) {
	result := Profile{Name: r.Name, Gender: Gender(r.Gender), Timezone: r.Timezone}
	if strings.TrimSpace(r.Birthday) == "" {
		return result, nil
	}

	birthDate, err := time.Parse(dateLayoutJSON, r.Birthday)
	if err != nil {
		return Profile{}, apperror.Validation("invalid_profile",
			"the birthday must be a date in the form YYYY-MM-DD").WithCause(err)
	}
	result.BirthDate = birthDate.UTC()
	return result, nil
}

// UserResponse is the public projection of a user: no password hash ever
// leaves the service. The activity counters travel with it because the web
// client derives unlocks from them.
type UserResponse struct {
	ID                int64     `json:"id"`
	Email             string    `json:"email"`
	EmailVerified     bool      `json:"emailVerified"`
	CreatedAt         time.Time `json:"createdAt"`
	LastActiveDate    string    `json:"lastActiveDate,omitempty"`
	DistinctLoginDays int       `json:"distinctLoginDays"`
	StreakDays        int       `json:"streakDays"`
	// Profile is null until onboarding is done. It lives inside the user
	// rather than beside it so it cannot drift out of sync between the four
	// endpoints that return one.
	Profile *ProfileResponse `json:"profile"`
}

// SessionResponse is returned by register, login and verify-email. When the
// verification gate is on, registering answers with no token and
// PendingVerification set: the session only starts once the code is
// confirmed.
type SessionResponse struct {
	Token     string     `json:"token,omitempty"`
	TokenType string     `json:"tokenType,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	ExpiresIn int64      `json:"expiresIn,omitempty"`
	// PendingVerification tells the client to show the code screen rather
	// than entering the app.
	PendingVerification bool         `json:"pendingVerification"`
	User                UserResponse `json:"user"`
}

// VerifyEmailRequest is the POST /auth/verify-email payload.
type VerifyEmailRequest struct {
	Email string `json:"email" validate:"required,email,max=255"`
	Code  string `json:"code" validate:"required,len=6,number"`
}

// ResendVerificationRequest is the POST /auth/resend-verification payload.
type ResendVerificationRequest struct {
	Email string `json:"email" validate:"required,email,max=255"`
}

// ForgotPasswordRequest is the POST /auth/forgot-password payload.
type ForgotPasswordRequest struct {
	Email string `json:"email" validate:"required,email,max=255"`
}

// ResetPasswordRequest is the POST /auth/reset-password payload.
type ResetPasswordRequest struct {
	Token    string `json:"token" validate:"required,max=128"`
	Password string `json:"password" validate:"required,min=8,maxbytes=72,password"`
}

// NewUserResponse projects a user for the wire. It is exported so the
// composition root can reuse the exact same shape on other endpoints.
func NewUserResponse(user User) UserResponse {
	response := UserResponse{
		ID:                user.ID.Int64(),
		Email:             user.Email.String(),
		EmailVerified:     user.EmailVerified(),
		CreatedAt:         user.CreatedAt,
		DistinctLoginDays: user.Activity.DistinctLoginDays,
		StreakDays:        user.Activity.StreakDays,
	}
	if !user.Activity.LastActiveDate.IsZero() {
		response.LastActiveDate = user.Activity.LastActiveDate.UTC().Format(dateLayoutJSON)
	}
	if user.Profile.Complete() {
		profile := ProfileResponse{
			Name:   user.Profile.Name,
			Gender: string(user.Profile.Gender),
		}
		if !user.Profile.BirthDate.IsZero() {
			profile.Birthday = user.Profile.BirthDate.UTC().Format(dateLayoutJSON)
		}
		profile.Timezone = user.Profile.Timezone
		response.Profile = &profile
	}
	return response
}

// MeResponse is the payload of GET /me. Progress is the client's own
// document, forwarded verbatim, or null when nothing has been saved yet.
type MeResponse struct {
	User     UserResponse    `json:"user"`
	Progress json.RawMessage `json:"progress"`
}

func newMeResponse(me Me) MeResponse {
	return MeResponse{User: NewUserResponse(me.User), Progress: me.Progress}
}

func newSessionResponse(session Session) SessionResponse {
	response := SessionResponse{User: NewUserResponse(session.User)}

	if session.Token == "" {
		// The gate is on and the address is not confirmed yet.
		response.PendingVerification = true
		return response
	}

	expiresAt := session.ExpiresAt
	response.Token = session.Token
	response.TokenType = "Bearer"
	response.ExpiresAt = &expiresAt
	response.ExpiresIn = int64(time.Until(expiresAt).Seconds())
	return response
}
