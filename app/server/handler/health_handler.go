package handler

import (
	"github.com/gofiber/fiber/v2"

	"doexam/business/health"
)

type HealthHandler struct {
	healthSvc *health.HealthService
}

func NewHealthHandler(healthSvc *health.HealthService) *HealthHandler {
	return &HealthHandler{healthSvc: healthSvc}
}

// Healthz is the liveness probe: 200 while the process is running.
func (h *HealthHandler) Healthz(c *fiber.Ctx) error {
	dto := h.healthSvc.Liveness(c.UserContext())
	return SuccessResponse(c, fiber.StatusOK, "service is alive", dto)
}

// Readyz is the readiness probe: 200 when all dependencies are up, 503 otherwise.
func (h *HealthHandler) Readyz(c *fiber.Ctx) error {
	dto, err := h.healthSvc.Readiness(c.UserContext())
	if err != nil {
		return ErrorResponse(c, fiber.StatusServiceUnavailable, "service not ready", dto)
	}
	return SuccessResponse(c, fiber.StatusOK, "service is ready", dto)
}
