package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/historysync/hsync-server/pkg/model"
)

const (
	defaultSupportAuditLimit = int32(20)
	maxSupportAuditLimit     = int32(50)
)

type supportUserStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
	GetByEmail(ctx context.Context, email string) (*model.User, error)
}

type supportUserTombstoneStore interface {
	GetAnyByID(ctx context.Context, id uuid.UUID) (*model.User, error)
}

type supportDeviceStore interface {
	ListByUser(ctx context.Context, userID uuid.UUID) ([]model.Device, error)
}

type supportQuotaStore interface {
	GetUsage(ctx context.Context, userID uuid.UUID) (*model.QuotaUsage, error)
	GetLimits(ctx context.Context, userID uuid.UUID) (*model.QuotaLimits, error)
}

type supportAuditStore interface {
	ListVisibleByUser(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AuditLog, error)
}

type supportErasureJobStore interface {
	ListByUser(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AccountErasureJob, error)
}

type supportTimelineStore interface {
	Lookup(ctx context.Context, filter model.SecurityTimelineFilter) (*model.SecurityTimelineResponse, error)
}

var ErrSupportContextInvalidLookup = errors.New("support context lookup requires user_id or email")

var ErrSupportContextLookupMismatch = errors.New("support context lookup conditions refer to different users")

type SupportContextDeps struct {
	Users       supportUserStore
	Devices     supportDeviceStore
	Quota       supportQuotaStore
	Audit       supportAuditStore
	ErasureJobs supportErasureJobStore
	Timeline    supportTimelineStore
}

type SupportContextService struct {
	users       supportUserStore
	devices     supportDeviceStore
	quota       supportQuotaStore
	audit       supportAuditStore
	erasureJobs supportErasureJobStore
	timeline    supportTimelineStore
}

type SupportContextLookup struct {
	UserID string
	Email  string
	Limit  int32
}

type SupportBaseContext struct {
	GeneratedAt      time.Time                      `json:"generated_at"`
	Lookup           SupportContextLookup           `json:"lookup"`
	User             *model.User                    `json:"user,omitempty"`
	Devices          []SupportDeviceSummary         `json:"devices"`
	Quota            *SupportQuotaSummary           `json:"quota,omitempty"`
	ErasureJobs      []SupportErasureJob            `json:"erasure_jobs"`
	RecentAudit      []SupportAuditSummary          `json:"recent_audit"`
	IncidentTimeline *model.SecurityTimelineResponse `json:"incident_timeline,omitempty"`
	RecentActions    []SupportIncidentAction        `json:"recent_actions"`
	ErasureStatus    *SupportErasureStatus          `json:"erasure_status,omitempty"`
	JobStatus        []SupportJobStatus             `json:"job_status"`
	AccountChanges   []SupportAccountChange         `json:"account_changes"`
}

type SupportErasureJob struct {
	ID          uuid.UUID                     `json:"id"`
	UserID      uuid.UUID                     `json:"user_id"`
	RequestedAt time.Time                     `json:"requested_at"`
	EligibleAt  time.Time                     `json:"eligible_at"`
	Status      model.AccountErasureJobStatus `json:"status"`
	Summary     map[string]any                `json:"summary,omitempty"`
	LastError   string                        `json:"last_error,omitempty"`
	StartedAt   *time.Time                    `json:"started_at,omitempty"`
	FinishedAt  *time.Time                    `json:"finished_at,omitempty"`
	UpdatedAt   time.Time                     `json:"updated_at"`
}

type SupportDeviceSummary struct {
	ID         uuid.UUID  `json:"id"`
	DeviceUUID uuid.UUID  `json:"device_uuid"`
	DeviceName string     `json:"device_name"`
	Platform   string     `json:"platform"`
	AppVersion string     `json:"app_version"`
	LastSyncAt *time.Time `json:"last_sync_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type SupportQuotaSummary struct {
	Usage          model.QuotaUsage   `json:"usage"`
	EffectiveLimit model.QuotaLimits  `json:"effective_limit"`
	Override       *model.QuotaLimits `json:"override,omitempty"`
}

type SupportAuditSummary struct {
	ID         uuid.UUID            `json:"id"`
	EventType  model.AuditEventType `json:"event_type"`
	TargetType string               `json:"target_type"`
	TargetID   string               `json:"target_id"`
	Metadata   map[string]any       `json:"metadata,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
}

