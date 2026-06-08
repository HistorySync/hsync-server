package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/historysync/hsync-server/pkg/apierrors"
	"github.com/historysync/hsync-server/pkg/middleware"
)

func TestAdminRoutesHaveIndependentRateLimit(t *testing.T) {
	limiter := middleware.NewMemoryLimiter()
	h := New(Deps{
		AdminKey:    "secret",
		RateLimiter: limiter,
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	for i := 0; i < adminRPM; i++ {
		req := httptest.NewRequest("GET", "/admin/stats", nil)
		req.Header.Set("X-Admin-Key", "wrong")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d app.Test() error = %v", i+1, err)
		}
		if resp.StatusCode != fiber.StatusForbidden {
			t.Fatalf("request %d status = %d, want forbidden before limit exhaustion", i+1, resp.StatusCode)
		}
	}

	req := httptest.NewRequest("GET", "/admin/stats", nil)
	req.Header.Set("X-Admin-Key", "wrong")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("limited app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusTooManyRequests {
		t.Fatalf("limited status = %d, want %d", resp.StatusCode, fiber.StatusTooManyRequests)
	}
	if got := resp.Header.Get("X-RateLimit-Limit"); got != "60" {
		t.Fatalf("X-RateLimit-Limit = %q, want 60", got)
	}
}

func TestAdminMutationRequiresIdempotencyKey(t *testing.T) {
	app := newSettingsTestApp(&handlerSettingStore{})

	req := httptest.NewRequest("PUT", "/admin/settings/feature_enabled", strings.NewReader(`{"value":"true"}`))
	req.Header.Set("X-Admin-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusBadRequest)
	}
	if code := decodeErrorCode(t, resp.Body); code != string(apierrors.CodeIdempotencyKeyMissing) {
		t.Fatalf("error code = %q, want %s", code, apierrors.CodeIdempotencyKeyMissing)
	}
}
