package service

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

const (
	defaultSecurityTimelineLimit    = int32(200)
	maxSecurityTimelineLimit        = int32(1000)
	defaultSecurityTimelineLookback = 7 * 24 * time.Hour
	maxSecurityTimelineScan         = 5000
)

type securityTimelineAuditStore interface {
	ListTimeline(ctx context.Context, filter model.AuditListFilter) ([]model.AuditLog, error)
}

type securityTimelineUserStore interface {
	GetByEmail(ctx context.Context, email string) (*model.User, error)
}

type SecurityTimelineService struct {
	audit securityTimelineAuditStore
	users securityTimelineUserStore
	now   func() time.Time
}

func NewSecurityTimelineService(audit securityTimelineAuditStore, users securityTimelineUserStore) *SecurityTimelineService {
	return &SecurityTimelineService{
		audit: audit,
		users: users,
		now:   time.Now,
	}
}

func (s *SecurityTimelineService) Lookup(ctx context.Context, filter model.SecurityTimelineFilter) (*model.SecurityTimelineResponse, error) {
	now := time.Now().UTC()
	if s != nil && s.now != nil {
		now = s.now().UTC()
	}
	filter, since, until, err := normalizeSecurityTimelineFilter(filter, now)
	if err != nil {
		return nil, err
	}
	out := &model.SecurityTimelineResponse{
		GeneratedAt: now,
		Filter:      filter,
		Events:      []model.SecurityTimelineEvent{},
		Summary: model.SecurityTimelineSummary{
			Actions: []model.AuditEventTypeCount{},
		},
	}
	if s == nil || s.audit == nil {
		return out, nil
	}

	resolvedUserID, err := s.resolveTimelineUserID(ctx, filter.Email)
	if err != nil {
		return nil, fmt.Errorf("resolve timeline user: %w", err)
	}
	if filter.UserID != "" {
		parsedUserID, parseErr := uuid.Parse(filter.UserID)
		if parseErr != nil {
			return nil, fmt.Errorf("parse timeline user id: %w", parseErr)
		}
		if resolvedUserID == nil {
			resolvedUserID = &parsedUserID
		}
	}

	need := int(filter.Offset + filter.Limit)
	if need < int(defaultSecurityTimelineLimit) {
		need = int(defaultSecurityTimelineLimit)
	}
	batchSize := int32(500)
	if batchSize > maxSecurityTimelineLimit {
		batchSize = maxSecurityTimelineLimit
	}

	matches := make([]model.SecurityTimelineEvent, 0, need)
	seen := make(map[string]struct{})
	scanLimit := int32(need)
	if scanLimit < batchSize {
		scanLimit = batchSize
	}
	if scanLimit > maxSecurityTimelineScan {
		scanLimit = maxSecurityTimelineScan
	}
	logs, err := s.audit.ListTimeline(ctx, model.AuditListFilter{
		ActorUserID: resolvedUserID,
		EventType:   model.AuditEventType(filter.Action),
		Email:       filter.Email,
		Since:       since,
		Until:       until,
		Limit:       scanLimit,
		Offset:      0,
	})
	if err != nil {
		return nil, fmt.Errorf("list timeline audit logs: %w", err)
	}
	for _, log := range logs {
		event, ok := securityTimelineEventFromAuditLog(log)
		if !ok || !securityTimelineMatchesLog(log, filter, resolvedUserID) {
			continue
		}
		if _, exists := seen[event.ID]; exists {
			continue
		}
		seen[event.ID] = struct{}{}
		matches = append(matches, event)
	}

	out.Total = int64(len(matches))
	out.Truncated = len(logs) >= int(scanLimit) && scanLimit >= maxSecurityTimelineScan
	out.Summary = summarizeSecurityTimeline(matches)

	start := int(filter.Offset)
	if start > len(matches) {
		start = len(matches)
	}
	end := start + int(filter.Limit)
	if end > len(matches) {
		end = len(matches)
	}
	out.Events = append(out.Events, matches[start:end]...)
	if end < len(matches) {
		out.Truncated = true
	}
	return out, nil
}

