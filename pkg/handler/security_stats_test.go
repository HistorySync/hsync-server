package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/service"
)

type handlerSecurityAuditStore struct {
	counts []model.SecurityEventWindowCount
}

func (s *handlerSecurityAuditStore) SecurityEventCounts(_ context.Context, _, _, _ time.Time) ([]model.SecurityEventWindowCount, error) {
	return s.counts, nil
}

type handlerSecurityUserStore struct {
	enabledUsers int64
	totalUsers   int64
}

func (s *handlerSecurityUserStore) TwoFactorEnabledStats(_ context.Context) (int64, int64, error) {
	return s.enabledUsers, s.totalUsers, nil
}

func TestAdminSecurityStatsRequiresAdminKey(t *testing.T) {
	h := New(Deps{AdminKey: "secret"})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("GET", "/api/v1/admin/security/stats", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusUnauthorized)
	}
}

func TestAdminSecurityStatsReturnsAggregates(t *testing.T) {
	statsSvc := service.NewSecurityStatsService(
		&handlerSecurityAuditStore{
			counts: []model.SecurityEventWindowCount{
				{EventType: model.AuditEventLoginSuccess, Last24h: 1, Last7d: 3},
				{EventType: model.AuditEventLoginFailure, Last24h: 2, Last7d: 4},
				{EventType: model.AuditEventTwoFactorChallengeFailure, Last24h: 1, Last7d: 2},
			},
		},
		&handlerSecurityUserStore{enabledUsers: 2, totalUsers: 5},
	)
	h := New(Deps{
		Services: &service.Services{SecurityStats: statsSvc},
		AdminKey: "secret",
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("GET", "/api/v1/admin/security/stats", nil)
	req.Header.Set("X-Admin-Key", "secret")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	var body model.SecurityStats
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Last24h.LoginSuccess != 1 || body.Last7d.LoginFailure != 4 {
		t.Fatalf("login counts = %+v/%+v, want 1 last24h success and 4 last7d failures", body.Last24h, body.Last7d)
	}
	if body.Last24h.TwoFactorChallengeFailure != 1 || body.Last7d.TwoFactorChallengeFailure != 2 {
		t.Fatalf("2fa failure counts = %+v/%+v, want 1/2", body.Last24h, body.Last7d)
	}
	if body.TwoFactor.EnabledUsers != 2 || body.TwoFactor.TotalUsers != 5 || body.TwoFactor.EnabledRatio != 0.4 {
		t.Fatalf("two factor stats = %+v, want 2/5/0.4", body.TwoFactor)
	}
	if len(body.EventsByType) != 3 {
		t.Fatalf("events by type len = %d, want 3", len(body.EventsByType))
	}
}

func TestAdminSecurityStatsEmptyDataStableShape(t *testing.T) {
	h := New(Deps{
		Services: &service.Services{
			SecurityStats: service.NewSecurityStatsService(nil, nil),
		},
		AdminKey: "secret",
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("GET", "/api/v1/admin/security/stats", nil)
	req.Header.Set("X-Admin-Key", "secret")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	var body model.SecurityStats
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Last24h.LoginSuccess != 0 || body.Last7d.TwoFactorChallengeFailure != 0 {
		t.Fatalf("empty counts are not zero: %+v/%+v", body.Last24h, body.Last7d)
	}
	if body.EventsByType == nil {
		t.Fatal("EventsByType is nil, want empty slice")
	}
}