type SupportIncidentAction struct {
	Action     model.AuditEventType `json:"action"`
	Category   string               `json:"category"`
	Source     string               `json:"source"`
	TargetType string               `json:"target_type,omitempty"`
	TargetID   string               `json:"target_id,omitempty"`
	Metadata   map[string]any       `json:"metadata,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
}

type SupportErasureStatus struct {
	Requested       bool                          `json:"requested"`
	InProgress      bool                          `json:"in_progress"`
	Completed       bool                          `json:"completed"`
	LatestStatus    model.AccountErasureJobStatus `json:"latest_status,omitempty"`
	LatestUpdatedAt *time.Time                    `json:"latest_updated_at,omitempty"`
	LatestEligibleAt *time.Time                   `json:"latest_eligible_at,omitempty"`
	LatestFinishedAt *time.Time                   `json:"latest_finished_at,omitempty"`
	LatestError     string                        `json:"latest_error,omitempty"`
}

type SupportJobStatus struct {
	Name      string         `json:"name"`
	Source    string         `json:"source"`
	Status    string         `json:"status"`
	UpdatedAt time.Time      `json:"updated_at"`
	Detail    string         `json:"detail,omitempty"`
	Summary   map[string]any `json:"summary,omitempty"`
}

type SupportAccountChange struct {
	Action    model.AuditEventType `json:"action"`
	Category  string               `json:"category"`
	Source    string               `json:"source"`
	Metadata  map[string]any       `json:"metadata,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
}

func NewSupportContextService(deps SupportContextDeps) *SupportContextService {
	return &SupportContextService{
		users:       deps.Users,
		devices:     deps.Devices,
		quota:       deps.Quota,
		audit:       deps.Audit,
		erasureJobs: deps.ErasureJobs,
		timeline:    deps.Timeline,
	}
}

func (s *SupportContextService) Lookup(ctx context.Context, lookup SupportContextLookup) (*SupportBaseContext, error) {
	if s == nil || s.users == nil {
		return nil, ErrUserNotFound
	}
	lookup.UserID = strings.TrimSpace(lookup.UserID)
	lookup.Email = strings.TrimSpace(lookup.Email)
	lookup.Limit = normalizeSupportContextLimit(lookup.Limit)
	if lookup.UserID == "" && lookup.Email == "" {
		return nil, ErrSupportContextInvalidLookup
	}

	user, err := s.lookupUser(ctx, lookup)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return &SupportBaseContext{
			GeneratedAt:    time.Now().UTC(),
			Lookup:         lookup,
			Devices:        []SupportDeviceSummary{},
			ErasureJobs:    []SupportErasureJob{},
			RecentAudit:    []SupportAuditSummary{},
			RecentActions:  []SupportIncidentAction{},
			JobStatus:      []SupportJobStatus{},
			AccountChanges: []SupportAccountChange{},
		}, nil
	}

	out := &SupportBaseContext{
		GeneratedAt:    time.Now().UTC(),
		Lookup:         lookup,
		User:           user,
		Devices:        []SupportDeviceSummary{},
		ErasureJobs:    []SupportErasureJob{},
		RecentAudit:    []SupportAuditSummary{},
		RecentActions:  []SupportIncidentAction{},
		JobStatus:      []SupportJobStatus{},
		AccountChanges: []SupportAccountChange{},
	}
	if s.devices != nil {
		devices, err := s.devices.ListByUser(ctx, user.ID)
		if err != nil {
			return nil, fmt.Errorf("list support devices: %w", err)
		}
		out.Devices = summarizeSupportDevices(devices)
	}
	if s.quota != nil {
		quota, err := s.supportQuota(ctx, user)
		if err != nil {
			return nil, err
		}
		out.Quota = quota
	}
	if s.audit != nil {
		logs, err := s.audit.ListVisibleByUser(ctx, user.ID, lookup.Limit)
		if err != nil {
			return nil, fmt.Errorf("list support audit: %w", err)
		}
		out.RecentAudit = summarizeSupportAudit(logs)
	}
	if s.erasureJobs != nil {
		jobs, err := s.erasureJobs.ListByUser(ctx, user.ID, lookup.Limit)
		if err != nil {
			return nil, fmt.Errorf("list support erasure jobs: %w", err)
		}
		out.ErasureJobs = summarizeSupportErasureJobs(jobs)
	}
	if err := s.populateIncidentView(ctx, out, user); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SupportContextService) lookupUser(ctx context.Context, lookup SupportContextLookup) (*model.User, error) {
	var byID *model.User
	if lookup.UserID != "" {
		userID, err := uuid.Parse(lookup.UserID)
		if err != nil {
			return nil, fmt.Errorf("parse user id: %w", err)
		}
		byID, err = s.users.GetByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("get support user by id: %w", err)
		}
		if byID == nil {
			if tombstones, ok := s.users.(supportUserTombstoneStore); ok {
				byID, err = tombstones.GetAnyByID(ctx, userID)
				if err != nil {
					return nil, fmt.Errorf("get support user tombstone by id: %w", err)
				}
			}
		}
	}
	var byEmail *model.User
	if lookup.Email != "" {
		user, err := s.users.GetByEmail(ctx, lookup.Email)
		if err != nil {
			return nil, fmt.Errorf("get support user by email: %w", err)
		}
		byEmail = user
	}
	if byID != nil && byEmail != nil && byID.ID != byEmail.ID {
		return nil, ErrSupportContextLookupMismatch
	}
	if byID != nil {
		return byID, nil
	}
	return byEmail, nil
}