func SecurityTimelineCSV(response *model.SecurityTimelineResponse) ([]byte, error) {
	var builder strings.Builder
	writer := csv.NewWriter(&builder)
	rows := [][]string{
		{"created_at", "source", "action", "category", "user_id", "actor_user_id", "email_hint", "ip_hash", "target_type", "target_id", "metadata"},
	}
	for _, event := range response.Events {
		rows = append(rows, []string{
			event.CreatedAt.UTC().Format(time.RFC3339),
			event.Source,
			string(event.Action),
			event.Category,
			event.UserID,
			event.ActorUserID,
			event.EmailHint,
			event.IPHash,
			event.TargetType,
			event.TargetID,
			mustJSON(event.Metadata),
		})
	}
	if err := writer.WriteAll(rows); err != nil {
		return nil, err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return []byte(builder.String()), nil
}

func normalizeSecurityTimelineFilter(filter model.SecurityTimelineFilter, now time.Time) (model.SecurityTimelineFilter, time.Time, time.Time, error) {
	filter.UserID = strings.TrimSpace(filter.UserID)
	filter.Email = strings.ToLower(strings.TrimSpace(filter.Email))
	filter.IPHash = normalizeSecurityTimelineHash(filter.IPHash)
	filter.Action = strings.TrimSpace(filter.Action)
	if filter.Limit <= 0 || filter.Limit > maxSecurityTimelineLimit {
		filter.Limit = defaultSecurityTimelineLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	until := now
	if parsed, err := parseSecurityTimelineTime(filter.Until); err == nil {
		until = parsed
	}
	since := until.Add(-defaultSecurityTimelineLookback)
	if parsed, err := parseSecurityTimelineTime(filter.Since); err == nil {
		since = parsed
	}
	if since.After(until) {
		return filter, time.Time{}, time.Time{}, fmt.Errorf("timeline since must be before until")
	}
	filter.Since = since.Format(time.RFC3339)
	filter.Until = until.Format(time.RFC3339)
	return filter, since, until, nil
}

func parseSecurityTimelineTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty")
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func normalizeSecurityTimelineHash(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.TrimPrefix(raw, "sha256:")
	return raw
}

func (s *SecurityTimelineService) resolveTimelineUserID(ctx context.Context, email string) (*uuid.UUID, error) {
	if strings.TrimSpace(email) == "" || s == nil || s.users == nil {
		return nil, nil
	}
	user, err := s.users.GetByEmail(ctx, email)
	if err != nil || user == nil {
		return nil, err
	}
	return &user.ID, nil
}

func securityTimelineMatchesLog(log model.AuditLog, filter model.SecurityTimelineFilter, resolvedUserID *uuid.UUID) bool {
	if filter.IPHash != "" && securityTimelineIPHash(log.IP) != filter.IPHash {
		return false
	}
	if resolvedUserID == nil && filter.UserID != "" {
		return false
	}
	if filter.Email != "" && resolvedUserID == nil {
		email := strings.ToLower(strings.TrimSpace(metadataString(log.Metadata, "email")))
		if email != filter.Email {
			return false
		}
	}
	return true
}

func securityTimelineEventFromAuditLog(log model.AuditLog) (model.SecurityTimelineEvent, bool) {
	category := securityTimelineCategory(log)
	if category == "" {
		return model.SecurityTimelineEvent{}, false
	}
	event := model.SecurityTimelineEvent{
		ID:         log.ID.String(),
		Source:     "ce_audit",
		Action:     log.EventType,
		Category:   category,
		IPHash:     securityTimelineIPHash(log.IP),
		TargetType: log.TargetType,
		TargetID:   log.TargetID,
		Metadata:   sanitizeAuditMetadata(log.Metadata),
		CreatedAt:  log.CreatedAt.UTC(),
	}
	if log.ActorUserID != nil {
		event.ActorUserID = log.ActorUserID.String()
		event.UserID = log.ActorUserID.String()
	}
	if log.TargetType == "user" && strings.TrimSpace(log.TargetID) != "" {
		event.UserID = strings.TrimSpace(log.TargetID)
	}
	if email := strings.TrimSpace(metadataString(log.Metadata, "email")); email != "" {
		event.EmailHint = securityTimelineEmailHint(email)
	}
	return event, true
}

func securityTimelineCategory(log model.AuditLog) string {
	switch log.EventType {
	case model.AuditEventLoginFailure:
		return "auth_failure"
	case model.AuditEventTwoFactorChallengeFailure, model.AuditEventTwoFactorChallengeSuccess:
		if strings.EqualFold(metadataString(log.Metadata, "flow"), "step_up") {
			return "step_up"
		}
		return "auth"
	case model.AuditEventTwoFactorEnable, model.AuditEventTwoFactorDisable:
		return "step_up"
	case model.AuditEventPasskeyAdded, model.AuditEventPasskeyDeleted:
		return "passkey_change"
	case model.AuditEventPrivacyExport,
		model.AuditEventAccountDeletionRequest,
		model.AuditEventAccountDeletionResult,
		model.AuditEventAccountErasureJobCreated,
		model.AuditEventAccountErasureJobStarted,
		model.AuditEventAccountErasureJobFinished,
		model.AuditEventAccountErasureJobFailed:
		return "account_lifecycle"
	case model.AuditEventAdminSecurityTimelineRead,
		model.AuditEventAdminQuotaRecalculate:
		return "admin_investigation"
	case model.AuditEventLoginSuccess:
		return "auth"
	default:
		return ""
	}
}

func summarizeSecurityTimeline(events []model.SecurityTimelineEvent) model.SecurityTimelineSummary {
	summary := model.SecurityTimelineSummary{
		TotalEvents: int64(len(events)),
		Actions:     []model.AuditEventTypeCount{},
	}
	counts := make(map[model.AuditEventType]int64)
	for _, event := range events {
		counts[event.Action]++
		switch event.Category {
		case "auth_failure":
			summary.AuthFailures++
		case "step_up":
			summary.StepUpEvents++
		case "passkey_change":
			summary.PasskeyChanges++
		case "account_lifecycle":
			summary.AccountDeletionOrExport++
		}
	}
	for action, count := range counts {
		summary.Actions = append(summary.Actions, model.AuditEventTypeCount{
			EventType: action,
			Count:     count,
		})
	}
	return summary
}

func securityTimelineIPHash(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:])
}

func securityTimelineEmailHint(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return "email:redacted"
	}
	sum := sha256.Sum256([]byte(email))
	return "email:" + hex.EncodeToString(sum[:])[:10] + "@" + email[at+1:]
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key]
	return strings.TrimSpace(fmt.Sprint(value))
}

func mustJSON(value any) string {
	if value == nil {
		return "{}"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}
