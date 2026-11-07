package handler

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"doexam/business/liveview"
	modelLive "doexam/models/liveview"
	"doexam/pkg/middleware"
)

// LiveViewHandler serves live camera/screen views (DOCS/PLAN/19-LIHAT-LAYAR-KAMERA.md).
type LiveViewHandler struct {
	svc *liveview.LiveViewService
}

// NewLiveViewHandler creates the handler.
func NewLiveViewHandler(svc *liveview.LiveViewService) *LiveViewHandler {
	return &LiveViewHandler{svc: svc}
}

// Open handles POST /api/v1/sessions/:id/live-views.
func (h *LiveViewHandler) Open(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelLive.OpenInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.svc.Open(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to open live view", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "live view opened", dto)
}

// Close handles DELETE /api/v1/sessions/:id/live-views/:vid.
func (h *LiveViewHandler) Close(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.svc.Close(c.UserContext(), principal(c), c.Params("id"), c.Params("vid"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to close live view", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "live view closed", nil)
}

// ProctorPoll handles GET /api/v1/sessions/:id/live-views/:vid/signal?after=&wait=.
func (h *LiveViewHandler) ProctorPoll(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	after, wait := pollParams(c)
	dto, err := h.svc.ProctorPoll(c.UserContext(), principal(c), c.Params("id"), c.Params("vid"), after, wait, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to poll live view", err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return SuccessResponse(c, fiber.StatusOK, "signals", dto)
}

// ProctorSignal handles POST /api/v1/sessions/:id/live-views/:vid/signal.
func (h *LiveViewHandler) ProctorSignal(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelLive.SignalInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	if err := h.svc.ProctorSignal(c.UserContext(), principal(c), c.Params("id"), c.Params("vid"), in); err != nil {
		return handleError(c, logger, "failed to send live view signal", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "signal sent", nil)
}

// ParticipantPoll handles GET /api/v1/attempts/:id/signal?after=&wait=.
func (h *LiveViewHandler) ParticipantPoll(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	after, wait := pollParams(c)
	dto, err := h.svc.ParticipantPoll(c.UserContext(), principal(c), c.Params("id"), after, wait)
	if err != nil {
		return handleError(c, logger, "failed to poll signals", err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return SuccessResponse(c, fiber.StatusOK, "signals", dto)
}

// ParticipantSignal handles POST /api/v1/attempts/:id/signal.
func (h *LiveViewHandler) ParticipantSignal(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelLive.SignalInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	if err := h.svc.ParticipantSignal(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to send signal", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "signal sent", nil)
}

// pollParams reads the long-poll cursor and wait (seconds, capped by the service).
func pollParams(c *fiber.Ctx) (int64, time.Duration) {
	after := int64(c.QueryInt("after", 0))
	if after < 0 {
		after = 0
	}
	return after, time.Duration(c.QueryInt("wait", 0)) * time.Second
}
