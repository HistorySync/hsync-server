// Package middleware provides shared HTTP middleware for HistorySync services.
package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

const (
	// RequestIDHeader is the canonical request identifier header.
	RequestIDHeader = "X-Request-ID"

	// RequestIDLocalKey is the Fiber locals key used to store the request ID.
	RequestIDLocalKey = "request_id"
)

// RequestID ensures every request has a stable ID available in headers and locals.
func RequestID() fiber.Handler {
	return func(c fiber.Ctx) error {
		requestID := strings.TrimSpace(c.Get(RequestIDHeader))
		if requestID == "" {
			requestID = uuid.NewString()
		}

		c.Locals(RequestIDLocalKey, requestID)
		c.Set(RequestIDHeader, requestID)

		return c.Next()
	}
}

// GetRequestID returns the request ID stored on the Fiber context.
func GetRequestID(c fiber.Ctx) string {
	requestID, _ := c.Locals(RequestIDLocalKey).(string)
	return requestID
}
