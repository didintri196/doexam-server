package handler

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/gofiber/fiber/v2"

	"doexam/business/importer"
	modelImport "doexam/models/importer"
	"doexam/pkg/errr"
	"doexam/pkg/middleware"
	"doexam/pkg/spreadsheet"
)

const xlsxMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// ImportHandler serves /api/v1/imports.
type ImportHandler struct {
	importSvc *importer.ImportService
}

// NewImportHandler returns a concrete ImportHandler.
func NewImportHandler(importSvc *importer.ImportService) *ImportHandler {
	return &ImportHandler{importSvc: importSvc}
}

// Template handles GET /api/v1/imports/templates/:kind (xlsx download).
func (h *ImportHandler) Template(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	kind := modelImport.Kind(c.Params("kind"))
	tpl, err := h.importSvc.Template(principal(c), kind)
	if err != nil {
		return handleError(c, logger, "failed to get import template", err)
	}
	var buf bytes.Buffer
	if err := spreadsheet.WriteTemplate(&buf, tpl); err != nil {
		return handleError(c, logger, "failed to write import template", err)
	}
	return sendXLSX(c, fmt.Sprintf("template-impor-%s.xlsx", kind), buf.Bytes())
}

// Upload handles POST /api/v1/imports (multipart: kind, file, optional audience and group_id).
func (h *ImportHandler) Upload(c *fiber.Ctx) error {
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
	data, err := io.ReadAll(io.LimitReader(f, modelImport.MaxFileBytes+1))
	if err != nil {
		return handleError(c, logger, "failed to read upload", err)
	}

	target := modelImport.Target{Audience: modelImport.Audience(c.FormValue("audience")), GroupID: c.FormValue("group_id")}
	dto, err := h.importSvc.Upload(c.UserContext(), principal(c), modelImport.Kind(c.FormValue("kind")), target, fh.Filename, data, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to upload import", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "import validated", dto)
}

// List handles GET /api/v1/imports?kind&audience&group_id&page&per_page.
func (h *ImportHandler) List(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	jobs, total, err := h.importSvc.List(c.UserContext(), principal(c),
		modelImport.ListFilter{Page: page, PerPage: perPage, Kind: modelImport.Kind(c.Query("kind")),
			Audience: modelImport.Audience(c.Query("audience")), GroupID: c.Query("group_id")})
	if err != nil {
		return handleError(c, logger, "failed to list imports", err)
	}
	return PaginatedResponse(c, jobs, total, page, perPage)
}

// Get handles GET /api/v1/imports/:id.
func (h *ImportHandler) Get(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.importSvc.Get(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get import", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "import retrieved", dto)
}

// Commit handles POST /api/v1/imports/:id/commit (202: runs in the background, poll Get).
func (h *ImportHandler) Commit(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	dto, err := h.importSvc.Commit(c.UserContext(), principal(c), c.Params("id"), clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to commit import", err)
	}
	return SuccessResponse(c, fiber.StatusAccepted, "import started", dto)
}

// Credentials handles GET /api/v1/imports/:id/credentials (xlsx, one-time download).
func (h *ImportHandler) Credentials(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	creds, err := h.importSvc.Credentials(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get import credentials", err)
	}
	rows := make([][]string, len(creds))
	for i, cr := range creds {
		rows[i] = []string{fmt.Sprint(cr.Row), cr.Username, cr.FullName, cr.Password}
	}
	var buf bytes.Buffer
	if err := spreadsheet.WriteTable(&buf, "Akun", []string{"baris", "username", "full_name", "password_sementara"}, rows); err != nil {
		return handleError(c, logger, "failed to write credentials", err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return sendXLSX(c, fmt.Sprintf("akun-impor-%s.xlsx", time.Now().Format("20060102-150405")), buf.Bytes())
}

func sendXLSX(c *fiber.Ctx, filename string, data []byte) error {
	c.Set(fiber.HeaderContentType, xlsxMIME)
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, filename))
	return c.Status(fiber.StatusOK).Send(data)
}
