package handler

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"doexam/business/session"
	modelSession "doexam/models/session"
	"doexam/pkg/errr"
	"doexam/pkg/middleware"
)

// SessionHandler serves /api/v1/sessions and /api/v1/my/sessions.
type SessionHandler struct {
	sessionSvc *session.SessionService
}

// NewSessionHandler returns a concrete SessionHandler.
func NewSessionHandler(sessionSvc *session.SessionService) *SessionHandler {
	return &SessionHandler{sessionSvc: sessionSvc}
}

// List handles GET /api/v1/sessions?exam_id&status&from&to&q&page&per_page (from/to: RFC 3339).
func (h *SessionHandler) List(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	f := modelSession.ListFilter{Page: page, PerPage: perPage, ExamID: c.Query("exam_id"),
		Status: modelSession.Status(c.Query("status")), Query: strings.TrimSpace(c.Query("q"))}
	v := errr.Validation{}
	f.From, f.To = queryTime(c, "from", v), queryTime(c, "to", v)
	if err := v.Err(); err != nil {
		return handleError(c, logger, "invalid filter", err)
	}
	out, total, err := h.sessionSvc.List(c.UserContext(), principal(c), f)
	if err != nil {
		return handleError(c, logger, "failed to list sessions", err)
	}
	return PaginatedResponse(c, out, total, page, perPage)
}

// Get handles GET /api/v1/sessions/:id.
func (h *SessionHandler) Get(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.sessionSvc.Get(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get session", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "session retrieved", dto)
}

// Create handles POST /api/v1/sessions.
func (h *SessionHandler) Create(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSession.SessionInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.sessionSvc.Create(c.UserContext(), principal(c), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to create session", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "session created", dto)
}

// Update handles PATCH /api/v1/sessions/:id.
func (h *SessionHandler) Update(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSession.SessionInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.sessionSvc.Update(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update session", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "session updated", dto)
}

// Delete handles DELETE /api/v1/sessions/:id.
func (h *SessionHandler) Delete(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.sessionSvc.Delete(c.UserContext(), principal(c), c.Params("id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to delete session", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "session deleted", nil)
}

// SetStatus handles POST /api/v1/sessions/:id/status {status}.
func (h *SessionHandler) SetStatus(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSession.StatusInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.sessionSvc.SetStatus(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to change session status", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "session status changed", dto)
}

// Token handles GET /api/v1/sessions/:id/token.
func (h *SessionHandler) Token(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.sessionSvc.Token(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get session token", err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return SuccessResponse(c, fiber.StatusOK, "token retrieved", dto)
}

// RotateToken handles POST /api/v1/sessions/:id/token/rotate.
func (h *SessionHandler) RotateToken(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.sessionSvc.RotateToken(c.UserContext(), principal(c), c.Params("id"), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to rotate session token", err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return SuccessResponse(c, fiber.StatusOK, "token rotated", dto)
}

// SetProctors handles PUT /api/v1/sessions/:id/proctors {user_ids}.
func (h *SessionHandler) SetProctors(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSession.ProctorsInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.sessionSvc.SetProctors(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to set proctors", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "proctors saved", dto)
}

// ListParticipants handles GET /api/v1/sessions/:id/participants?q&attendance&page&per_page.
func (h *SessionHandler) ListParticipants(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	f := modelSession.ParticipantFilter{Page: page, PerPage: perPage, Query: c.Query("q"), Attendance: modelSession.Attendance(c.Query("attendance"))}
	out, total, err := h.sessionSvc.ListParticipants(c.UserContext(), principal(c), c.Params("id"), f)
	if err != nil {
		return handleError(c, logger, "failed to list participants", err)
	}
	return PaginatedResponse(c, out, total, page, perPage)
}

// AddParticipants handles POST /api/v1/sessions/:id/participants {user_ids, group_ids, extra_time_minutes}.
func (h *SessionHandler) AddParticipants(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSession.AddParticipantsInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.sessionSvc.AddParticipants(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to add participants", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "participants added", dto)
}

// UpdateParticipant handles PATCH /api/v1/sessions/:id/participants/:user_id.
func (h *SessionHandler) UpdateParticipant(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSession.ParticipantUpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.sessionSvc.UpdateParticipant(c.UserContext(), principal(c), c.Params("id"), c.Params("user_id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update participant", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "participant updated", dto)
}

// RemoveParticipant handles DELETE /api/v1/sessions/:id/participants/:user_id.
func (h *SessionHandler) RemoveParticipant(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.sessionSvc.RemoveParticipant(c.UserContext(), principal(c), c.Params("id"), c.Params("user_id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to remove participant", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "participant removed", nil)
}

// MySessions handles GET /api/v1/my/sessions (participant view).
func (h *SessionHandler) MySessions(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	out, err := h.sessionSvc.MySessions(c.UserContext(), principal(c))
	if err != nil {
		return handleError(c, logger, "failed to list my sessions", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "sessions retrieved", out)
}

// queryTime parses an optional RFC 3339 query parameter.
func queryTime(c *fiber.Ctx, key string, v errr.Validation) *time.Time {
	raw := c.Query(key)
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	v.Check(err == nil, key, "must be RFC 3339, e.g. 2026-11-02T07:00:00+07:00")
	if err != nil {
		return nil
	}
	return &t
}

// Print handles GET /api/v1/sessions/:id/print (data for cards, attendance list, minutes, proctor sheet).
func (h *SessionHandler) Print(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.sessionSvc.Print(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get session print data", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "session print data", dto)
}

// CardPasswords handles POST /api/v1/sessions/:id/card-passwords {only_new?}: new system
// passwords for the participant cards, shown once (never cached).
func (h *SessionHandler) CardPasswords(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelSession.CardPasswordsInput
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&in); err != nil {
			return badBody(c)
		}
	}
	dto, err := h.sessionSvc.GenerateCardPasswords(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to generate card passwords", err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return SuccessResponse(c, fiber.StatusOK, "card passwords generated", dto)
}
