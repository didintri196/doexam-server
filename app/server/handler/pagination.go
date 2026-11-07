package handler

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"doexam/pkg/errr"
)

// Pagination defaults (AGENTS.md §7).
const (
	defaultPerPage = 20
	maxPerPage     = 100
)

// parsePage reads ?page= and ?per_page=. Missing values use defaults; per_page above
// maxPerPage is capped. Non-numeric or < 1 values are a validation error.
func parsePage(c *fiber.Ctx) (page, perPage int, err error) {
	return parsePageSized(c, defaultPerPage, maxPerPage)
}

// parsePageSized is parsePage with an endpoint's own default and cap (e.g. the monitor's
// 10 cards per page).
func parsePageSized(c *fiber.Ctx, def, max int) (page, perPage int, err error) {
	v := errr.Validation{}
	page = queryInt(c, "page", 1, v)
	perPage = min(queryInt(c, "per_page", def, v), max)
	return page, perPage, v.Err()
}

func queryInt(c *fiber.Ctx, key string, def int, v errr.Validation) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	v.Check(err == nil && n >= 1, key, "must be a positive integer")
	if err != nil || n < 1 {
		return def
	}
	return n
}
