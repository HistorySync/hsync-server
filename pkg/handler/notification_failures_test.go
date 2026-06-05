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
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/service"
)

type handlerNotificationOutboxStore struct {
	items []model.NotificationOutbox
}

func (s *handlerNotificationOutboxStore) Enqueue(context.Context, *model.NotificationOutbox) error {
	return nil
}

func (s *handlerNotificationOutboxStore) ClaimDue(context.Context, time.Time, int32) ([]model.NotificationOutbox, error) {
	return nil, nil
}

func (s *handlerNotificationOutboxStore) GetByID(_ context.Context, id uuid.UUID) (*model.NotificationOutbox, error) {
	for i := range s.items {
		if s.items[i].ID == id {
			item := s.items[i]
			return &item, nil
		}
	}
	return nil, nil
}

func (s *handlerNotificationOutboxStore) ClaimFailedByID(_ context.Context, id uuid.UUID) (*model.NotificationOutbox, error) {
	for i := range s.items {
		if s.items[i].ID == id && s.items[i].Status == model.NotificationOutboxFailed {
			s.items[i].Status = model.NotificationOutboxProcessing
			item := s.items[i]
			return &item, nil
		}
	}
	return nil, nil
}

func (s *handlerNotificationOutboxStore) ClaimFailed(_ context.Context, limit int32) ([]model.NotificationOutbox, error) {
	var claimed []model.NotificationOutbox
	for i := range s.items {
		if int32(len(claimed)) >= limit {
			break
		}
		if s.items[i].Status == model.NotificationOutboxFailed {
			s.items[i].Status = model.NotificationOutboxProcessing
			claimed = append(claimed, s.items[i])
		}
	}
	return claimed, nil
}

func (s *handlerNotificationOutboxStore) MarkSent(context.Context, uuid.UUID, time.Time) error {
	return nil
}

func (s *handlerNotificationOutboxStore) MarkRetry(context.Context, uuid.UUID, time.Time, string) error {
	return nil
}

func (s *handlerNotificationOutboxStore) MarkFailed(context.Context, uuid.UUID, string) error {
	return nil
}

func (s *handlerNotificationOutboxStore) RequeueFailed(_ context.Context, id uuid.UUID, nextRetryAt time.Time) (bool, error) {
	for i := range s.items {
		if s.items[i].ID == id && s.items[i].Status == model.NotificationOutboxFailed {
			s.items[i].Status = model.NotificationOutboxPending
			s.items[i].AttemptCount = 0
			s.items[i].NextRetryAt = nextRetryAt
			s.items[i].LastError = ""
			return true, nil
		}
	}
	return false, nil
}

func (s *handlerNotificationOutboxStore) MarkDiscarded(_ context.Context, id uuid.UUID) (bool, error) {
	for i := range s.items {
		if s.items[i].ID == id && s.items[i].Status == model.NotificationOutboxFailed {
			s.items[i].Status = model.NotificationOutboxDiscarded
			return true, nil
		}
	}
	return false, nil
}

func (s *handlerNotificationOutboxStore) ListFailures(context.Context, int32, int32) ([]model.NotificationOutbox, error) {
	return s.items, nil
}

type handlerIdempotencyStore struct {
	records map[string]*model.IdempotencyRecord
}

func (s *handlerIdempotencyStore) Claim(_ context.Context, p repository.IdempotencyClaimParams) (repository.IdempotencyClaimResult, error) {
	if s.records == nil {
		s.records = map[string]*model.IdempotencyRecord{}
	}
	key := p.Scope + "\x00" + p.IdempotencyKeyHash
	if existing := s.records[key]; existing != nil {
		if existing.RequestFingerprint != p.RequestFingerprint {
			return repository.IdempotencyClaimResult{Status: repository.IdempotencyClaimConflict, Record: *existing}, nil
		}
		if existing.Status == model.IdempotencyStatusSucceeded {
			return repository.IdempotencyClaimResult{Status: repository.IdempotencyClaimReplayed, Record: *existing}, nil
		}
		return repository.IdempotencyClaimResult{Status: repository.IdempotencyClaimProcessing, Record: *existing}, nil
	}
	record := &model.IdempotencyRecord{
		ID:                 uuid.New(),
		Scope:              p.Scope,
		IdempotencyKeyHash: p.IdempotencyKeyHash,
		RequestFingerprint: p.RequestFingerprint,
		Status:             model.IdempotencyStatusProcessing,
		LockedUntil:        &p.LockedUntil,
		ExpiresAt:          p.ExpiresAt,
		CreatedAt:          p.Now,
		UpdatedAt:          p.Now,
	}
	s.records[key] = record
	return repository.IdempotencyClaimResult{Status: repository.IdempotencyClaimStarted, Record: *record}, nil
}

func (s *handlerIdempotencyStore) MarkSucceeded(_ context.Context, id uuid.UUID, responseStatus int, responseBody []byte, now time.Time) error {
	for _, record := range s.records {
		if record.ID == id {
			record.Status = model.IdempotencyStatusSucceeded
			record.LockedUntil = nil
			record.ResponseStatus = &responseStatus
			record.ResponseBody = append([]byte(nil), responseBody...)
			record.UpdatedAt = now
			return nil
		}
	}
	return nil
}

func (s *handlerIdempotencyStore) MarkFailed(_ context.Context, id uuid.UUID, reason string, now time.Time) error {
	for _, record := range s.records {
		if record.ID == id {
			record.Status = model.IdempotencyStatusFailed
			record.LockedUntil = nil
			record.ErrorReason = reason
			record.UpdatedAt = now
			return nil
		}
	}
	return nil
}

