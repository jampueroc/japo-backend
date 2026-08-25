package progress

import "github.com/gofiber/fiber/v2"

// RegisterRoutes mounts the progress endpoints. The guards (JWT auth) are
// injected by the composition root so the module does not depend on a
// particular authentication scheme.
func RegisterRoutes(router fiber.Router, h *Handler, guards ...fiber.Handler) {
	group := router.Group("/progress", guards...)
	group.Get("/", h.Get)
	group.Put("/", h.Save)
	// Server side grading: these recalculate the document instead of
	// replacing it, so they are safe against concurrent writers.
	group.Post("/answer", h.Answer)
	group.Post("/lesson-complete", h.CompleteLesson)
}