func (s *SupportContextService) supportQuota(ctx context.Context, user *model.User) (*SupportQuotaSummary, error) {
	usage, err := s.quota.GetUsage(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("get support quota usage: %w", err)
	}
	limits := model.TierLimits(user.Tier)
	limits.UserID = user.ID
	override, err := s.quota.GetLimits(ctx, user.ID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("get support quota limits: %w", err)
		}
	} else if override != nil {
		limits = *override
	}
	return &SupportQuotaSummary{
		Usage:          *usage,
		EffectiveLimit: limits,
		Override:       override,
	}, nil
}

func normalizeSupportContextLimit(limit int32) int32 {
	if limit <= 0 {
		return defaultSupportAuditLimit
	}
	if limit > maxSupportAuditLimit {
		return maxSupportAuditLimit
	}
	return limit
}

func summarizeSupportDevices(devices []model.Device) []SupportDeviceSummary {
	out := make([]SupportDeviceSummary, 0, len(devices))
	for _, device := range devices {
		out = append(out, SupportDeviceSummary{
			ID:         device.ID,
			DeviceUUID: device.DeviceUUID,
			DeviceName: device.DeviceName,
			Platform:   device.Platform,
			AppVersion: device.AppVersion,
			LastSyncAt: device.LastSyncAt,
			RevokedAt:  device.RevokedAt,
			CreatedAt:  device.CreatedAt,
		})
	}
	return out
}

func summarizeSupportAudit(logs []model.AuditLog) []SupportAuditSummary {
	out := make([]SupportAuditSummary, 0, len(logs))
	for _, log := range logs {
		out = append(out, SupportAuditSummary{
			ID:         log.ID,
			EventType:  log.EventType,
			TargetType: log.TargetType,
			TargetID:   log.TargetID,
			Metadata:   sanitizeAuditMetadata(log.Metadata),
			CreatedAt:  log.CreatedAt,
		})
	}
	return out
}

