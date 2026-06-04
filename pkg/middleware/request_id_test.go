package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestRequestIDGeneratesHeader(t *testing.T) {
	app := fiber.New()
	app.Use(RequestID())
	app.Get("/", func(c fiber.Ctx) error {
		if GetRequestID(c) == "" {
			t.Fatal("request id local is empty")
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequest(fiber.MethodGet, "/", nil)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.Header.Get(RequestIDHeader) == "" {
		t.Fatal("response request id header is empty")
	}
}

func TestRequestIDKeepsIncomingHeader(t *testing.T) {
	const requestID = "req-test-123"

	app := fiber.New()
	app.Use(RequestID())
	app.Get("/", func(c fiber.Ctx) error {
		if got := GetRequestID(c); got != requestID {
			t.Fatalf("request id local = %q, want %q", got, requestID)
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequest(fiber.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, requestID)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get(RequestIDHeader); got != requestID {
		t.Fatalf("response request id header = %q, want %q", got, requestID)
	}
}
