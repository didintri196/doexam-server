package handler

import (
	"io"

	"github.com/gofiber/fiber/v2"

	"doexam/business/itembank"
	modelBank "doexam/models/itembank"
	"doexam/pkg/errr"
	"doexam/pkg/middleware"
)

// ItemBankHandler serves /api/v1/item-banks, /api/v1/stimuli and /api/v1/items.
type ItemBankHandler struct {
	bankSvc *itembank.ItemBankService
}

// NewItemBankHandler returns a concrete ItemBankHandler.
func NewItemBankHandler(bankSvc *itembank.ItemBankService) *ItemBankHandler {
	return &ItemBankHandler{bankSvc: bankSvc}
}

// ---- banks ----

// ListBanks handles GET /api/v1/item-banks?subject_id&q&page&per_page.
func (h *ItemBankHandler) ListBanks(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	out, total, err := h.bankSvc.ListBanks(c.UserContext(), principal(c),
		modelBank.BankFilter{Page: page, PerPage: perPage, SubjectID: c.Query("subject_id"), Query: c.Query("q")})
	if err != nil {
		return handleError(c, logger, "failed to list item banks", err)
	}
	return PaginatedResponse(c, out, total, page, perPage)
}

// GetBank handles GET /api/v1/item-banks/:id.
func (h *ItemBankHandler) GetBank(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.bankSvc.GetBank(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get item bank", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "item bank retrieved", dto)
}

// CreateBank handles POST /api/v1/item-banks.
func (h *ItemBankHandler) CreateBank(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelBank.BankInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.bankSvc.CreateBank(c.UserContext(), principal(c), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to create item bank", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "item bank created", dto)
}

// UpdateBank handles PATCH /api/v1/item-banks/:id.
func (h *ItemBankHandler) UpdateBank(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelBank.BankUpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.bankSvc.UpdateBank(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update item bank", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "item bank updated", dto)
}

// DeleteBank handles DELETE /api/v1/item-banks/:id.
func (h *ItemBankHandler) DeleteBank(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.bankSvc.DeleteBank(c.UserContext(), principal(c), c.Params("id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to delete item bank", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "item bank deleted", nil)
}

// ListMembers handles GET /api/v1/item-banks/:id/members?page&per_page.
func (h *ItemBankHandler) ListMembers(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	out, total, err := h.bankSvc.ListMembers(c.UserContext(), principal(c), c.Params("id"),
		modelBank.MemberFilter{Page: page, PerPage: perPage})
	if err != nil {
		return handleError(c, logger, "failed to list item bank members", err)
	}
	return PaginatedResponse(c, out, total, page, perPage)
}

// SetMember handles POST /api/v1/item-banks/:id/members.
func (h *ItemBankHandler) SetMember(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelBank.MemberInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	if err := h.bankSvc.SetMember(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to set item bank member", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "member saved", nil)
}

// RemoveMember handles DELETE /api/v1/item-banks/:id/members/:user_id.
func (h *ItemBankHandler) RemoveMember(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.bankSvc.RemoveMember(c.UserContext(), principal(c), c.Params("id"), c.Params("user_id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to remove item bank member", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "member removed", nil)
}

// ---- stimuli ----

// ListStimuli handles GET /api/v1/item-banks/:id/stimuli?q&page&per_page.
func (h *ItemBankHandler) ListStimuli(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	out, total, err := h.bankSvc.ListStimuli(c.UserContext(), principal(c), c.Params("id"), c.Query("q"), page, perPage)
	if err != nil {
		return handleError(c, logger, "failed to list stimuli", err)
	}
	return PaginatedResponse(c, out, total, page, perPage)
}

// CreateStimulus handles POST /api/v1/item-banks/:id/stimuli.
func (h *ItemBankHandler) CreateStimulus(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelBank.StimulusInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.bankSvc.CreateStimulus(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to create stimulus", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "stimulus created", dto)
}

// GetStimulus handles GET /api/v1/stimuli/:id.
func (h *ItemBankHandler) GetStimulus(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.bankSvc.GetStimulus(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get stimulus", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "stimulus retrieved", dto)
}

// UpdateStimulus handles PATCH /api/v1/stimuli/:id.
func (h *ItemBankHandler) UpdateStimulus(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelBank.StimulusUpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.bankSvc.UpdateStimulus(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update stimulus", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "stimulus updated", dto)
}

// DeleteStimulus handles DELETE /api/v1/stimuli/:id.
func (h *ItemBankHandler) DeleteStimulus(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.bankSvc.DeleteStimulus(c.UserContext(), principal(c), c.Params("id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to delete stimulus", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "stimulus deleted", nil)
}

// ---- items ----

// ListItems handles GET /api/v1/item-banks/:id/items?status&type&competency_id&difficulty&tag&q&page&per_page.
func (h *ItemBankHandler) ListItems(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	f := modelBank.ItemFilter{
		Page: page, PerPage: perPage, Status: modelBank.ItemStatus(c.Query("status")), Type: modelBank.ItemType(c.Query("type")),
		CompetencyID: c.Query("competency_id"), Difficulty: c.Query("difficulty"), Tag: c.Query("tag"), Query: c.Query("q"),
	}
	out, total, err := h.bankSvc.ListItems(c.UserContext(), principal(c), c.Params("id"), f)
	if err != nil {
		return handleError(c, logger, "failed to list items", err)
	}
	return PaginatedResponse(c, out, total, page, perPage)
}

// CreateItem handles POST /api/v1/item-banks/:id/items.
func (h *ItemBankHandler) CreateItem(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelBank.ItemInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.bankSvc.CreateItem(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to create item", err)
	}
	return SuccessResponse(c, fiber.StatusCreated, "item created", dto)
}

// GetItem handles GET /api/v1/items/:id.
func (h *ItemBankHandler) GetItem(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.bankSvc.GetItem(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to get item", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "item retrieved", dto)
}

// UpdateItem handles PATCH /api/v1/items/:id.
func (h *ItemBankHandler) UpdateItem(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	var in modelBank.ItemUpdateInput
	if err := c.BodyParser(&in); err != nil {
		return badBody(c)
	}
	dto, err := h.bankSvc.UpdateItem(c.UserContext(), principal(c), c.Params("id"), in, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to update item", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "item updated", dto)
}

// DeleteItem handles DELETE /api/v1/items/:id.
func (h *ItemBankHandler) DeleteItem(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	if err := h.bankSvc.DeleteItem(c.UserContext(), principal(c), c.Params("id"), clientInfo(c)); err != nil {
		return handleError(c, logger, "failed to delete item", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "item deleted", nil)
}

// ListVersions handles GET /api/v1/items/:id/versions.
func (h *ItemBankHandler) ListVersions(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	out, err := h.bankSvc.ListVersions(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to list item versions", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "item versions retrieved", out)
}

// ImportWord handles POST /api/v1/item-banks/:id/import-word (multipart "file", .docx).
// ?preview=1 only reads the document; otherwise the valid items are created as drafts.
func (h *ItemBankHandler) ImportWord(c *fiber.Ctx) error {
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
	data, err := io.ReadAll(io.LimitReader(f, modelBank.MaxWordBytes+1))
	if err != nil {
		return handleError(c, logger, "failed to read upload", err)
	}
	commit := c.Query("preview") == ""
	dto, err := h.bankSvc.ImportWord(c.UserContext(), principal(c), c.Params("id"), fh.Filename, data, commit, clientInfo(c))
	if err != nil {
		return handleError(c, logger, "failed to import word document", err)
	}
	if commit {
		return SuccessResponse(c, fiber.StatusCreated, "items imported", dto)
	}
	return SuccessResponse(c, fiber.StatusOK, "word document read", dto)
}

// WordTemplate handles GET /api/v1/item-banks/import-word/template (.docx download).
func (h *ItemBankHandler) WordTemplate(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	data, err := itembank.WordTemplate()
	if err != nil {
		return handleError(c, logger, "failed to build word template", err)
	}
	c.Set(fiber.HeaderContentType, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="template-impor-soal.docx"`)
	return c.Status(fiber.StatusOK).Send(data)
}
