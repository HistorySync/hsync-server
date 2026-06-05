package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
)

type memoryNotificationOutboxStore struct {
	items []model.NotificationOutbox
}

func (s *memoryNotificationOutboxStore) Enqueue(_ context.Context, item *model.NotificationOutbox) error {
	if item.ID == uuid.Nil {
		item.ID = uuid.New()
	}
	item.Status = model.NotificationOutboxPending
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	s.items = append(s.items, *item)
	return nil
}

func (s *memoryNotificationOutboxStore) ClaimDue(_ context.Context, now time.Time, limit int32) ([]model.NotificationOutbox, error) {
	var claimed []model.NotificationOutbox
	for i := range s.items {
		if int32(len(claimed)) >= limit {
			break
		}
		if s.items[i].Status == model.NotificationOutboxPending && !s.items[i].NextRetryAt.After(now) {
			s.items[i].Status = model.NotificationOutboxProcessing
			claimed = append(claimed, s.items[i])
		}
	}
	return claimed, nil
}

func (s *memoryNotificationOutboxStore) MarkSent(_ context.Context, id uuid.UUID, sentAt time.Time) error {
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Status = model.NotificationOutboxSent
			s.items[i].SentAt = &sentAt
		}
	}
	return nil
}

func (s *memoryNotificationOutboxStore) MarkRetry(_ context.Context, id uuid.UUID, nextRetryAt time.Time, errText string) error {
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Status = model.NotificationOutboxPending
			s.items[i].AttemptCount++
			s.items[i].NextRetryAt = nextRetryAt
			s.items[i].LastError = errText
		}
	}
	return nil
}

func (s *memoryNotificationOutboxStore) MarkFailed(_ context.Context, id uuid.UUID, errText string) error {
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Status = model.NotificationOutboxFailed
			s.items[i].AttemptCount++
			s.items[i].LastError = errText
		}
	}
	return nil
}

func (s *memoryNotificationOutboxStore) ListFailures(context.Context, int32, int32) ([]model.NotificationOutbox, error) {
	var failed []model.NotificationOutbox
	for _, item := range s.items {
		if item.Status == model.NotificationOutboxFailed {
			failed = append(failed, item)
		}
	}
	return failed, nil
}

type countingNotifier struct {
	sent int
	err  error
}

func (n *countingNotifier) DeliveryEnabled() bool { return true }
func (n *countingNotifier) SendWelcome(context.Context, provider.WelcomeParams) error {
	return nil
}
func (n *countingNotifier) SendEmailVerification(context.Context, provider.EmailVerificationParams) error {
	return nil
}
func (n *countingNotifier) SendPasswordReset(context.Context, provider.PasswordResetParams) error {
	return nil
}
func (n *countingNotifier) SendQuotaWarning(context.Context, provider.QuotaWarningParams) error {
	n.sent++
	return n.err
}
func (n *countingNotifier) SendQuotaExhausted(context.Context, provider.QuotaExhaustedParams) error {
	n.sent++
	return n.err
}
func (n *countingNotifier) SendQuotaRestored(context.Context, provider.QuotaRestoredParams) error {
	n.sent++
	return n.err
}
func (n *countingNotifier) SendNotification(context.Context, provider.NotificationParams) error {
	n.sent++
	return n.err
}

func TestNotificationOutboxEnqueueAndProcessSuccess(t *testing.T) {
	userID := uuid.New()
	before := metricValue(t, "hsync_notification_delivery_total", map[string]string{"category": "security", "result": "success"})
	prefs := &memoryNotificationPreferenceStore{
		prefs: map[uuid.UUID]model.NotificationPreferences{
			userID: {UserID: userID, SecurityEmail: true},
		},
	}
	users := &notificationUserMemoryStore{users: map[uuid.UUID]*model.User{
		userID: {ID: userID, Email: "user@example.com", DisplayName: "User"},
	}}
	outbox := &memoryNotificationOutboxStore{}
	notifier := &countingNotifier{}
	svc := NewNotificationServiceWithStoresAndOutbox(users, prefs, outbox, notifier, nil, NotificationConfig{Enabled: true})

	if err := svc.SendNotification(context.Background(), NotificationInput{
		UserID: userID, Category: NotificationCategorySecurity, Type: "security.login", Subject: "Login", Message: "Detected",
	}); err != nil {
		t.Fatalf("SendNotification: %v", err)
	}
	if len(outbox.items) != 1 || outbox.items[0].Channel != model.NotificationChannelEmail {
		t.Fatalf("outbox items = %+v, want one email item", outbox.items)
	}

	result, err := svc.ProcessOutbox(context.Background(), 10)
	if err != nil {
		t.Fatalf("ProcessOutbox: %v", err)
	}
	if result.Sent != 1 || notifier.sent != 1 || outbox.items[0].Status != model.NotificationOutboxSent {
		t.Fatalf("result=%+v sent=%d item=%+v", result, notifier.sent, outbox.items[0])
	}
	after := metricValue(t, "hsync_notification_delivery_total", map[string]string{"category": "security", "result": "success"})
	if after != before+1 {
		t.Fatalf("notification success metric delta = %v, want 1", after-before)
	}
}

func TestNotificationOutboxRetryAndPermanentFailure(t *testing.T) {
	userID := uuid.New()
	before := metricValue(t, "hsync_notification_delivery_total", map[string]string{"category": "security", "result": "failure"})
	prefs := &memoryNotificationPreferenceStore{
		prefs: map[uuid.UUID]model.NotificationPreferences{
			userID: {UserID: userID, SecurityEmail: true},
		},
	}
	users := &notificationUserMemoryStore{users: map[uuid.UUID]*model.User{
		userID: {ID: userID, Email: "user@example.com"},
	}}
	outbox := &memoryNotificationOutboxStore{}
	notifier := &countingNotifier{err: errors.New("smtp token=secret failed")}
	svc := NewNotificationServiceWithStoresAndOutbox(users, prefs, outbox, notifier, nil, NotificationConfig{Enabled: true})

	if err := svc.SendNotification(context.Background(), NotificationInput{
		UserID: userID, Category: NotificationCategorySecurity, Type: "security.login", Subject: "Login", Message: "Detected",
	}); err != nil {
		t.Fatalf("SendNotification: %v", err)
	}

	result, err := svc.ProcessOutbox(context.Background(), 10)
	if err != nil {
		t.Fatalf("ProcessOutbox retry: %v", err)
	}
	if result.Retried != 1 || outbox.items[0].Status != model.NotificationOutboxPending || outbox.items[0].AttemptCount != 1 {
		t.Fatalf("retry result=%+v item=%+v", result, outbox.items[0])
	}
	if outbox.items[0].LastError == "" || outbox.items[0].LastError == "smtp token=secret failed" {
		t.Fatalf("LastError = %q, want sanitized summary", outbox.items[0].LastError)
	}

	outbox.items[0].AttemptCount = defaultNotificationMaxAttempts - 1
	outbox.items[0].NextRetryAt = time.Now().Add(-time.Second)
	result, err = svc.ProcessOutbox(context.Background(), 10)
	if err != nil {
		t.Fatalf("ProcessOutbox failed: %v", err)
	}
	if result.Failed != 1 || outbox.items[0].Status != model.NotificationOutboxFailed {
		t.Fatalf("failed result=%+v item=%+v", result, outbox.items[0])
	}
	after := metricValue(t, "hsync_notification_delivery_total", map[string]string{"category": "security", "result": "failure"})
	if after != before+2 {
		t.Fatalf("notification failure metric delta = %v, want 2", after-before)
	}
}
