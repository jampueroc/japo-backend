package auth

import (
	"errors"
	"log/slog"

	"github.com/gofiber/fiber/v2"

	"github.com/jorgeampuero/japo-backend/internal/platform/middleware"
	"github.com/jorgeampuero/japo-backend/internal/shared/apperror"
	"github.com/jorgeampuero/japo-backend/internal/shared/httpx"
)

// Validator is the narrow view the transport needs of the shared validator.
type Validator interface {
	Struct(payload any) error
}

// Handler adapts HTTP to the auth use cases. It is the only layer of the
// module allowed to speak Fiber.
type Handler struct {
	service   Service
	validator Validator
	logger    *slog.Logger
}

// NewHandler wires the transport layer.
func NewHandler(service Service, validator Validator, logger *slog.Logger) *Handler {
	return &Handler{service: service, validator: validator, logger: logger}
}

// Register handles POST /auth/register and opens a session right away.
func (h *Handler) Register(c *fiber.Ctx) error {
	var req RegisterRequest
	if err := httpx.ParseBody(c, &req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}
	if err := h.validator.Struct(req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	session, err := h.service.Register(c.UserContext(), req.credentials())
	if err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return httpx.Created(c, newSessionResponse(session))
}

// Login handles POST /auth/login.
func (h *Handler) Login(c *fiber.Ctx) error {
	var req LoginRequest
	if err := httpx.ParseBody(c, &req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}
	if err := h.validator.Struct(req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	session, err := h.service.Login(c.UserContext(), req.credentials())
	if err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return httpx.OK(c, newSessionResponse(session))
}

// VerifyEmail handles POST /auth/verify-email. On success it opens the
// session, which is what makes the gate work: with verification required, the
// token is issued here and nowhere else during signup.
func (h *Handler) VerifyEmail(c *fiber.Ctx) error {
	var req VerifyEmailRequest
	if err := httpx.ParseBody(c, &req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}
	if err := h.validator.Struct(req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	session, err := h.service.VerifyEmail(c.UserContext(), req.Email, req.Code)
	if err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return httpx.OK(c, newSessionResponse(session))
}

// ResendVerification handles POST /auth/resend-verification. It answers 204
// whatever happened, so it cannot be used to find out which addresses are
// registered.
func (h *Handler) ResendVerification(c *fiber.Ctx) error {
	var req ResendVerificationRequest
	if err := httpx.ParseBody(c, &req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}
	if err := h.validator.Struct(req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	if err := h.service.ResendVerification(c.UserContext(), req.Email); err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return httpx.NoContent(c)
}

// ForgotPassword handles POST /auth/forgot-password. Same reasoning as above:
// always 204.
func (h *Handler) ForgotPassword(c *fiber.Ctx) error {
	var req ForgotPasswordRequest
	if err := httpx.ParseBody(c, &req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}
	if err := h.validator.Struct(req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	if err := h.service.ForgotPassword(c.UserContext(), req.Email); err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return httpx.NoContent(c)
}

// ResetPassword handles POST /auth/reset-password.
func (h *Handler) ResetPassword(c *fiber.Ctx) error {
	var req ResetPasswordRequest
	if err := httpx.ParseBody(c, &req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}
	if err := h.validator.Struct(req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	if err := h.service.ResetPassword(c.UserContext(), req.Token, req.Password); err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return httpx.NoContent(c)
}

// Me handles GET /me for the authenticated user.
func (h *Handler) Me(c *fiber.Ctx) error {
	userID, err := middleware.UserID(c)
	if err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	me, err := h.service.Me(c.UserContext(), userID)
	if err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return httpx.OK(c, newMeResponse(me))
}

// Logout handles POST /auth/logout. Access tokens are stateless and there is
// no denylist, so this cannot revoke anything: the real logout is the client
// dropping the token. The endpoint exists so the client has a symmetric API
// and a place to hook future revocation.
func (h *Handler) Logout(c *fiber.Ctx) error {
	return httpx.NoContent(c)
}

// toHTTPError maps the domain sentinels to client facing errors. Anything
// unmapped stays as it is and is rendered as an opaque 500.
func toHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrEmailAlreadyExists):
		return apperror.Conflict("email_already_registered", "that email is already registered").WithCause(err)
	case errors.Is(err, ErrInvalidCredentials):
		return apperror.Unauthorized("invalid_credentials", "the email or password is incorrect").WithCause(err)
	case errors.Is(err, ErrUserNotFound):
		return apperror.NotFound("user_not_found", "the user does not exist").WithCause(err)
	case errors.Is(err, ErrInvalidVerificationCode):
		return apperror.Validation("invalid_verification_code",
			"the verification code is not valid or has expired").WithCause(err)
	case errors.Is(err, ErrEmailAlreadyVerified):
		return apperror.Conflict("email_already_verified",
			"this address is already verified, please sign in").WithCause(err)
	case errors.Is(err, ErrEmailNotVerified):
		return apperror.Forbidden("email_not_verified",
			"confirm your email address before signing in").WithCause(err)
	case errors.Is(err, ErrInvalidResetToken):
		return apperror.Validation("invalid_reset_token",
			"this reset link is not valid or has expired").WithCause(err)
	default:
		return err
	}
}
