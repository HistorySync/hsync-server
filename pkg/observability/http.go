package observability

import "github.com/gofiber/fiber/v3"

// HTTPMetricsMiddleware records one low-cardinality request counter per handled
// request. It uses Fiber's registered route path, not the raw URL.
func HTTPMetricsMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		err := c.Next()
		route := "unknown"
		if r := c.Route(); r != nil && r.Path != "" {
			route = r.Path
		}
		status := c.Response().StatusCode()
		if err != nil && status < 400 {
			status = fiber.StatusInternalServerError
		}
		RecordHTTPRequest(route, c.Method(), status)
		return err
	}
}
