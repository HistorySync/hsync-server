package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

type auditEventStore interface {
	Create(ctx context.Context, event *model.AuditLog) error
	List(ctx context.Context, filter model.AuditListFilter) ([]model.AuditLog, error)
}

type AuditService struct {
	store auditEventStore
}

type AuditEventInput struct {
	ActorUserID *uuid.UUID
	EventType   model.AuditEventType
	TargetType  string
	TargetID    string
	IP          string
	UserAgent   string
	Metadata    map[string]any
}

func NewAuditService(store auditEventStore) *AuditService {
	return &AuditService{store: store}
}

func (s *AuditService) Record(ctx context.Context, input AuditEventInput) error {
	if s == nil || s.store == nil {
		return nil
	}
	if input.EventType == "" {
		return fmt.Errorf("audit event type is required")
	}
	event := &model.AuditLog{
		ActorUserID: input.ActorUserID,
		EventType:   input.EventType,
		TargetType:  strings.TrimSpace(input.TargetType),
		TargetID:    strings.TrimSpace(input.TargetID),
		IP:          strings.TrimSpace(input.IP),
		UserAgent:   strings.TrimSpace(input.UserAgent),
		Metadata:    sanitizeAuditMetadata(input.Metadata),
	}
	return s.store.Create(ctx, event)
}

func (s *AuditService) List(ctx context.Context, filter model.AuditListFilter) ([]model.AuditLog, error) {
	if s == nil || s.store == nil {
		return []model.AuditLog{}, nil
	}
	logs, err := s.store.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	if logs == nil {
		return []model.AuditLog{}, nil
	}
	return logs, nil
}

func sanitizeAuditMetadata(metadata map[string]any) map[string]any {
	clean := make(map[string]any)
	for key, value := range metadata {
		if isSensitiveAuditMetadataKey(key) {
			continue
		}
		clean[key] = sanitizeAuditMetadataValue(value)
	}
	return clean
}

func sanitizeAuditMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeAuditMetadata(typed)
	case map[string]string:
		clean := make(map[string]any, len(typed))
		for key, value := range typed {
			if isSensitiveAuditMetadataKey(key) {
				continue
			}
			clean[key] = value
		}
		return clean
	case []any:
		clean := make([]any, 0, len(typed))
		for _, item := range typed {
			clean = append(clean, sanitizeAuditMetadataValue(item))
		}
		return clean
	default:
		return value
	}
}

func isSensitiveAuditMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return false
	}
	if normalized == "code" || normalized == "totp" || normalized == "cookie" {
		return true
	}
	for _, marker := range []string{"password", "secret", "token", "turnstile", "authorization", "backup_code"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
