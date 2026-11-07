package handler

import (
	"bytes"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"

	"doexam/business/report"
	modelReport "doexam/models/report"
	"doexam/pkg/middleware"
	"doexam/pkg/spreadsheet"
)

// ReportHandler serves score recaps, item analysis and individual reports.
type ReportHandler struct {
	reportSvc *report.ReportService
}

// NewReportHandler returns a concrete ReportHandler.
func NewReportHandler(reportSvc *report.ReportService) *ReportHandler {
	return &ReportHandler{reportSvc: reportSvc}
}

// Recap handles GET /api/v1/exams/:id/recap?session_id&group_id&page&per_page. Rows are one
// page of participants; counts and meta.total cover every matching participant.
func (h *ReportHandler) Recap(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	page, perPage, err := parsePage(c)
	if err != nil {
		return handleError(c, logger, "invalid pagination", err)
	}
	f := recapFilter(c)
	f.Page, f.PerPage = page, perPage
	dto, total, err := h.reportSvc.Recap(c.UserContext(), principal(c), c.Params("id"), f)
	if err != nil {
		return handleError(c, logger, "failed to build recap", err)
	}
	return PaginatedResponse(c, dto, int64(total), page, perPage)
}

// RecapExport handles GET /api/v1/exams/:id/recap/export?session_id&group_id (xlsx, every participant).
func (h *ReportHandler) RecapExport(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, _, err := h.reportSvc.Recap(c.UserContext(), principal(c), c.Params("id"), recapFilter(c))
	if err != nil {
		return handleError(c, logger, "failed to build recap", err)
	}
	headers, rows := report.RecapTable(dto)
	var buf bytes.Buffer
	if err := spreadsheet.WriteTable(&buf, "Rekap", headers, rows); err != nil {
		return handleError(c, logger, "failed to write recap", err)
	}
	return sendXLSX(c, fmt.Sprintf("rekap-nilai-%s.xlsx", time.Now().Format("20060102-1504")), buf.Bytes())
}

// Analysis handles GET /api/v1/exams/:id/analysis?session_id (computed on first request).
func (h *ReportHandler) Analysis(c *fiber.Ctx) error {
	return h.analysis(c, false)
}

// Recompute handles POST /api/v1/exams/:id/analysis?session_id (forces a fresh computation).
func (h *ReportHandler) Recompute(c *fiber.Ctx) error {
	return h.analysis(c, true)
}

func (h *ReportHandler) analysis(c *fiber.Ctx, recompute bool) error {
	logger := middleware.GetLogger(c)
	dto, err := h.reportSvc.Analysis(c.UserContext(), principal(c), c.Params("id"), c.Query("session_id"), recompute)
	if err != nil {
		return handleError(c, logger, "failed to analyse exam", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "analysis retrieved", dto)
}

// AttemptReport handles GET /api/v1/attempts/:id/report (staff view).
func (h *ReportHandler) AttemptReport(c *fiber.Ctx) error {
	logger := middleware.GetLogger(c)
	dto, err := h.reportSvc.AttemptReport(c.UserContext(), principal(c), c.Params("id"))
	if err != nil {
		return handleError(c, logger, "failed to build attempt report", err)
	}
	return SuccessResponse(c, fiber.StatusOK, "attempt report retrieved", dto)
}

func recapFilter(c *fiber.Ctx) modelReport.RecapFilter {
	return modelReport.RecapFilter{SessionID: c.Query("session_id"), GroupID: c.Query("group_id")}
}