func TestAdminNotificationFailuresReturnsSanitizedFailureViews(t *testing.T) {
	userID := uuid.New()
	failureID := uuid.New()
	now := time.Unix(1_700_000_000, 0).UTC()
	outbox := &handlerNotificationOutboxStore{
		items: []model.NotificationOutbox{{
			ID:           failureID,
			UserID:       userID,
			Channel:      model.NotificationChannelWebhook,
			Category:     service.NotificationCategorySecurity,
			Type:         "security.login",
			PayloadJSON:  json.RawMessage(`{"secret":"do-not-return"}`),
			Status:       model.NotificationOutboxFailed,
			AttemptCount: 3,
			LastError:    `send webhook: Post "https://example.com/hook?token=secret&ok=1": timeout secret=abc123`,
			CreatedAt:    now,
			UpdatedAt:    now,
		}},
	}
	h := New(Deps{
		Services: &service.Services{
			Notification: service.NewNotificationServiceWithStoresAndOutbox(nil, nil, outbox, provider.NewLogNotifier(), nil, service.NotificationConfig{}),
		},
		AdminKey: "secret",
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("GET", "/admin/notifications/failures?limit=25", nil)
	req.Header.Set("X-Admin-Key", "secret")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	body := string(data)
	for _, leaked := range []string{"do-not-return", "PayloadJSON", "payload_json", "last_error", "token=secret", "abc123"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("notification failure response leaked %q: %s", leaked, body)
		}
	}
	if !strings.Contains(body, "error_summary") || !strings.Contains(body, "https://example.com/...") {
		t.Fatalf("notification failure response missing sanitized summary: %s", body)
	}
}

func TestAdminNotificationFailureActionReplaysIdempotentResponseAndAuditsOnce(t *testing.T) {
	notificationID := uuid.New()
	outbox := &handlerNotificationOutboxStore{
		items: []model.NotificationOutbox{{
			ID:           notificationID,
			Status:       model.NotificationOutboxFailed,
			AttemptCount: 4,
			LastError:    "timeout",
		}},
	}
	auditStore := &handlerAuditStore{}
	h := New(Deps{
		Services: &service.Services{
			Notification: service.NewNotificationServiceWithStoresAndOutbox(nil, nil, outbox, provider.NewLogNotifier(), nil, service.NotificationConfig{}),
			Idempotency:  service.NewIdempotencyService(&handlerIdempotencyStore{}),
			Audit:        service.NewAuditService(auditStore),
		},
		AdminKey: "secret",
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	url := "/admin/notifications/failures/" + notificationID.String() + "/requeue"
	first := httptest.NewRequest("POST", url, strings.NewReader(`{}`))
	first.Header.Set("X-Admin-Key", "secret")
	first.Header.Set("Content-Type", "application/json")
	first.Header.Set("Idempotency-Key", "same-key")
	resp, err := app.Test(first)
	if err != nil {
		t.Fatalf("first app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("first status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	var firstBody service.NotificationOutboxActionResult
	if err := json.NewDecoder(resp.Body).Decode(&firstBody); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if firstBody.Result != service.NotificationOutboxActionRequeued || firstBody.Replayed {
		t.Fatalf("first body = %+v, want fresh requeue", firstBody)
	}
	if outbox.items[0].Status != model.NotificationOutboxPending {
		t.Fatalf("outbox status = %q, want pending", outbox.items[0].Status)
	}
	if len(auditStore.created) != 1 {
		t.Fatalf("audit count after first = %d, want 1", len(auditStore.created))
	}
	event := auditStore.created[0]
	if event.EventType != model.AuditEventNotificationOutboxRequeue {
		t.Fatalf("audit event = %q, want %q", event.EventType, model.AuditEventNotificationOutboxRequeue)
	}
	if event.TargetType != "notification_outbox" || event.TargetID != notificationID.String() {
		t.Fatalf("audit target = %q/%q", event.TargetType, event.TargetID)
	}
	if event.Metadata["result"] != string(service.NotificationOutboxActionRequeued) {
		t.Fatalf("audit metadata = %+v, want requeued result", event.Metadata)
	}

	second := httptest.NewRequest("POST", url, strings.NewReader(`{}`))
	second.Header.Set("X-Admin-Key", "secret")
	second.Header.Set("Content-Type", "application/json")
	second.Header.Set("Idempotency-Key", "same-key")
	resp, err = app.Test(second)
	if err != nil {
		t.Fatalf("second app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("second status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	var secondBody service.NotificationOutboxActionResult
	if err := json.NewDecoder(resp.Body).Decode(&secondBody); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if secondBody.Result != service.NotificationOutboxActionRequeued || !secondBody.Replayed {
		t.Fatalf("second body = %+v, want replayed requeue", secondBody)
	}
	if len(auditStore.created) != 1 {
		t.Fatalf("audit count after replay = %d, want 1", len(auditStore.created))
	}
}

func TestAdminNotificationFailureActionRequiresIdempotencyKey(t *testing.T) {
	notificationID := uuid.New()
	outbox := &handlerNotificationOutboxStore{
		items: []model.NotificationOutbox{{ID: notificationID, Status: model.NotificationOutboxFailed}},
	}
	h := New(Deps{
		Services: &service.Services{
			Notification: service.NewNotificationServiceWithStoresAndOutbox(nil, nil, outbox, provider.NewLogNotifier(), nil, service.NotificationConfig{}),
			Idempotency:  service.NewIdempotencyService(&handlerIdempotencyStore{}),
		},
		AdminKey: "secret",
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("POST", "/admin/notifications/failures/"+notificationID.String()+"/discard", strings.NewReader(`{}`))
	req.Header.Set("X-Admin-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusBadRequest)
	}
}
