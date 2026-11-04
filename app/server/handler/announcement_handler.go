package handler

import (
	"github.com/gofiber/fiber/v2"

	modelSession "doexam/models/session"
	"doexam/pkg/middleware"
)

// Announcements live in the session domain; these SessionHandler methods serve them
// (DOCS/PLAN/12-PENGUMUMAN-PESERTA.md).

// Announce handles POST /api/v1/sessions/:id/announcements.
func (h *SessionHandler) Announce(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSession.AnnouncementInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.sessionSvc.Announce(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to send announcement", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "announcement sent", dto)
}

// Broadcast handles POST /api/v1/announcements (several sessions at once).
func (h *SessionHandler) Broadcast(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSession.BroadcastInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.sessionSvc.Broadcast(c.UserContext(), principal(c), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to broadcast announcement", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "announcement sent", dto)
}

// ListAnnouncements handles GET /api/v1/sessions/:id/announcements.
func (h *SessionHandler) ListAnnouncements(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.sessionSvc.ListAnnouncements(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to list announcements", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "announcements retrieved", dto)
}

// AnnouncementRecipients handles GET /api/v1/sessions/:id/announcements/:aid/recipients.
func (h *SessionHandler) AnnouncementRecipients(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	out, err := h.sessionSvc.AnnouncementRecipients(c.UserContext(), principal(c), c.Params("id"), c.Params("aid"))
	if err != nil {
		return handleError(c, logger, "failed to list announcement recipients", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "recipients retrieved", out)
}

// RetractAnnouncement handles POST /api/v1/sessions/:id/announcements/:aid/retract.
func (h *SessionHandler) RetractAnnouncement(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.sessionSvc.RetractAnnouncement(c.UserContext(), principal(c), c.Params("id"), c.Params("aid"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to retract announcement", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "announcement retracted", nil)
}

// MyAnnouncements handles GET /api/v1/my/announcements?session_id= (participant view).
func (h *SessionHandler) MyAnnouncements(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.sessionSvc.MyAnnouncements(c.UserContext(), principal(c), c.Query("session_id"))
	if err != nil {
		return handleError(c, logger, "failed to list my announcements", err)
	}
	// announcements change while the page is open: never serve a cached copy
	c.Set(fiber.HeaderCacheControl, "no-store")
	return SuccessResponse(c, fiber.StatusOK, "announcements retrieved", dto)
}

// ReadAnnouncement handles POST /api/v1/my/announcements/:aid/read.
func (h *SessionHandler) ReadAnnouncement(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.sessionSvc.ReadAnnouncement(c.UserContext(), principal(c), c.Params("aid")); err != nil {
		return handleError(c, logger, "failed to mark announcement read", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "announcement read", nil)
}
