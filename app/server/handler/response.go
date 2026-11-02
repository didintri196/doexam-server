package handler

import (
	"github.com/gofiber/fiber/v2"

	"doexam/pkg/middleware"
)

// APIResponse is the standard response envelope.
type APIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	TraceID string `json:"trace_id"`
	Data    any    `json:"data,omitempty"`
	Errors  any    `json:"errors,omitempty"`
	Meta    any    `json:"meta,omitempty"`
}

func SuccessResponse(c *fiber.Ctx, statusCode int, message string, data any) error {
	return c.Status(statusCode).JSON(APIResponse{
		Success: true,
		Message: message,
		TraceID: middleware.GetTraceID(c),
		Data:    data,
	})
}

func ErrorResponse(c *fiber.Ctx, statusCode int, message string, errors any) error {
	return c.Status(statusCode).JSON(APIResponse{
		Success: false,
		Message: message,
		TraceID: middleware.GetTraceID(c),
		Errors:  errors,
	})
}

func PaginatedResponse(c *fiber.Ctx, data any, total int64, page, perPage int) error {
	return c.JSON(APIResponse{
		Success: true,
		Message: "success",
		TraceID: middleware.GetTraceID(c),
		Data:    data,
		Meta: fiber.Map{
			"total":    total,
			"page":     page,
			"per_page": perPage,
		},
	})
}
