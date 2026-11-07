package handler

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"doexam/business/attempt"
	modelAttempt "doexam/models/attempt"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	"doexam/pkg/middleware"
)

// headerDeviceID identifies the exam client's device (a UUID the client keeps in local storage).
const headerDeviceID = "X-Device-ID"

// AttemptHandler serves /api/v1/attempts, /api/v1/my/attempts and the proctor monitor.
type AttemptHandler struct {
	attemptSvc *attempt.AttemptService
}

// NewAttemptHandler returns a concrete AttemptHandler.
func NewAttemptHandler(attemptSvc *attempt.AttemptService) *AttemptHandler {
	return &AttemptHandler{attemptSvc: attemptSvc}
}

// Start handles POST /api/v1/attempts {session_id, token} (header X-Device-ID): start or resume.
func (h *AttemptHandler) Start(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelAttempt.StartInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.attemptSvc.Start(c.UserContext(), principal(c), in, c.Get(headerDeviceID), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to start attempt", err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return SuccessResponse(c, fiber.StatusOK, "attempt ready", dto)
}

// Get handles GET /api/v1/attempts/:id.
func (h *AttemptHandler) Get(c *fiber.Ctx) error {
	return h.view(c, "attempt retrieved", "failed to get attempt", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelAttempt.AttemptView, error) {
		return h.attemptSvc.Get(ctx, p, c.Params("id"), cl)
	})
}

// Sync handles POST /api/v1/attempts/:id/events {events}.
func (h *AttemptHandler) Sync(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelAttempt.EventsInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.attemptSvc.Sync(c.UserContext(), principal(c), c.Params("id"), in, c.Get(headerDeviceID), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to sync attempt events", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "events synced", dto)
}

// FinishSection handles POST /api/v1/attempts/:id/sections/:section_id/finish.
func (h *AttemptHandler) FinishSection(c *fiber.Ctx) error {
	return h.view(c, "section finished", "failed to finish section", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelAttempt.AttemptView, error) {
		return h.attemptSvc.FinishSection(ctx, p, c.Params("id"), c.Params("section_id"), cl)
	})
}

// Submit handles POST /api/v1/attempts/:id/submit.
func (h *AttemptHandler) Submit(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.attemptSvc.Submit(c.UserContext(), principal(c), c.Params("id"), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to submit attempt", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "attempt submitted", dto)
}

// Result handles GET /api/v1/attempts/:id/result.
func (h *AttemptHandler) Result(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.attemptSvc.Result(c.UserContext(), principal(c), c.Params("id"), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to get result", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "result retrieved", dto)
}

// MyAttempts handles GET /api/v1/my/attempts.
func (h *AttemptHandler) MyAttempts(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	out, err := h.attemptSvc.MyAttempts(c.UserContext(), principal(c))
	if err != nil {
		return handleError(c, logger, "failed to list my attempts", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "attempts retrieved", out)
}

// Monitor handles GET /api/v1/sessions/:id/monitor (proctor dashboard).
func (h *AttemptHandler) Monitor(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.attemptSvc.Monitor(c.UserContext(), principal(c), c.Params("id"), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to load monitor", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "monitor retrieved", dto)
}

// monitorPerPage: the monitor shows 10 cards per page (DOCS/PLAN/15-MONITOR-KARTU.md); a
// larger per_page is capped so no poll ever loads more than one page of cards.
const monitorPerPage = 10

// MonitorList handles GET /api/v1/sessions/:id/monitor/list?view&violation_type&group_id&seat&q&page&per_page:
// one page of a monitor view, paged in SQL (DOCS/PLAN/15-MONITOR-KARTU.md).
func (h *AttemptHandler) MonitorList(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePageSized(c, monitorPerPage, monitorPerPage)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	f := modelAttempt.MonitorFilter{
		View: modelAttempt.MonitorView(c.Query("view")), ViolationType: c.Query("violation_type"),
		GroupID: c.Query("group_id"), Seat: c.Query("seat"), Query: c.Query("q"), Page: page, PerPage: perPage,
	}
	dto, total, err := h.attemptSvc.MonitorList(c.UserContext(), principal(c), c.Params("id"), f, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to load monitor", err)
	}
	return PaginatedResponse(c, dto, total, page, perPage)
}

// MonitorWatch handles POST /api/v1/sessions/:id/monitor/watch {user_ids} (at most 25).
func (h *AttemptHandler) MonitorWatch(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelAttempt.WatchInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.attemptSvc.MonitorWatch(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to load monitor", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "monitor retrieved", dto)
}

// Extend handles POST /api/v1/attempts/:id/extend {minutes}.
func (h *AttemptHandler) Extend(c *fiber.Ctx) error {
	var in modelAttempt.ExtendInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.view(c, "time extended", "failed to extend attempt", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelAttempt.AttemptView, error) {
		return h.attemptSvc.Extend(ctx, p, c.Params("id"), in, cl)
	})
}

// GrantPlays handles POST /api/v1/attempts/:id/grant-plays {plays}.
func (h *AttemptHandler) GrantPlays(c *fiber.Ctx) error {
	var in modelAttempt.GrantPlaysInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.view(c, "plays granted", "failed to grant plays", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelAttempt.AttemptView, error) {
		return h.attemptSvc.GrantPlays(ctx, p, c.Params("id"), in, cl)
	})
}

// Lock handles POST /api/v1/attempts/:id/lock.
func (h *AttemptHandler) Lock(c *fiber.Ctx) error {
	return h.view(c, "attempt locked", "failed to lock attempt", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelAttempt.AttemptView, error) {
		return h.attemptSvc.Lock(ctx, p, c.Params("id"), cl)
	})
}

// Unlock handles POST /api/v1/attempts/:id/unlock.
func (h *AttemptHandler) Unlock(c *fiber.Ctx) error {
	return h.view(c, "attempt unlocked", "failed to unlock attempt", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelAttempt.AttemptView, error) {
		return h.attemptSvc.Unlock(ctx, p, c.Params("id"), cl)
	})
}

// ResetDevice handles POST /api/v1/attempts/:id/reset-device.
func (h *AttemptHandler) ResetDevice(c *fiber.Ctx) error {
	return h.view(c, "device reset", "failed to reset device", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelAttempt.AttemptView, error) {
		return h.attemptSvc.ResetDevice(ctx, p, c.Params("id"), cl)
	})
}

// ForceSubmit handles POST /api/v1/attempts/:id/force-submit.
func (h *AttemptHandler) ForceSubmit(c *fiber.Ctx) error {
	return h.view(c, "attempt submitted", "failed to force submit", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelAttempt.AttemptView, error) {
		return h.attemptSvc.ForceSubmit(ctx, p, c.Params("id"), cl)
	})
}

func (h *AttemptHandler) view(c *fiber.Ctx, msg, failMsg string,
	call func(context.Context, modelAuth.Principal, modelAudit.Client) (modelAttempt.AttemptView, error)) error {
	dto, err := call(c.UserContext(), principal(c), clientInfo(c))
	if err != nil {
		return handleError(c, middleware.GetLogger(c), failMsg, err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return SuccessResponse(c, fiber.StatusOK, msg, dto)
}
