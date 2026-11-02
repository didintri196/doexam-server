package handler

import (
	"errors"
	"log/slog"

	"github.com/gofiber/fiber/v2"

	"doexam/pkg/errr"
)

// handleError maps a service error to an HTTP response (AGENTS.md §5).
// 4xx responses use a fixed or client-safe message (errr.WithMessage);
// anything unexpected is logged with msg and returned as a generic 500.
func handleError(c *fiber.Ctx, logger *slog.Logger, msg string, err error) error {
	var ve *errr.ValidationError
	if errors.As(err, &ve) {
		return ErrorResponse(c, fiber.StatusBadRequest, "invalid input", ve.Fields)
	}

	status, message := fiber.StatusInternalServerError, "internal server error"
	switch {
	case errors.Is(err, errr.ErrInvalidInput):
		status, message = fiber.StatusBadRequest, "invalid input"
	case errors.Is(err, errr.ErrUnauthorized):
		status, message = fiber.StatusUnauthorized, "unauthorized"
	case errors.Is(err, errr.ErrForbidden):
		status, message = fiber.StatusForbidden, "forbidden"
	case errors.Is(err, errr.ErrNotFound):
		status, message = fiber.StatusNotFound, "not found"
	case errors.Is(err, errr.ErrAlreadyExists):
		status, message = fiber.StatusConflict, "already exists"
	case errors.Is(err, errr.ErrConflict):
		status, message = fiber.StatusConflict, "conflict"
	case errors.Is(err, errr.ErrUnavailable):
		status, message = fiber.StatusServiceUnavailable, "service not ready"
	}

	if status == fiber.StatusInternalServerError {
		logger.ErrorContext(c.UserContext(), msg, slog.String("error", err.Error()))
		return ErrorResponse(c, status, message, nil)
	}

	var pe *errr.PublicError
	if errors.As(err, &pe) {
		message = pe.Message
	}
	return ErrorResponse(c, status, message, nil)
}

// badBody is the response for a request body that is not valid JSON.
func badBody(c *fiber.Ctx) error {
	return ErrorResponse(c, fiber.StatusBadRequest, "invalid request body", nil)
}
