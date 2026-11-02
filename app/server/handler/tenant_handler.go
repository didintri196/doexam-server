package handler

import (
	"encoding/hex"
	"io"

	"github.com/gofiber/fiber/v2"

	"doexam/business/tenant"
	modelTenant "doexam/models/tenant"
	"doexam/pkg/errr"
	"doexam/pkg/middleware"
)

// TenantHandler serves first-run setup, the institution profile and its public view.
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

// Update handles PATCH /api/v1/tenant.
func (h *TenantHandler) Update(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelTenant.UpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.tenantSvc.Update(c.UserContext(), principal(c), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update tenant", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "tenant updated", dto)
}

// SetLogo handles PUT /api/v1/tenant/logo (multipart: file).
func (h *TenantHandler) SetLogo(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	fh, err := c.FormFile("file")
	if err != nil {
		return handleError(c, logger, "missing file", &errr.ValidationError{Fields: map[string]string{"file": "required"}})
	}
	f, err := fh.Open()
	if err != nil {
		return handleError(c, logger, "failed to open upload", err)
	}
	defer f.Close()
	// read one byte past the limit so the service can reject oversized files
	data, err := io.ReadAll(io.LimitReader(f, modelTenant.MaxLogoBytes+1))
	if err != nil {
		return handleError(c, logger, "failed to read upload", err)
	}
	dto, err := h.tenantSvc.SetLogo(c.UserContext(), principal(c), fh.Filename, data, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to set logo", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "logo updated", dto)
}

// RemoveLogo handles DELETE /api/v1/tenant/logo.
func (h *TenantHandler) RemoveLogo(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.tenantSvc.RemoveLogo(c.UserContext(), principal(c), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to remove logo", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "logo removed", dto)
}

// Public handles GET /api/v1/public/tenant?tenant=<slug> (no auth; the slug is optional
// while the installation serves a single institution).
func (h *TenantHandler) Public(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.tenantSvc.Public(c.UserContext(), c.Query("tenant"))
	if err != nil {
		return handleError(c, logger, "failed to get tenant", err)
	}
	c.Set(fiber.HeaderCacheControl, "no-cache")
	return SuccessResponse(c, fiber.StatusOK, "tenant retrieved", dto)
}

// Logo handles GET /api/v1/public/tenant/logo?tenant=<slug> (no auth).
func (h *TenantHandler) Logo(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	logo, err := h.tenantSvc.Logo(c.UserContext(), c.Query("tenant"))
	if err != nil {
		return handleError(c, logger, "failed to get logo", err)
	}
	etag := `"` + hex.EncodeToString(logo.SHA256) + `"`
	c.Set(fiber.HeaderETag, etag)
	// URLs from logo_url carry the file id, so a day of caching never shows a stale logo.
	c.Set(fiber.HeaderCacheControl, "public, max-age=86400")
	c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
	if c.Get(fiber.HeaderIfNoneMatch) == etag {
		return c.SendStatus(fiber.StatusNotModified)
	}
	c.Set(fiber.HeaderContentType, logo.MimeType)
	return c.Status(fiber.StatusOK).Send(logo.Data)
}
