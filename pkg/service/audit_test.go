package service

import (
	"context"
	"testing"

	"github.com/historysync/hsync-server/pkg/model"
)

type fakeAuditStore struct {
	created *model.AuditLog
}

func (f *fakeAuditStore) Create(_ context.Context, event *model.AuditLog) error {
	copy := *event
	f.created = &copy
	return nil
}

func (f *fakeAuditStore) List(_ context.Context, _ model.AuditListFilter) ([]model.AuditLog, error) {
	return nil, nil
}

func TestAuditRecordSanitizesSensitiveMetadata(t *testing.T) {
	store := &fakeAuditStore{}
	svc := NewAuditService(store)

	if err := svc.Record(context.Background(), AuditEventInput{
		EventType: model.AuditEventLoginFailure,
		Metadata: map[string]any{
			"reason":          "invalid_credentials",
			"password":        "secret",
			"turnstile_token": "token",
			"nested": map[string]any{
				"totp_secret": "secret",
				"safe":        "value",
			},
		},
	}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if store.created == nil {
		t.Fatal("expected created audit event")
	}
	if _, ok := store.created.Metadata["password"]; ok {
		t.Fatal("password metadata was not removed")
	}
	if _, ok := store.created.Metadata["turnstile_token"]; ok {
		t.Fatal("turnstile token metadata was not removed")
	}
	nested, ok := store.created.Metadata["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested metadata type = %T, want map[string]any", store.created.Metadata["nested"])
	}
	if _, ok := nested["totp_secret"]; ok {
		t.Fatal("nested totp secret metadata was not removed")
	}
	if nested["safe"] != "value" {
		t.Fatalf("nested safe = %v, want value", nested["safe"])
	}
}
