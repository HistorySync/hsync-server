package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/service"
)

type handlerAuditStore struct {
	filter  model.AuditListFilter
	logs    []model.AuditLog
	created []model.AuditLog
}

func (s *handlerAuditStore) Create(_ context.Context, event *model.AuditLog) error {
	if event != nil {
		s.created = append(s.created, *event)
	}
	return nil
}

func (s *handlerAuditStore) List(_ context.Context, filter model.AuditListFilter) ([]model.AuditLog, error) {
	s.filter = filter
	return s.logs, nil
}

func (s *handlerAuditStore) ListTimeline(_ context.Context, filter model.AuditListFilter) ([]model.AuditLog, error) {
	s.filter = filter
	return s.logs, nil
}

func TestAdminListAuditLogsParsesFilters(t *testing.T) {
	actorID := uuid.New()
	store := &handlerAuditStore{
		logs: []model.AuditLog{{
			ID:          uuid.New(),
			ActorUserID: &actorID,
			EventType:   model.AuditEventLoginSuccess,
			TargetType:  "user",
			TargetID:    actorID.String(),
			Metadata:    map[string]any{"method": "password"},
			CreatedAt:   time.Now(),
		}},
	}
	h := New(Deps{
		Services: &service.Services{
			Audit: service.NewAuditService(store),
		},
		AdminKey: "secret",
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("GET", "/admin/audit-logs?limit=25&offset=10&actor_user_id="+actorID.String()+"&event_type=auth.login.success&target_type=user&target_id="+actorID.String(), nil)
	req.Header.Set("X-Admin-Key", "secret")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	if store.filter.ActorUserID == nil || *store.filter.ActorUserID != actorID {
		t.Fatalf("ActorUserID = %v, want %s", store.filter.ActorUserID, actorID)
	}
	if store.filter.EventType != model.AuditEventLoginSuccess {
		t.Fatalf("EventType = %q, want %q", store.filter.EventType, model.AuditEventLoginSuccess)
	}
	if store.filter.TargetType != "user" || store.filter.TargetID != actorID.String() {
		t.Fatalf("target = %q/%q, want user/%s", store.filter.TargetType, store.filter.TargetID, actorID)
	}
	if store.filter.Limit != 25 || store.filter.Offset != 10 {
		t.Fatalf("pagination = %d/%d, want 25/10", store.filter.Limit, store.filter.Offset)
	}

	var body struct {
		AuditLogs []model.AuditLog `json:"audit_logs"`
		Limit     int32            `json:"limit"`
		Offset    int32            `json:"offset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.AuditLogs) != 1 {
		t.Fatalf("audit log count = %d, want 1", len(body.AuditLogs))
	}
	if body.Limit != 25 || body.Offset != 10 {
		t.Fatalf("response pagination = %d/%d, want 25/10", body.Limit, body.Offset)
	}
}

func TestAdminSetSettingRecordsAuditEvent(t *testing.T) {
	auditStore := &handlerAuditStore{}
	settingStore := &handlerSettingStore{}
	settings := service.NewSettingsService(settingStore, []service.SettingDefinition{
		{
			Key:         "api_token",
			Type:        service.SettingTypeString,
			Default:     "",
			Description: "a secret",
			Group:       service.SettingGroupSecurity,
			Sensitive:   true,
		},
	})
	h := New(Deps{
		Services: &service.Services{
			Settings: settings,
			Audit:    service.NewAuditService(auditStore),
		},
		AdminKey: "secret",
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("PUT", "/admin/settings/api_token", strings.NewReader(`{"value":"shh"}`))
	req.Header.Set("X-Admin-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "audit-setting-write")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	if len(auditStore.created) != 1 {
		t.Fatalf("created audit count = %d, want 1", len(auditStore.created))
	}
	event := auditStore.created[0]
	if event.EventType != model.AuditEventAdminConfigChange {
		t.Fatalf("event type = %q, want %q", event.EventType, model.AuditEventAdminConfigChange)
	}
	if event.TargetType != "system_setting" || event.TargetID != "api_token" {
		t.Fatalf("target = %q/%q, want system_setting/api_token", event.TargetType, event.TargetID)
	}
	if event.Metadata["key"] != "api_token" || event.Metadata["group"] != service.SettingGroupSecurity {
		t.Fatalf("metadata = %+v, want key and group", event.Metadata)
	}
	if event.Metadata["sensitive"] != true {
		t.Fatalf("metadata sensitive = %v, want true", event.Metadata["sensitive"])
	}
	if _, ok := event.Metadata["value"]; ok {
		t.Fatalf("metadata leaked value: %+v", event.Metadata)
	}
	for _, value := range event.Metadata {
		if value == "shh" {
			t.Fatalf("metadata leaked sensitive plaintext: %+v", event.Metadata)
		}
	}
}

func TestAdminSecurityTimelineReturnsJSONAndWritesAudit(t *testing.T) {
	userID := uuid.New()
	store := &handlerAuditStore{
		logs: []model.AuditLog{{
			ID:          uuid.New(),
			ActorUserID: &userID,
			EventType:   model.AuditEventLoginFailure,
			TargetType:  "user",
			TargetID:    userID.String(),
			IP:          "203.0.113.4",
			Metadata: map[string]any{
				"email":  "user@example.com",
				"reason": "invalid_credentials",
			},
			CreatedAt: time.Now().UTC(),
		}},
	}
	services := &service.Services{
		Audit:            service.NewAuditService(store),
		SecurityTimeline: service.NewSecurityTimelineService(store, nil),
	}
	h := New(Deps{Services: services, AdminKey: "secret"})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("GET", "/api/v1/admin/security/timeline?email=user@example.com&format=json", nil)
	req.Header.Set("X-Admin-Key", "secret")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	var body model.SecurityTimelineResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(body.Events))
	}
	if body.Summary.AuthFailures != 1 {
		t.Fatalf("auth failures = %d, want 1", body.Summary.AuthFailures)
	}
	if body.Events[0].EmailHint == "" || body.Events[0].IPHash == "" {
		t.Fatalf("event not redacted enough: %+v", body.Events[0])
	}
	if len(store.created) != 1 || store.created[0].EventType != model.AuditEventAdminSecurityTimelineRead {
		t.Fatalf("audit query event = %+v, want timeline read audit", store.created)
	}
}
