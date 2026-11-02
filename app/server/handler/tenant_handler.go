package handler

import (
	"github.com/gofiber/fiber/v2"

	"doexam/business/tenant"
	modelTenant "doexam/models/tenant"
	"doexam/pkg/middleware"
)

// TenantHandler serves first-run setup and the current tenant.
type TenantHandler struct {
	tenantSvc *tenant.TenantService
}

// NewTenantHandler returns a concrete TenantHandler.
func NewTenantHandler(tenantSvc *tenant.TenantService) *TenantHandler {
	return &TenantHandler{tenantSvc: tenantSvc}
}

// Setup handles POST /api/v1/setup: creates the first tenant and its admin.
func (h *TenantHandler) Setup(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelTenant.SetupInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.tenantSvc.Setup(c.UserContext(), in)
	if err != nil {
		return handleError(c, logger, "failed to setup tenant", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "setup completed", dto)
}

// Current handles GET /api/v1/tenant.
func (h *TenantHandler) Current(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.tenantSvc.Current(c.UserContext(), principal(c))
	if err != nil {
		return handleError(c, logger, "failed to get tenant", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "tenant retrieved", dto)
}
