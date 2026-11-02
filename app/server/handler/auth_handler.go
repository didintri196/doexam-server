package handler

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"doexam/business/auth"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	"doexam/pkg/middleware"
)

const principalLocalsKey = "principal"

// AuthHandler serves /api/v1/auth/* and provides the RequireAuth middleware.
type AuthHandler struct {
	authSvc *auth.AuthService
}

// NewAuthHandler returns a concrete AuthHandler.
func NewAuthHandler(authSvc *auth.AuthService) *AuthHandler {
	return &AuthHandler{authSvc: authSvc}
}

// RequireAuth validates "Authorization: Bearer <token>" and stores the Principal
// in Fiber locals and in the request context (modelAuth.PrincipalFrom).
func (h *AuthHandler) RequireAuth(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	scheme, token, found := strings.Cut(c.Get(fiber.HeaderAuthorization), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return ErrorResponse(c, fiber.StatusUnauthorized, "unauthorized", nil)
	}

	p, err := h.authSvc.Authenticate(c.UserContext(), strings.TrimSpace(token))
	if err != nil {
		return handleError(c, logger, "failed to authenticate", err)
	}
	c.Locals(principalLocalsKey, p)
	c.SetUserContext(modelAuth.WithPrincipal(c.UserContext(), p))
	return c.Next()
}

// Login handles POST /api/v1/auth/login.
func (h *AuthHandler) Login(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelAuth.LoginInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.authSvc.Login(c.UserContext(), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to login", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "logged in", dto)
}

// Me handles GET /api/v1/auth/me.
func (h *AuthHandler) Me(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.authSvc.Me(c.UserContext(), principal(c))
	if err != nil {
		return handleError(c, logger, "failed to get current user", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "current user retrieved", dto)
}

// Logout handles POST /api/v1/auth/logout.
func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	if err := h.authSvc.Logout(c.UserContext(), principal(c), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to logout", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "logged out", nil)
}

// ChangePassword handles POST /api/v1/auth/change-password.
func (h *AuthHandler) ChangePassword(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelAuth.ChangePasswordInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	if err := h.authSvc.ChangePassword(c.UserContext(), principal(c), in, clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to change password", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "password changed", nil)
}

// principal returns the Principal set by RequireAuth. Only call it on routes behind RequireAuth.
func principal(c *fiber.Ctx) modelAuth.Principal {
	p, _ := c.Locals(principalLocalsKey).(modelAuth.Principal)
	return p
}

func clientInfo(c *fiber.Ctx) modelAudit.Client {
	return modelAudit.Client{IP: c.IP(), UserAgent: c.Get(fiber.HeaderUserAgent)}
}
