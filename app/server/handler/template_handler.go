package handler

import (
	"bytes"

	"github.com/gofiber/fiber/v2"

	"doexam/business/importer"
	"doexam/business/itembank"
	modelImport "doexam/models/importer"
	"doexam/pkg/errr"
	"doexam/pkg/middleware"
	"doexam/pkg/spreadsheet"
)

const docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// PublicTemplate handles GET /api/v1/public/templates/:name (no login). Templates hold
// only instructions and example rows, so they are served like static files: a plain link
// downloads them with their name, also in browsers that ignore the download attribute of
// blob links (and then save files under a random name without extension).
func PublicTemplate(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	c.Set(fiber.HeaderCacheControl, "public, max-age=3600")

	switch name := c.Params("name"); name {
	case "template-impor-soal.docx":
		data, err := itembank.WordTemplate()
		if err != nil {
			return handleError(c, logger, "failed to build word template", err)
		}
		c.Set(fiber.HeaderContentType, docxMIME)
		c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+name+`"`)
		return c.Status(fiber.StatusOK).Send(data)
	case "template-impor-users.xlsx", "template-impor-groups.xlsx":
		kind := modelImport.KindUsers
		if name == "template-impor-groups.xlsx" {
			kind = modelImport.KindGroups
		}
		var buf bytes.Buffer
		if err := spreadsheet.WriteTemplate(&buf, importer.Template(kind)); err != nil {
			return handleError(c, logger, "failed to write import template", err)
		}
		return sendXLSX(c, name, buf.Bytes())
	}
	return handleError(c, logger, "unknown template", errr.ErrNotFound)
}
