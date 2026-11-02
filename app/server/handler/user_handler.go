package handler

import (
	"github.com/gofiber/fiber/v2"

	"doexam/business/user"
	modelAuth "doexam/models/auth"
	modelUser "doexam/models/user"
	"doexam/pkg/middleware"
)

// UserHandler serves /api/v1/users and /api/v1/roles.
type UserHandler struct {
	userSvc *user.UserService
}

// NewUserHandler returns a concrete UserHandler.
func NewUserHandler(userSvc *user.UserService) *UserHandler {
	return &UserHandler{userSvc: userSvc}
}

// ListRoles handles GET /api/v1/roles.
func (h *UserHandler) ListRoles(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	roles, err := h.userSvc.ListRoles(c.UserContext(), principal(c))
	if err != nil {
		return handleError(c, logger, "failed to list roles", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "roles retrieved", roles)
}

// List handles GET /api/v1/users?page&per_page&q&role&group_id&status.
func (h *UserHandler) List(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	f := modelUser.ListFilter{
		Page: page, PerPage: perPage, Query: c.Query("q"), RoleCode: c.Query("role"),
		GroupID: c.Query("group_id"), Status: modelAuth.UserStatus(c.Query("status")),
	}
	users, total, err := h.userSvc.List(c.UserContext(), principal(c), f)
	if err != nil {
		return handleError(c, logger, "failed to list users", err)
	}
	return PaginatedResponse(c, users, total, page, perPage)
}

// Get handles GET /api/v1/users/:id.
func (h *UserHandler) Get(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.userSvc.Get(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get user", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "user retrieved", dto)
}

// Create handles POST /api/v1/users.
func (h *UserHandler) Create(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelUser.CreateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.userSvc.Create(c.UserContext(), principal(c), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to create user", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "user created", dto)
}

// Update handles PATCH /api/v1/users/:id.
func (h *UserHandler) Update(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelUser.UpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.userSvc.Update(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update user", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "user updated", dto)
}

// Delete handles DELETE /api/v1/users/:id.
func (h *UserHandler) Delete(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	if err := h.userSvc.Delete(c.UserContext(), principal(c), c.Params("id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to delete user", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "user deleted", nil)
}

// ResetPassword handles POST /api/v1/users/:id/reset-password.
func (h *UserHandler) ResetPassword(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.userSvc.ResetPassword(c.UserContext(), principal(c), c.Params("id"), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to reset password", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "password reset", dto)
}
