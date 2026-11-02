package handler

import (
	"github.com/gofiber/fiber/v2"

	"doexam/business/subject"
	modelSubject "doexam/models/subject"
	"doexam/pkg/middleware"
)

// SubjectHandler serves /api/v1/subjects and /api/v1/competencies.
type SubjectHandler struct {
	subjectSvc *subject.SubjectService
}

// NewSubjectHandler returns a concrete SubjectHandler.
func NewSubjectHandler(subjectSvc *subject.SubjectService) *SubjectHandler {
	return &SubjectHandler{subjectSvc: subjectSvc}
}

// List handles GET /api/v1/subjects?q&page&per_page.
func (h *SubjectHandler) List(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	out, total, err := h.subjectSvc.List(c.UserContext(), principal(c), modelSubject.ListFilter{Page: page, PerPage: perPage, Query: c.Query("q")})
	if err != nil {
		return handleError(c, logger, "failed to list subjects", err)
	}
	return PaginatedResponse(c, out, total, page, perPage)
}

// Get handles GET /api/v1/subjects/:id.
func (h *SubjectHandler) Get(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.subjectSvc.Get(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get subject", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "subject retrieved", dto)
}

// Create handles POST /api/v1/subjects.
func (h *SubjectHandler) Create(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSubject.SubjectInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.subjectSvc.Create(c.UserContext(), principal(c), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to create subject", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "subject created", dto)
}

// Update handles PATCH /api/v1/subjects/:id.
func (h *SubjectHandler) Update(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSubject.SubjectUpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.subjectSvc.Update(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update subject", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "subject updated", dto)
}

// Delete handles DELETE /api/v1/subjects/:id.
func (h *SubjectHandler) Delete(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.subjectSvc.Delete(c.UserContext(), principal(c), c.Params("id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to delete subject", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "subject deleted", nil)
}

// ListCompetencies handles GET /api/v1/subjects/:id/competencies.
func (h *SubjectHandler) ListCompetencies(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	out, err := h.subjectSvc.ListCompetencies(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to list competencies", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "competencies retrieved", out)
}

// CreateCompetency handles POST /api/v1/subjects/:id/competencies.
func (h *SubjectHandler) CreateCompetency(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSubject.CompetencyInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.subjectSvc.CreateCompetency(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to create competency", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "competency created", dto)
}

// UpdateCompetency handles PATCH /api/v1/competencies/:id.
func (h *SubjectHandler) UpdateCompetency(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelSubject.CompetencyUpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.subjectSvc.UpdateCompetency(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update competency", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "competency updated", dto)
}

// DeleteCompetency handles DELETE /api/v1/competencies/:id.
func (h *SubjectHandler) DeleteCompetency(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.subjectSvc.DeleteCompetency(c.UserContext(), principal(c), c.Params("id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to delete competency", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "competency deleted", nil)
}
