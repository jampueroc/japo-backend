package auth

import "github.com/gofiber/fiber/v2"

// RouteGuards are the middlewares the composition root injects. The module
// declares what it needs protected or throttled; it does not know how either
// is implemented.
type RouteGuards struct {
	// Authenticated protects the routes that require an access token.
	Authenticated []fiber.Handler
	// Login throttles the password check, which is expensive on purpose.
	Login []fiber.Handler
	// Register throttles account creation.
	Register []fiber.Handler
	// Email throttles every endpoint that makes the server send a message,
	// keyed by destination as well as by IP.
	Email []fiber.Handler
	// Verify throttles redeeming a code or a reset link.
	Verify []fiber.Handler
}

// RegisterRoutes mounts the authentication endpoints.
func RegisterRoutes(router fiber.Router, h *Handler, guards RouteGuards) {
	group := router.Group("/auth")
	group.Post("/register", chain(guards.Register, h.Register)...)
	group.Post("/login", chain(guards.Login, h.Login)...)
	// Public on purpose: logging out with an already expired token must not
	// answer 401, it is a no-op either way.
	group.Post("/logout", h.Logout)

	// Email verification and password recovery. The send side is throttled
	// by destination so nobody's inbox can be used as a weapon; the redeem
	// side is throttled because both are guessable secrets.
	group.Post("/verify-email", chain(guards.Verify, h.VerifyEmail)...)
	group.Post("/resend-verification", chain(guards.Email, h.ResendVerification)...)
	group.Post("/forgot-password", chain(guards.Email, h.ForgotPassword)...)
	group.Post("/reset-password", chain(guards.Verify, h.ResetPassword)...)

	// The caller's own identity, behind the token guard.
	me := router.Group("/me", guards.Authenticated...)
	me.Get("/", h.Me)
}

// chain puts the middlewares in front of the handler without aliasing the
// caller's slice.
func chain(middlewares []fiber.Handler, final fiber.Handler) []fiber.Handler {
	handlers := make([]fiber.Handler, 0, len(middlewares)+1)
	handlers = append(handlers, middlewares...)
	return append(handlers, final)
}
