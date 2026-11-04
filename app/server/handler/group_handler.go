package handler

import (
	"github.com/gofiber/fiber/v2"

	"doexam/business/group"
	modelGroup "doexam/models/group"
	"doexam/pkg/middleware"
)

// GroupHandler serves /api/v1/groups.
type GroupHandler struct {
	groupSvc *group.GroupService
}

// NewGroupHandler returns a concrete GroupHandler.
func NewGroupHandler(groupSvc *group.GroupService) *GroupHandler {
	return &GroupHandler{groupSvc: groupSvc}
}

// List handles GET /api/v1/groups?page&per_page&parent_id&kind&period&q.
func (h *GroupHandler) List(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	f := modelGroup.ListFilter{
		Page: page, PerPage: perPage, ParentID: c.Query("parent_id"),
		Kind: modelGroup.Kind(c.Query("kind")), Period: c.Query("period"), Query: c.Query("q"),
	}
	groups, total, err := h.groupSvc.List(c.UserContext(), principal(c), f)
	if err != nil {
		return handleError(c, logger, "failed to list groups", err)
	}
	return PaginatedResponse(c, groups, total, page, perPage)
}

// Get handles GET /api/v1/groups/:id.
func (h *GroupHandler) Get(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.groupSvc.Get(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get group", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "group retrieved", dto)
}

// Create handles POST /api/v1/groups.
func (h *GroupHandler) Create(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelGroup.CreateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.groupSvc.Create(c.UserContext(), principal(c), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to create group", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "group created", dto)
}

// Update handles PATCH /api/v1/groups/:id.
func (h *GroupHandler) Update(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelGroup.UpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.groupSvc.Update(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update group", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "group updated", dto)
}

// Delete handles DELETE /api/v1/groups/:id.
func (h *GroupHandler) Delete(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	if err := h.groupSvc.Delete(c.UserContext(), principal(c), c.Params("id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to delete group", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "group deleted", nil)
}

// ListMembers handles GET /api/v1/groups/:id/members?page&per_page.
func (h *GroupHandler) ListMembers(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	members, total, err := h.groupSvc.ListMembers(c.UserContext(), principal(c), c.Params("id"),
		modelGroup.MemberFilter{Page: page, PerPage: perPage})
	if err != nil {
		return handleError(c, logger, "failed to list group members", err)
	}
	return PaginatedResponse(c, members, total, page, perPage)
}

// AddMembers handles POST /api/v1/groups/:id/members.
func (h *GroupHandler) AddMembers(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelGroup.AddMembersInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.groupSvc.AddMembers(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to add group members", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "members added", dto)
}

// RemoveMembers handles POST /api/v1/groups/:id/members/remove {user_ids}.
func (h *GroupHandler) RemoveMembers(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelGroup.RemoveMembersInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.groupSvc.RemoveMembers(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to remove group members", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "members removed", dto)
}

// RemoveMember handles DELETE /api/v1/groups/:id/members/:user_id.
func (h *GroupHandler) RemoveMember(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	if err := h.groupSvc.RemoveMember(c.UserContext(), principal(c), c.Params("id"), c.Params("user_id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to remove group member", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "member removed", nil)
}