func summarizeSupportErasureJobs(jobs []model.AccountErasureJob) []SupportErasureJob {
	out := make([]SupportErasureJob, 0, len(jobs))
	for _, job := range jobs {
		summary := map[string]any{}
		if len(job.Summary) > 0 {
			_ = json.Unmarshal(job.Summary, &summary)
		}
		out = append(out, SupportErasureJob{
			ID:          job.ID,
			UserID:      job.UserID,
			RequestedAt: job.RequestedAt,
			EligibleAt:  job.EligibleAt,
			Status:      job.Status,
			Summary:     summary,
			LastError:   job.LastError,
			StartedAt:   job.StartedAt,
			FinishedAt:  job.FinishedAt,
			UpdatedAt:   job.UpdatedAt,
		})
	}
	return out
}

func (s *SupportContextService) populateIncidentView(ctx context.Context, out *SupportBaseContext, user *model.User) error {
	if out == nil || user == nil {
		return nil
	}
	out.ErasureStatus = summarizeSupportErasureStatus(out.ErasureJobs)
	out.JobStatus = summarizeSupportJobStatus(out.ErasureJobs)
	if s.timeline == nil {
		return nil
	}
	timeline, err := s.timeline.Lookup(ctx, model.SecurityTimelineFilter{
		UserID: user.ID.String(),
		Email:  user.Email,
		Limit:  int32(maxInt(int(out.Lookup.Limit), int(defaultSecurityTimelineLimit))),
	})
	if err != nil {
		return fmt.Errorf("load support incident timeline: %w", err)
	}
	out.IncidentTimeline = timeline
	out.RecentActions = summarizeSupportIncidentActions(timeline.Events, 5)
	out.AccountChanges = summarizeSupportAccountChanges(timeline.Events)
	return nil
}

func summarizeSupportIncidentActions(events []model.SecurityTimelineEvent, limit int) []SupportIncidentAction {
	if len(events) == 0 || limit <= 0 {
		return []SupportIncidentAction{}
	}
	if limit > len(events) {
		limit = len(events)
	}
	out := make([]SupportIncidentAction, 0, limit)
	for _, event := range events[:limit] {
		out = append(out, SupportIncidentAction{
			Action:     event.Action,
			Category:   event.Category,
			Source:     event.Source,
			TargetType: event.TargetType,
			TargetID:   event.TargetID,
			Metadata:   sanitizeAuditMetadata(event.Metadata),
			CreatedAt:  event.CreatedAt,
		})
	}
	return out
}

func summarizeSupportErasureStatus(jobs []SupportErasureJob) *SupportErasureStatus {
	status := &SupportErasureStatus{}
	if len(jobs) == 0 {
		return status
	}
	status.Requested = true
	latest := jobs[0]
	status.LatestStatus = latest.Status
	status.LatestUpdatedAt = &latest.UpdatedAt
	status.LatestEligibleAt = &latest.EligibleAt
	status.LatestFinishedAt = latest.FinishedAt
	status.LatestError = latest.LastError
	for _, job := range jobs {
		switch job.Status {
		case model.AccountErasureJobStatusRunning, model.AccountErasureJobStatusPending:
			status.InProgress = true
		case model.AccountErasureJobStatusCompleted:
			status.Completed = true
		}
	}
	return status
}

func summarizeSupportJobStatus(jobs []SupportErasureJob) []SupportJobStatus {
	out := make([]SupportJobStatus, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, SupportJobStatus{
			Name:      "account_erasure",
			Source:    "ce_erasure",
			Status:    string(job.Status),
			UpdatedAt: job.UpdatedAt,
			Detail:    strings.TrimSpace(job.LastError),
			Summary:   job.Summary,
		})
	}
	return out
}

func summarizeSupportAccountChanges(events []model.SecurityTimelineEvent) []SupportAccountChange {
	out := []SupportAccountChange{}
	for _, event := range events {
		switch event.Category {
		case "account_lifecycle", "passkey_change", "step_up":
			out = append(out, SupportAccountChange{
				Action:    event.Action,
				Category:  event.Category,
				Source:    event.Source,
				Metadata:  sanitizeAuditMetadata(event.Metadata),
				CreatedAt: event.CreatedAt,
			})
		}
	}
	return out
}

func maxInt(first, second int) int {
	if first > second {
		return first
	}
	return second
}
