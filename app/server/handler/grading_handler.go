package handler

import (
	"github.com/gofiber/fiber/v2"

	"doexam/business/attempt"
	modelAttempt "doexam/models/attempt"
	"doexam/pkg/middleware"
)

// GradingHandler serves essay grading and regrade adjustments.
type GradingHandler struct {
	attemptSvc *attempt.AttemptService
}

// NewGradingHandler returns a concrete GradingHandler.
func NewGradingHandler(attemptSvc *attempt.AttemptService) *GradingHandler {
	return &GradingHandler{attemptSvc: attemptSvc}
}

// Summary handles GET /api/v1/exams/:id/grading/summary.
func (h *GradingHandler) Summary(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	out, err := h.attemptSvc.GradingSummary(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to load grading summary", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "grading summary retrieved", out)
}

// Queue handles GET /api/v1/exams/:id/grading?item_version_id&status&blind&page&per_page.
func (h *GradingHandler) Queue(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	f := modelAttempt.GradingFilter{Page: page, PerPage: perPage, ItemVersionID: c.Query("item_version_id"),
		Status: c.Query("status"), Blind: c.QueryBool("blind")}
	out, total, err := h.attemptSvc.GradingQueue(c.UserContext(), principal(c), c.Params("id"), f)
	if err != nil {
		return handleError(c, logger, "failed to load grading queue", err)
	}
	return PaginatedResponse(c, out, total, page, perPage)
}

// Item handles GET /api/v1/grading/items/:id.
func (h *GradingHandler) Item(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.attemptSvc.GradingItem(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to load answer", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "answer retrieved", dto)
}

// Grade handles POST /api/v1/grading/items/:id {score | rubric_scores, comment}.
func (h *GradingHandler) Grade(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelAttempt.GradeInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.attemptSvc.Grade(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to grade answer", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "answer graded", dto)
}

// ListAdjustments handles GET /api/v1/exams/:id/adjustments.
func (h *GradingHandler) ListAdjustments(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	out, err := h.attemptSvc.ListAdjustments(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to list adjustments", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "adjustments retrieved", out)
}

// CreateAdjustment handles POST /api/v1/exams/:id/adjustments.
func (h *GradingHandler) CreateAdjustment(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelAttempt.AdjustmentInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.attemptSvc.CreateAdjustment(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to create adjustment", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "exam regraded", dto)
}

// RevokeAdjustment handles DELETE /api/v1/adjustments/:id.
func (h *GradingHandler) RevokeAdjustment(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.attemptSvc.RevokeAdjustment(c.UserContext(), principal(c), c.Params("id"), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to revoke adjustment", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "adjustment revoked, exam regraded", dto)
}
