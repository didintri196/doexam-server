package handler

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"doexam/business/exam"
	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelExam "doexam/models/exam"
	"doexam/pkg/middleware"
)

// ExamHandler serves /api/v1/exams, /sections, /section-items and /section-rules.
type ExamHandler struct {
	examSvc *exam.ExamService
}

// NewExamHandler returns a concrete ExamHandler.
func NewExamHandler(examSvc *exam.ExamService) *ExamHandler {
	return &ExamHandler{examSvc: examSvc}
}

// List handles GET /api/v1/exams?status&subject_id&q&mine&page&per_page.
func (h *ExamHandler) List(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	f := modelExam.ListFilter{
		Page: page, PerPage: perPage, Status: modelExam.Status(c.Query("status")),
		SubjectID: c.Query("subject_id"), Query: c.Query("q"), Mine: c.QueryBool("mine"),
	}
	out, total, err := h.examSvc.List(c.UserContext(), principal(c), f)
	if err != nil {
		return handleError(c, logger, "failed to list exams", err)
	}
	return PaginatedResponse(c, out, total, page, perPage)
}

// Get handles GET /api/v1/exams/:id.
func (h *ExamHandler) Get(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.examSvc.Get(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get exam", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "exam retrieved", dto)
}

// Check handles GET /api/v1/exams/:id/check (dry run of publish).
func (h *ExamHandler) Check(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.examSvc.Check(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to check exam", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "exam checked", dto)
}

// Create handles POST /api/v1/exams.
func (h *ExamHandler) Create(c *fiber.Ctx) error {
	var in modelExam.ExamInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusCreated, "exam created", "failed to create exam", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.Create(ctx, p, in, cl)
	})
}

// Update handles PATCH /api/v1/exams/:id.
func (h *ExamHandler) Update(c *fiber.Ctx) error {
	var in modelExam.ExamInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusOK, "exam updated", "failed to update exam", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.Update(ctx, p, c.Params("id"), in, cl)
	})
}

// Delete handles DELETE /api/v1/exams/:id.
func (h *ExamHandler) Delete(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.examSvc.Delete(c.UserContext(), principal(c), c.Params("id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to delete exam", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "exam deleted", nil)
}

// Publish handles POST /api/v1/exams/:id/publish.
func (h *ExamHandler) Publish(c *fiber.Ctx) error {
	return h.respond(c, fiber.StatusOK, "exam published", "failed to publish exam", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.Publish(ctx, p, c.Params("id"), cl)
	})
}

// Unpublish handles POST /api/v1/exams/:id/unpublish.
func (h *ExamHandler) Unpublish(c *fiber.Ctx) error {
	return h.respond(c, fiber.StatusOK, "exam unpublished", "failed to unpublish exam", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.Unpublish(ctx, p, c.Params("id"), cl)
	})
}

// Archive handles POST /api/v1/exams/:id/archive.
func (h *ExamHandler) Archive(c *fiber.Ctx) error {
	return h.respond(c, fiber.StatusOK, "exam archived", "failed to archive exam", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.Archive(ctx, p, c.Params("id"), cl)
	})
}

// CreateSection handles POST /api/v1/exams/:id/sections.
func (h *ExamHandler) CreateSection(c *fiber.Ctx) error {
	var in modelExam.SectionInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusCreated, "section created", "failed to create section", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.CreateSection(ctx, p, c.Params("id"), in, cl)
	})
}

// OrderSections handles POST /api/v1/exams/:id/sections/order.
func (h *ExamHandler) OrderSections(c *fiber.Ctx) error {
	var in modelExam.OrderInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusOK, "sections reordered", "failed to reorder sections", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.OrderSections(ctx, p, c.Params("id"), in, cl)
	})
}

// UpdateSection handles PATCH /api/v1/sections/:id.
func (h *ExamHandler) UpdateSection(c *fiber.Ctx) error {
	var in modelExam.SectionInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusOK, "section updated", "failed to update section", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.UpdateSection(ctx, p, c.Params("id"), in, cl)
	})
}

// DeleteSection handles DELETE /api/v1/sections/:id.
func (h *ExamHandler) DeleteSection(c *fiber.Ctx) error {
	return h.respond(c, fiber.StatusOK, "section deleted", "failed to delete section", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.DeleteSection(ctx, p, c.Params("id"), cl)
	})
}

// AddItems handles POST /api/v1/sections/:id/items.
func (h *ExamHandler) AddItems(c *fiber.Ctx) error {
	var in modelExam.AddItemsInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusOK, "items added", "failed to add items", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.AddItems(ctx, p, c.Params("id"), in, cl)
	})
}

// OrderSectionItems handles POST /api/v1/sections/:id/items/order.
func (h *ExamHandler) OrderSectionItems(c *fiber.Ctx) error {
	var in modelExam.OrderInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusOK, "items reordered", "failed to reorder items", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.OrderSectionItems(ctx, p, c.Params("id"), in, cl)
	})
}

// UpdateSectionItem handles PATCH /api/v1/section-items/:id.
func (h *ExamHandler) UpdateSectionItem(c *fiber.Ctx) error {
	var in modelExam.SectionItemUpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusOK, "item updated", "failed to update section item", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.UpdateSectionItem(ctx, p, c.Params("id"), in, cl)
	})
}

// RemoveSectionItem handles DELETE /api/v1/section-items/:id.
func (h *ExamHandler) RemoveSectionItem(c *fiber.Ctx) error {
	return h.respond(c, fiber.StatusOK, "item removed", "failed to remove section item", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.RemoveSectionItem(ctx, p, c.Params("id"), cl)
	})
}

// CreateRule handles POST /api/v1/sections/:id/rules.
func (h *ExamHandler) CreateRule(c *fiber.Ctx) error {
	var in modelExam.RuleInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusCreated, "rule created", "failed to create rule", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.CreateRule(ctx, p, c.Params("id"), in, cl)
	})
}

// UpdateRule handles PATCH /api/v1/section-rules/:id.
func (h *ExamHandler) UpdateRule(c *fiber.Ctx) error {
	var in modelExam.RuleInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	return h.respond(c, fiber.StatusOK, "rule updated", "failed to update rule", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.UpdateRule(ctx, p, c.Params("id"), in, cl)
	})
}

// DeleteRule handles DELETE /api/v1/section-rules/:id.
func (h *ExamHandler) DeleteRule(c *fiber.Ctx) error {
	return h.respond(c, fiber.StatusOK, "rule deleted", "failed to delete rule", func(ctx context.Context, p modelAuth.Principal, cl modelAudit.Client) (modelExam.ExamDTO, error) {
		return h.examSvc.DeleteRule(ctx, p, c.Params("id"), cl)
	})
}

// respond runs a service call that returns the whole exam and writes the envelope.
// Every structure change answers with the updated exam so the editor can re-render at once.
func (h *ExamHandler) respond(c *fiber.Ctx, status int, msg, failMsg string,
	call func(context.Context, modelAuth.Principal, modelAudit.Client) (modelExam.ExamDTO, error)) error {
	dto, err := call(c.UserContext(), principal(c), clientInfo(c))
	if err != nil {
		return handleError(c, middleware.GetLogger(c), failMsg, err)
	}
	return SuccessResponse(c, status, msg, dto)
}
