package progress

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

// Handler adapts HTTP to the progress use cases.
type Handler struct {
	service   Service
	validator Validator
	logger    *slog.Logger
}

// NewHandler wires the transport layer. The validator only applies to the
// grading endpoints: GET and PUT exchange an opaque document, and the service
// is what checks that it is valid, non empty JSON within the size budget.
func NewHandler(service Service, validator Validator, logger *slog.Logger) *Handler {
	return &Handler{service: service, validator: validator, logger: logger}
}

// Get handles GET /progress for the authenticated user.
func (h *Handler) Get(c *fiber.Ctx) error {
	userID, err := middleware.UserID(c)
	if err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	found, err := h.service.Get(c.UserContext(), userID)
	if err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return c.Status(fiber.StatusOK).Type("json").Send(found.Data)
}

// Save handles PUT /progress: a full document upsert for the caller.
func (h *Handler) Save(c *fiber.Ctx) error {
	userID, err := middleware.UserID(c)
	if err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	saved, err := h.service.Save(c.UserContext(), userID, documentFromBody(c.Body()))
	if err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return c.Status(fiber.StatusOK).Type("json").Send(saved.Data)
}

// Answer handles POST /progress/answer: one graded attempt, scored server
// side.
func (h *Handler) Answer(c *fiber.Ctx) error {
	userID, err := middleware.UserID(c)
	if err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	var req AnswerRequest
	if err := httpx.ParseBody(c, &req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}
	if err := h.validator.Struct(req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	saved, err := h.service.RecordAnswer(c.UserContext(), userID, req.answer())
	if err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return c.Status(fiber.StatusOK).Type("json").Send(saved.Data)
}

// CompleteLesson handles POST /progress/lesson-complete.
func (h *Handler) CompleteLesson(c *fiber.Ctx) error {
	userID, err := middleware.UserID(c)
	if err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	var req LessonCompleteRequest
	if err := httpx.ParseBody(c, &req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}
	if err := h.validator.Struct(req); err != nil {
		return httpx.Fail(c, h.logger, err)
	}

	saved, err := h.service.CompleteLesson(c.UserContext(), userID, req.LessonID)
	if err != nil {
		return httpx.Fail(c, h.logger, toHTTPError(err))
	}

	return c.Status(fiber.StatusOK).Type("json").Send(saved.Data)
}

// toHTTPError maps the domain sentinels to client facing errors.
func toHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrProgressNotFound):
		return apperror.NotFound("progress_not_found", "no progress has been saved yet").WithCause(err)
	case errors.Is(err, ErrUnknownOwner):
		return apperror.Unauthorized("invalid_token", "the account in the access token no longer exists").WithCause(err)
	case errors.Is(err, ErrInvalidAnswer):
		return apperror.Validation("invalid_answer", "the kana or the skill is not one this server can score").WithCause(err)
	case errors.Is(err, ErrInvalidLesson):
		return apperror.Validation("invalid_lesson", "the lesson identifier is not valid").WithCause(err)
	case errors.Is(err, ErrUnsupportedSchema):
		return apperror.Conflict("unsupported_schema_version",
			"the stored document uses a schema version this server does not support").WithCause(err)
	case errors.Is(err, ErrUnreadableDocument):
		return apperror.Conflict("unreadable_document",
			"the stored document cannot be read; replace it with PUT /progress").WithCause(err)
	default:
		return err
	}
}
