package handler

import (
	"encoding/hex"
	"io"

	"github.com/gofiber/fiber/v2"

	"doexam/business/media"
	modelMedia "doexam/models/media"
	"doexam/pkg/errr"
	"doexam/pkg/middleware"
)

// MediaHandler serves /api/v1/media.
type MediaHandler struct {
	mediaSvc *media.MediaService
}

// NewMediaHandler returns a concrete MediaHandler.
func NewMediaHandler(mediaSvc *media.MediaService) *MediaHandler {
	return &MediaHandler{mediaSvc: mediaSvc}
}

// Upload handles POST /api/v1/media (multipart field "file").
func (h *MediaHandler) Upload(c *fiber.Ctx) error {
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
	data, err := io.ReadAll(io.LimitReader(f, modelMedia.MaxImageBytes+1))
	if err != nil {
		return handleError(c, logger, "failed to read upload", err)
	}
	dto, err := h.mediaSvc.Upload(c.UserContext(), principal(c), fh.Filename, data, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to upload media", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "media uploaded", dto)
}

// Links handles POST /api/v1/media/links {ids}: signed links for media found in content.
func (h *MediaHandler) Links(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	var in modelMedia.LinksInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	refs, err := h.mediaSvc.Links(c.UserContext(), principal(c), in)
	if err != nil {
		return handleError(c, logger, "failed to sign media links", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "media links", refs)
}

// Serve handles GET /api/v1/media/:id?t&e&s (signed link, no login).
func (h *MediaHandler) Serve(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)

	f, err := h.mediaSvc.Serve(c.UserContext(), c.Params("id"), c.Query("t"), c.Query("e"), c.Query("s"))
	if err != nil {
		return handleError(c, logger, "failed to serve media", err)
	}
	etag := `"` + hex.EncodeToString(f.SHA256) + `"`
	c.Set(fiber.HeaderETag, etag)
	// Content behind an id never changes; private: exam pictures must not sit in shared caches.
	c.Set(fiber.HeaderCacheControl, "private, max-age=86400, immutable")
	c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
	c.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if c.Get(fiber.HeaderIfNoneMatch) == etag {
		return c.SendStatus(fiber.StatusNotModified)
	}
	c.Set(fiber.HeaderContentType, f.MimeType)
	return c.Status(fiber.StatusOK).Send(f.Data)
}
