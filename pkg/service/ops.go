package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/historysync/hsync-server/pkg/buildinfo"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/storage"
	"github.com/rs/zerolog/log"
)

const (
	OpsStatusOK            = "ok"
	OpsStatusDegraded      = "degraded"
	OpsStatusUnhealthy     = "unhealthy"
	OpsStatusDisabled      = "disabled"
	OpsStatusNotConfigured = "not_configured"
	OpsStatusSkipped       = "skipped"
	OpsStatusNotChecked    = "not_checked"
)

const (
	DefaultOpsConsistencyLimit int32 = 1000
	maxOpsConsistencyIssues          = 50
)

// PingFunc verifies one runtime dependency without exposing the concrete client.
type PingFunc func(ctx context.Context) error

type opsBundleMetadataStore interface {
	CountAll(ctx context.Context) (int64, error)
	SumSizeAll(ctx context.Context) (int64, error)
	ListForOpsConsistency(ctx context.Context, limit int32) ([]model.BundleMeta, error)
}

type opsSnapshotMetadataStore interface {
	CountAll(ctx context.Context) (int64, error)
	SumSizeAll(ctx context.Context) (int64, error)
	ListForOpsConsistency(ctx context.Context, limit int32) ([]model.SnapshotMeta, error)
}

type opsHistoryStore interface {
	Create(ctx context.Context, run *model.OpsCheckRun) error
	ListRecent(ctx context.Context, limit int32) ([]model.OpsCheckRun, error)
	ListRecentFailures(ctx context.Context, limit int32) ([]model.OpsCheckRun, error)
}

type OpsAlertConfig struct {
	Email         string
	WebhookURL    string
	WebhookSecret string
	AppName       string
}

// OpsDeps holds the runtime dependencies used by the self-hosted operations
// surface. The active probes are intentionally dependency-injected so tests and
// Enterprise wrappers can provide narrow fakes.
type OpsDeps struct {
	Config           *config.Config
	BuildInfo        buildinfo.Info
	Repos            *repository.Repos
	BlobStore        storage.BlobStorage
	DatabasePing     PingFunc
	RedisPing        PingFunc
	BundleMetadata   opsBundleMetadataStore
	SnapshotMetadata opsSnapshotMetadataStore
	History          opsHistoryStore
	Alert            OpsAlertConfig
	Notifier         provider.Notifier
	Webhook          provider.WebhookProvider
	Now              func() time.Time
}

// OpsService builds operator-facing summaries and dependency probes.
type OpsService struct {
	cfg          *config.Config
	buildInfo    buildinfo.Info
	repos        *repository.Repos
	blobStore    storage.BlobStorage
	databasePing PingFunc
	redisPing    PingFunc
	bundles      opsBundleMetadataStore
	snapshots    opsSnapshotMetadataStore
	history      opsHistoryStore
	alert        OpsAlertConfig
	notifier     provider.Notifier
	webhook      provider.WebhookProvider
	now          func() time.Time

	mu              sync.Mutex
	lastDependency  *OpsDependencyReport
	lastConsistency *OpsConsistencyReport
}

func NewOpsService(deps OpsDeps) *OpsService {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	bundles := deps.BundleMetadata
	snapshots := deps.SnapshotMetadata
	if deps.Repos != nil {
		if bundles == nil {
			bundles = deps.Repos.Bundles
		}
		if snapshots == nil {
			snapshots = deps.Repos.Snapshots
		}
		if deps.History == nil {
			deps.History = deps.Repos.OpsHistory
		}
	}
	return &OpsService{
		cfg:          deps.Config,
		buildInfo:    normalizeBuildInfo(deps.BuildInfo),
		repos:        deps.Repos,
		blobStore:    deps.BlobStore,
		databasePing: deps.DatabasePing,
		redisPing:    deps.RedisPing,
		bundles:      bundles,
		snapshots:    snapshots,
		history:      deps.History,
		alert:        deps.Alert,
		notifier:     deps.Notifier,
		webhook:      deps.Webhook,
		now:          now,
	}
}

func normalizeBuildInfo(info buildinfo.Info) buildinfo.Info {
	if info.Version == "" && info.Commit == "" && info.BuildTime == "" && info.Edition == "" && info.SchemaVersion == 0 {
		return buildinfo.Current()
	}
	if info.SchemaVersion == 0 {
		info.SchemaVersion = buildinfo.LatestSchemaVersion()
	}
	return info
}

type MaskedValue struct {
	Set   bool   `json:"set"`
	Value string `json:"value,omitempty"`
}

type OpsSummary struct {
	GeneratedAt time.Time           `json:"generated_at"`
	BuildInfo   buildinfo.Info      `json:"build_info"`
	Config      OpsConfigSummary    `json:"config"`
	Readiness   OpsReadinessSummary `json:"readiness"`
	Backup      OpsBackupGuidance   `json:"backup"`
}

type OpsConfigSummary struct {
	Server       map[string]any `json:"server"`
	Database     map[string]any `json:"database"`
	Redis        map[string]any `json:"redis"`
	Storage      map[string]any `json:"storage"`
	Security     map[string]any `json:"security"`
	Features     map[string]any `json:"features"`
	Notification map[string]any `json:"notification"`
}

type OpsReadinessSummary struct {
	LastDependencyCheck  *OpsDependencyReport  `json:"last_dependency_check,omitempty"`
	LastConsistencyCheck *OpsConsistencyReport `json:"last_consistency_check,omitempty"`
	RecentRuns           []model.OpsCheckRun   `json:"recent_runs"`
	RecentFailures       []model.OpsCheckRun   `json:"recent_failures"`
}

type OpsBackupGuidance struct {
	ZeroKnowledgeBoundary string               `json:"zero_knowledge_boundary"`
	Components            []OpsBackupComponent `json:"components"`
}

type OpsBackupComponent struct {
	Name             string     `json:"name"`
	Required         bool       `json:"required"`
	Role             string     `json:"role"`
	BackupGuidance   string     `json:"backup_guidance"`
	RestoreGuidance  string     `json:"restore_guidance"`
	LastCheckStatus  string     `json:"last_check_status"`
	LastCheckedAt    *time.Time `json:"last_checked_at,omitempty"`
	OperatorAction   string     `json:"operator_action,omitempty"`
	ZeroKnowledge    bool       `json:"zero_knowledge,omitempty"`
	ConsistencyScope string     `json:"consistency_scope,omitempty"`
}

type OpsDependencyReport struct {
	Overall        string               `json:"overall"`
	CheckedAt      time.Time            `json:"checked_at"`
	DurationMillis int64                `json:"duration_millis"`
	Checks         []OpsDependencyCheck `json:"checks"`
}

type OpsDependencyCheck struct {
	Name           string `json:"name"`
	Required       bool   `json:"required"`
	Status         string `json:"status"`
	DurationMillis int64  `json:"duration_millis"`
	Message        string `json:"message"`
	Action         string `json:"action,omitempty"`
	ErrorClass     string `json:"error_class,omitempty"`
}

type OpsConsistencyReport struct {
	Overall               string                   `json:"overall"`
	CheckedAt             time.Time                `json:"checked_at"`
	DurationMillis        int64                    `json:"duration_millis"`
	SampleLimit           int32                    `json:"sample_limit"`
	ZeroKnowledgeBoundary string                   `json:"zero_knowledge_boundary"`
	Artifacts             []OpsArtifactConsistency `json:"artifacts"`
	Issues                []OpsConsistencyIssue    `json:"issues,omitempty"`
}

type OpsArtifactConsistency struct {
	Kind                 string `json:"kind"`
	Status               string `json:"status"`
	MetadataTotal        int64  `json:"metadata_total"`
	MetadataBytes        int64  `json:"metadata_bytes"`
	CheckedMetadata      int    `json:"checked_metadata"`
	StorageObjectCount   int    `json:"storage_object_count"`
	StorageBytes         int64  `json:"storage_bytes"`
	MetadataTruncated    bool   `json:"metadata_truncated"`
	StorageListTruncated bool   `json:"storage_list_truncated"`
	MissingObjects       int    `json:"missing_objects"`
	SizeMismatches       int    `json:"size_mismatches"`
	CheckFailures        int    `json:"check_failures"`
	IssueCount           int    `json:"issue_count"`
	Message              string `json:"message"`
	Action               string `json:"action,omitempty"`
	ErrorClass           string `json:"error_class,omitempty"`
}

type OpsConsistencyIssue struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Key          string `json:"key"`
	Status       string `json:"status"`
	Message      string `json:"message"`
	Action       string `json:"action"`
	ExpectedSize *int64 `json:"expected_size,omitempty"`
	ActualSize   *int64 `json:"actual_size,omitempty"`
	ErrorClass   string `json:"error_class,omitempty"`
}

type OpsHistoryView struct {
	RecentRuns     []model.OpsCheckRun `json:"recent_runs"`
	RecentFailures []model.OpsCheckRun `json:"recent_failures"`
}

func (s *OpsService) Summary(ctx context.Context) OpsSummary {
	last := s.lastDependencyReport()
	lastConsistency := s.lastConsistencyReport()
	history, err := s.History(ctx, 10)
	if err != nil {
		log.Warn().Err(err).Msg("load ops check history failed")
		history = OpsHistoryView{RecentRuns: []model.OpsCheckRun{}, RecentFailures: []model.OpsCheckRun{}}
	}
	return OpsSummary{
		GeneratedAt: s.now().UTC(),
		BuildInfo:   s.buildInfo,
		Config:      s.configSummary(),
		Readiness: OpsReadinessSummary{
			LastDependencyCheck:  last,
			LastConsistencyCheck: lastConsistency,
			RecentRuns:           history.RecentRuns,
			RecentFailures:       history.RecentFailures,
		},
		Backup: s.backupGuidance(last),
	}
}

func (s *OpsService) CheckDependencies(ctx context.Context) OpsDependencyReport {
	start := s.now()
	checks := make([]OpsDependencyCheck, 0, 4)
	checks = append(checks, s.checkPostgres(ctx))
	checks = append(checks, s.checkRedis(ctx))
	storageCheck := s.checkStorageList(ctx)
	checks = append(checks, storageCheck)
	checks = append(checks, s.checkStorageProbe(ctx, storageCheck.Status))

	report := OpsDependencyReport{
		Overall:        opsOverall(checks),
		CheckedAt:      start.UTC(),
		DurationMillis: millisSince(start, s.now),
		Checks:         checks,
	}

	s.mu.Lock()
	cp := cloneDependencyReport(report)
	s.lastDependency = &cp
	s.mu.Unlock()

	s.persistDependencyRun(ctx, report)
	return report
}

func (s *OpsService) CheckConsistency(ctx context.Context, limit int32) OpsConsistencyReport {
	if limit <= 0 || limit > DefaultOpsConsistencyLimit {
		limit = DefaultOpsConsistencyLimit
	}
	start := s.now()
	report := OpsConsistencyReport{
		Overall:               OpsStatusOK,
		CheckedAt:             start.UTC(),
		SampleLimit:           limit,
		ZeroKnowledgeBoundary: "This check compares metadata rows to object existence and size only; it never downloads, parses, or decrypts real bundle or snapshot blobs.",
	}

	bundleArtifact, bundleIssues := s.checkBundleConsistency(ctx, limit)
	snapshotArtifact, snapshotIssues := s.checkSnapshotConsistency(ctx, limit)
	report.Artifacts = []OpsArtifactConsistency{bundleArtifact, snapshotArtifact}
	report.Issues = append(report.Issues, bundleIssues...)
	report.Issues = append(report.Issues, snapshotIssues...)
	report.Overall = consistencyOverall(report.Artifacts)
	report.DurationMillis = millisSince(start, s.now)

	s.mu.Lock()
	cp := cloneConsistencyReport(report)
	s.lastConsistency = &cp
	s.mu.Unlock()

	s.persistConsistencyRun(ctx, report)
	return report
}

func (s *OpsService) RunScheduledDependencyCheck(ctx context.Context) {
	report := s.CheckDependencies(ctx)
	s.notifyOpsFailure(ctx, model.OpsRunTypeDependency, report.Overall, report.DurationMillis, dependencyAlertSummary(report))
}

func (s *OpsService) RunScheduledConsistencyCheck(ctx context.Context, limit int32) {
	report := s.CheckConsistency(ctx, limit)
	s.notifyOpsFailure(ctx, model.OpsRunTypeConsistency, report.Overall, report.DurationMillis, consistencyAlertSummary(report))
}

func (s *OpsService) History(ctx context.Context, limit int32) (OpsHistoryView, error) {
	view := OpsHistoryView{
		RecentRuns:     []model.OpsCheckRun{},
		RecentFailures: []model.OpsCheckRun{},
	}
	if s == nil || s.history == nil {
		return view, nil
	}
	recent, err := s.history.ListRecent(ctx, limit)
	if err != nil {
		return view, err
	}
	failures, err := s.history.ListRecentFailures(ctx, limit)
	if err != nil {
		return view, err
	}
	view.RecentRuns = recent
	view.RecentFailures = failures
	return view, nil
}

func (s *OpsService) notifyOpsFailure(ctx context.Context, runType model.OpsRunType, overall string, durationMillis int64, summary map[string]any) {
	if s == nil || overall == OpsStatusOK {
		return
	}
	subject := fmt.Sprintf("HistorySync ops %s check %s", runType, overall)
	message := fmt.Sprintf("The scheduled %s ops check completed with status %s.", runType, overall)
	data := map[string]any{
		"run_type":        string(runType),
		"overall_status":  overall,
		"duration_millis": durationMillis,
		"summary":         summary,
	}
	if strings.TrimSpace(s.alert.Email) != "" && s.notifier != nil && s.notifier.DeliveryEnabled() {
		if err := s.notifier.SendNotification(ctx, provider.NotificationParams{
			UserID:      "ops",
			Email:       strings.TrimSpace(s.alert.Email),
			DisplayName: "HistorySync operator",
			AppName:     fallbackOpsAlertAppName(s.alert.AppName),
			Category:    "ops",
			Type:        "ops." + string(runType) + ".failure",
			Subject:     subject,
			Message:     message,
		}); err != nil {
			log.Warn().Err(err).Str("run_type", string(runType)).Msg("ops email alert failed")
		}
	}
	if strings.TrimSpace(s.alert.WebhookURL) != "" && s.webhook != nil && s.webhook.DeliveryEnabled() {
		if err := s.webhook.Send(ctx, s.alert.WebhookURL, s.alert.WebhookSecret, provider.WebhookNotification{
			Type:     "ops." + string(runType) + ".failure",
			Category: "ops",
			Subject:  subject,
			Message:  message,
			Data:     data,
		}); err != nil {
			log.Warn().Err(err).Str("run_type", string(runType)).Msg("ops webhook alert failed")
		}
	}
}

func dependencyAlertSummary(report OpsDependencyReport) map[string]any {
	return map[string]any{
		"failures": dependencyFailureSummaries(report.Checks),
		"counts": map[string]any{
			"dependencies": len(report.Checks),
			"ok":           countDependencyStatus(report.Checks, OpsStatusOK),
			"degraded":     countDependencyStatus(report.Checks, OpsStatusDegraded),
			"unhealthy":    countDependencyStatus(report.Checks, OpsStatusUnhealthy),
		},
	}
}

func consistencyAlertSummary(report OpsConsistencyReport) map[string]any {
	return map[string]any{
		"issues": consistencyIssueSummaries(report.Issues),
		"counts": map[string]any{
			"artifacts":       len(report.Artifacts),
			"checked_rows":    sumCheckedMetadata(report.Artifacts),
			"missing_objects": sumMissingObjects(report.Artifacts),
			"size_mismatches": sumSizeMismatches(report.Artifacts),
			"check_failures":  sumCheckFailures(report.Artifacts),
		},
	}
}

func fallbackOpsAlertAppName(appName string) string {
	appName = strings.TrimSpace(appName)
	if appName == "" {
		return "HistorySync Cloud"
	}
	return appName
}

func (s *OpsService) persistDependencyRun(ctx context.Context, report OpsDependencyReport) {
	if s == nil || s.history == nil {
		return
	}
	run, err := dependencyHistoryRun(report)
	if err != nil {
		log.Warn().Err(err).Msg("build dependency check history failed")
		return
	}
	if err := s.history.Create(ctx, &run); err != nil {
		log.Warn().Err(err).Msg("persist dependency check history failed")
	}
}

func (s *OpsService) persistConsistencyRun(ctx context.Context, report OpsConsistencyReport) {
	if s == nil || s.history == nil {
		return
	}
	run, err := consistencyHistoryRun(report)
	if err != nil {
		log.Warn().Err(err).Msg("build consistency check history failed")
		return
	}
	if err := s.history.Create(ctx, &run); err != nil {
		log.Warn().Err(err).Msg("persist consistency check history failed")
	}
}

func dependencyHistoryRun(report OpsDependencyReport) (model.OpsCheckRun, error) {
	findings := map[string]any{
		"checks":   len(report.Checks),
		"failures": dependencyFailureSummaries(report.Checks),
	}
	counts := map[string]any{
		"dependencies": len(report.Checks),
		"ok":           countDependencyStatus(report.Checks, OpsStatusOK),
		"degraded":     countDependencyStatus(report.Checks, OpsStatusDegraded),
		"unhealthy":    countDependencyStatus(report.Checks, OpsStatusUnhealthy),
	}
	started := report.CheckedAt
	finished := started.Add(time.Duration(report.DurationMillis) * time.Millisecond)
	return buildOpsHistoryRun(model.OpsRunTypeDependency, report.Overall, started, finished, report.DurationMillis, findings, counts, report)
}

func consistencyHistoryRun(report OpsConsistencyReport) (model.OpsCheckRun, error) {
	findings := map[string]any{
		"issue_count": len(report.Issues),
		"issues":      consistencyIssueSummaries(report.Issues),
	}
	counts := map[string]any{
		"artifacts":       len(report.Artifacts),
		"checked_rows":    sumCheckedMetadata(report.Artifacts),
		"missing_objects": sumMissingObjects(report.Artifacts),
		"size_mismatches": sumSizeMismatches(report.Artifacts),
		"check_failures":  sumCheckFailures(report.Artifacts),
	}
	started := report.CheckedAt
	finished := started.Add(time.Duration(report.DurationMillis) * time.Millisecond)
	return buildOpsHistoryRun(model.OpsRunTypeConsistency, report.Overall, started, finished, report.DurationMillis, findings, counts, report)
}

func buildOpsHistoryRun(runType model.OpsRunType, overall string, started, finished time.Time, durationMillis int64, findings, counts map[string]any, report any) (model.OpsCheckRun, error) {
	findingsJSON, err := json.Marshal(findings)
	if err != nil {
		return model.OpsCheckRun{}, err
	}
	countsJSON, err := json.Marshal(counts)
	if err != nil {
		return model.OpsCheckRun{}, err
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return model.OpsCheckRun{}, err
	}
	return model.OpsCheckRun{
		RunType:            runType,
		OverallStatus:      overall,
		StartedAt:          started.UTC(),
		FinishedAt:         finished.UTC(),
		DurationMillis:     durationMillis,
		SummarizedFindings: json.RawMessage(findingsJSON),
		ArtifactCounts:     json.RawMessage(countsJSON),
		ReportJSON:         json.RawMessage(reportJSON),
	}, nil
}

func dependencyFailureSummaries(checks []OpsDependencyCheck) []map[string]any {
	failures := make([]map[string]any, 0)
	for _, check := range checks {
		if check.Status == OpsStatusOK || check.Status == OpsStatusDisabled {
			continue
		}
		failures = append(failures, map[string]any{
			"name":        check.Name,
			"status":      check.Status,
			"required":    check.Required,
			"error_class": check.ErrorClass,
			"message":     check.Message,
			"action":      check.Action,
		})
	}
	return failures
}

func consistencyIssueSummaries(issues []OpsConsistencyIssue) []map[string]any {
	summaries := make([]map[string]any, 0, len(issues))
	for _, issue := range issues {
		summaries = append(summaries, map[string]any{
			"kind":        issue.Kind,
			"id":          issue.ID,
			"status":      issue.Status,
			"error_class": issue.ErrorClass,
			"message":     issue.Message,
			"action":      issue.Action,
		})
	}
	return summaries
}

func countDependencyStatus(checks []OpsDependencyCheck, status string) int {
	var count int
	for _, check := range checks {
		if check.Status == status {
			count++
		}
	}
	return count
}

func sumCheckedMetadata(artifacts []OpsArtifactConsistency) int {
	var total int
	for _, artifact := range artifacts {
		total += artifact.CheckedMetadata
	}
	return total
}

func sumMissingObjects(artifacts []OpsArtifactConsistency) int {
	var total int
	for _, artifact := range artifacts {
		total += artifact.MissingObjects
	}
	return total
}

func sumSizeMismatches(artifacts []OpsArtifactConsistency) int {
	var total int
	for _, artifact := range artifacts {
		total += artifact.SizeMismatches
	}
	return total
}

func sumCheckFailures(artifacts []OpsArtifactConsistency) int {
	var total int
	for _, artifact := range artifacts {
		total += artifact.CheckFailures
	}
	return total
}

func (s *OpsService) configSummary() OpsConfigSummary {
	cfg := s.cfg
	if cfg == nil {
		return OpsConfigSummary{
			Server:       map[string]any{"configured": false},
			Database:     map[string]any{"configured": false},
			Redis:        map[string]any{"configured": false},
			Storage:      map[string]any{"configured": false},
			Security:     map[string]any{"configured": false},
			Features:     map[string]any{"configured": false},
			Notification: map[string]any{"configured": false},
		}
	}

	return OpsConfigSummary{
		Server: map[string]any{
			"listen_addr":       cfg.ListenAddr,
			"public_url":        cfg.PublicURL,
			"web_enabled":       cfg.WebEnabled,
			"web_console_path":  cfg.WebConsolePath,
			"web_support_email": cfg.WebSupportEmail,
			"metrics_enabled":   cfg.MetricsEnabled,
			"metrics_path":      cfg.MetricsPath,
		},
		Database: map[string]any{
			"database_url": redactConnectionURL(cfg.DatabaseURL),
		},
		Redis: map[string]any{
			"redis_url": redactConnectionURL(cfg.RedisURL),
			"optional":  true,
		},
		Storage: map[string]any{
			"s3_endpoint":   cfg.S3Endpoint,
			"s3_bucket":     cfg.S3Bucket,
			"s3_use_ssl":    cfg.S3UseSSL,
			"s3_access_key": maskSecret(cfg.S3AccessKey),
			"s3_secret_key": maskSecret(cfg.S3SecretKey),
		},
		Security: map[string]any{
			"admin_key":             maskSecret(cfg.AdminKey),
			"jwt_private_key":       maskSecret(cfg.JWTPrivateKey),
			"security_secret":       maskSecret(cfg.SecuritySecret),
			"stripe_secret_key":     maskSecret(cfg.StripeSecretKey),
			"stripe_webhook_secret": maskSecret(cfg.StripeWebhookSecret),
			"oidc_client_secret":    maskSecret(cfg.OIDCClientSecret),
			"turnstile_secret":      maskSecret(cfg.TurnstileSecret),
		},
		Features: map[string]any{
			"stripe_disabled":                cfg.StripeDisabled,
			"oidc_enabled":                   cfg.OIDCEnabled,
			"turnstile_enabled":              cfg.TurnstileEnabled,
			"background_tasks_enabled":       cfg.BackgroundTasksEnabled,
			"quota_reconcile_interval":       cfg.QuotaReconcileInterval.String(),
			"retention_cleanup_interval":     cfg.RetentionCleanupInterval.String(),
			"retention_grace_period":         cfg.RetentionGracePeriod.String(),
			"retention_dry_run":              cfg.RetentionDryRun,
			"history_retention_interval":     cfg.HistoryRetentionInterval.String(),
			"history_hot_retention":          cfg.HistoryHotRetention.String(),
			"history_archive_retention":      cfg.HistoryArchiveRetention.String(),
			"history_retention_dry_run":      cfg.HistoryRetentionDryRun,
			"notification_outbox_interval":   cfg.NotificationOutboxInterval.String(),
			"ops_dependency_check_interval":  cfg.OpsDependencyCheckInterval.String(),
			"ops_consistency_check_interval": cfg.OpsConsistencyCheckInterval.String(),
			"ops_consistency_check_limit":    cfg.OpsConsistencyCheckLimit,
			"options_file":                   cfg.OptionsFile,
		},
		Notification: map[string]any{
			"notifications_enabled":     cfg.NotificationsEnabled,
			"quota_warning_threshold":   cfg.QuotaWarningThreshold,
			"quota_exhausted_threshold": cfg.QuotaExhaustedThreshold,
			"email_verification_path":   cfg.EmailVerificationPath,
			"password_reset_path":       cfg.PasswordResetPath,
			"smtp_enabled":              cfg.SMTPEnabled,
			"smtp_server":               cfg.SMTPServer,
			"smtp_port":                 cfg.SMTPPort,
			"smtp_username":             cfg.SMTPUsername,
			"smtp_password":             maskSecret(cfg.SMTPPassword),
			"smtp_from":                 cfg.SMTPFrom,
			"smtp_from_name":            cfg.SMTPFromName,
			"smtp_tls_mode":             cfg.SMTPTLSMode,
			"ops_alert_email":           maskEmailAddress(cfg.OpsAlertEmail),
			"ops_alert_webhook_url":     maskWebhookURL(cfg.OpsAlertWebhookURL),
			"ops_alert_webhook_secret":  maskSecret(cfg.OpsAlertWebhookSecret),
		},
	}
}

func (s *OpsService) backupGuidance(last *OpsDependencyReport) OpsBackupGuidance {
	component := func(name string, required bool, role, backup, restore, action, scope string, zeroKnowledge bool, checkNames ...string) OpsBackupComponent {
		status, checkedAt := lastStatusFor(last, checkNames...)
		return OpsBackupComponent{
			Name:             name,
			Required:         required,
			Role:             role,
			BackupGuidance:   backup,
			RestoreGuidance:  restore,
			LastCheckStatus:  status,
			LastCheckedAt:    checkedAt,
			OperatorAction:   action,
			ZeroKnowledge:    zeroKnowledge,
			ConsistencyScope: scope,
		}
	}

	return OpsBackupGuidance{
		ZeroKnowledgeBoundary: "Bundle and snapshot payloads are opaque encrypted blobs; operations checks must never parse or decrypt their contents.",
		Components: []OpsBackupComponent{
			component(
				"postgresql",
				true,
				"Authoritative metadata store for users, devices, bundle indexes, snapshots, quota, audit logs, settings, and tokens.",
				"Back up the full PostgreSQL database with a consistent dump or volume snapshot, including migration state.",
				"Restore PostgreSQL before accepting writes, then verify metadata counts and readiness before reconnecting clients.",
				"Keep database_url credentials, schema migrations, and backup schedule documented outside the database.",
				"Readiness checks verify connectivity only; consistency checks compare metadata rows to object existence.",
				false,
				"postgresql",
			),
			component(
				"s3_bucket",
				true,
				"Stores immutable encrypted bundle and snapshot blobs referenced by PostgreSQL metadata.",
				"Back up the entire configured S3 bucket or replicate it with versioning/object-lock policy appropriate for the deployment.",
				"Restore objects before enabling uploads/downloads; metadata without matching objects means clients cannot retrieve history data.",
				"Ensure S3 credentials can list, write, read, stat, and delete objects in the configured bucket.",
				"Checks only list objects and probe operator-created test objects; real blob contents remain opaque.",
				true,
				"storage",
				"storage_probe",
			),
			component(
				"configuration_and_secrets",
				true,
				"Runtime configuration and signing/encryption secrets needed to boot the same service identity.",
				"Back up config files, deployment manifests, JWT private key, security_secret, admin key, S3 credentials, SMTP/OIDC secrets, and any external secret-manager entries.",
				"Restore secrets exactly; rotating JWT or encryption secrets can invalidate tokens or locally encrypted records.",
				"Keep a sealed recovery copy of secrets and the config source used to produce this summary.",
				"Configuration summary masks sensitive values and only proves whether they are set.",
				false,
			),
			component(
				"redis",
				false,
				"Optional cache and shared rate-limit backend; the CE server degrades to in-memory behavior when it is unavailable.",
				"Back up Redis only if operators rely on its runtime state; normal metadata/blob recovery does not depend on it.",
				"Redis may be started empty after a disaster; expect cache warmup and local rate-limit state reset.",
				"Configure redis_url when multiple server instances need shared limiting/cache behavior.",
				"Readiness checks ping Redis when configured; disabled Redis is acceptable for single-instance self-hosting.",
				false,
				"redis",
			),
		},
	}
}

func (s *OpsService) checkPostgres(ctx context.Context) OpsDependencyCheck {
	const name = "postgresql"
	if s.databasePing == nil {
		return OpsDependencyCheck{
			Name:     name,
			Required: true,
			Status:   OpsStatusNotConfigured,
			Message:  "PostgreSQL ping is not wired; metadata readiness cannot be verified from this process.",
			Action:   "Check database_url and server startup wiring.",
		}
	}
	start := s.now()
	if err := s.databasePing(ctx); err != nil {
		return OpsDependencyCheck{
			Name:           name,
			Required:       true,
			Status:         OpsStatusUnhealthy,
			DurationMillis: millisSince(start, s.now),
			Message:        "PostgreSQL is not reachable; metadata reads and writes will fail.",
			Action:         "Verify database_url, network reachability, credentials, migrations, and PostgreSQL health.",
			ErrorClass:     classifyOpsError(err),
		}
	}
	return OpsDependencyCheck{
		Name:           name,
		Required:       true,
		Status:         OpsStatusOK,
		DurationMillis: millisSince(start, s.now),
		Message:        "PostgreSQL ping succeeded; metadata store is reachable.",
	}
}

func (s *OpsService) checkRedis(ctx context.Context) OpsDependencyCheck {
	const name = "redis"
	if s.redisPing == nil {
		return OpsDependencyCheck{
			Name:     name,
			Required: false,
			Status:   OpsStatusDisabled,
			Message:  "Redis is not configured; the server will use in-memory fallbacks where supported.",
			Action:   "Configure redis_url when shared rate limiting or cache behavior is required.",
		}
	}
	start := s.now()
	if err := s.redisPing(ctx); err != nil {
		return OpsDependencyCheck{
			Name:           name,
			Required:       false,
			Status:         OpsStatusDegraded,
			DurationMillis: millisSince(start, s.now),
			Message:        "Redis is configured but not reachable; optional cache/rate-limit behavior may be degraded.",
			Action:         "Verify redis_url, Redis health, credentials, and network path.",
			ErrorClass:     classifyOpsError(err),
		}
	}
	return OpsDependencyCheck{
		Name:           name,
		Required:       false,
		Status:         OpsStatusOK,
		DurationMillis: millisSince(start, s.now),
		Message:        "Redis ping succeeded; optional cache/rate-limit backend is reachable.",
	}
}

func (s *OpsService) checkStorageList(ctx context.Context) OpsDependencyCheck {
	const name = "storage"
	if s.blobStore == nil {
		return OpsDependencyCheck{
			Name:     name,
			Required: true,
			Status:   OpsStatusNotConfigured,
			Message:  "Blob storage is not configured; bundle and snapshot objects cannot be served.",
			Action:   "Check S3 endpoint, bucket, credentials, and server startup wiring.",
		}
	}
	start := s.now()
	objects, err := s.blobStore.List(ctx, "")
	if err != nil {
		return OpsDependencyCheck{
			Name:           name,
			Required:       true,
			Status:         OpsStatusUnhealthy,
			DurationMillis: millisSince(start, s.now),
			Message:        "Blob storage list check failed; the configured bucket may be unreachable or credentials may lack list permission.",
			Action:         "Verify the S3 endpoint, bucket name, credentials, TLS setting, and bucket policy.",
			ErrorClass:     classifyOpsError(err),
		}
	}
	return OpsDependencyCheck{
		Name:           name,
		Required:       true,
		Status:         OpsStatusOK,
		DurationMillis: millisSince(start, s.now),
		Message:        fmt.Sprintf("Blob storage list check succeeded; saw %d object(s) in the bounded prefix scan.", len(objects)),
	}
}

func (s *OpsService) checkStorageProbe(ctx context.Context, storageStatus string) OpsDependencyCheck {
	const name = "storage_probe"
	if s.blobStore == nil || storageStatus != OpsStatusOK {
		return OpsDependencyCheck{
			Name:     name,
			Required: true,
			Status:   OpsStatusSkipped,
			Message:  "S3 read/write probe was skipped because the storage dependency is not healthy.",
			Action:   "Resolve the storage readiness check first, then rerun the dependency check.",
		}
	}

	start := s.now()
	key := s.probeKey()
	body := []byte("hsync ops storage probe\n")
	if err := s.blobStore.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		return storageProbeFailure(start, s.now, "write", "S3 probe object could not be written.", "Verify write permission for the configured bucket and prefix.", err)
	}
	size, ok, err := s.blobStore.Size(ctx, key)
	if err != nil {
		_ = s.blobStore.Delete(context.WithoutCancel(ctx), key)
		return storageProbeFailure(start, s.now, "stat", "S3 probe object metadata could not be read after write.", "Verify stat/head permission for the configured bucket.", err)
	}
	if !ok || size != int64(len(body)) {
		_ = s.blobStore.Delete(context.WithoutCancel(ctx), key)
		return OpsDependencyCheck{
			Name:           name,
			Required:       true,
			Status:         OpsStatusUnhealthy,
			DurationMillis: millisSince(start, s.now),
			Message:        "S3 probe metadata did not match the bytes written.",
			Action:         "Check for incompatible object-store behavior, proxy corruption, or bucket policy issues.",
			ErrorClass:     "metadata_mismatch",
		}
	}
	reader, err := s.blobStore.Get(ctx, key)
	if err != nil {
		_ = s.blobStore.Delete(context.WithoutCancel(ctx), key)
		return storageProbeFailure(start, s.now, "read", "S3 probe object could not be read after write.", "Verify read permission for the configured bucket.", err)
	}
	readBody, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		_ = s.blobStore.Delete(context.WithoutCancel(ctx), key)
		return storageProbeFailure(start, s.now, "read", "S3 probe object read failed before completion.", "Verify object-store stability and network path.", readErr)
	}
	if closeErr != nil {
		_ = s.blobStore.Delete(context.WithoutCancel(ctx), key)
		return storageProbeFailure(start, s.now, "read", "S3 probe object stream could not be closed cleanly.", "Verify object-store stability and network path.", closeErr)
	}
	if !bytes.Equal(readBody, body) {
		_ = s.blobStore.Delete(context.WithoutCancel(ctx), key)
		return OpsDependencyCheck{
			Name:           name,
			Required:       true,
			Status:         OpsStatusUnhealthy,
			DurationMillis: millisSince(start, s.now),
			Message:        "S3 probe object read back different bytes than were written.",
			Action:         "Check for proxy corruption, transparent transformations, or incompatible object-store behavior.",
			ErrorClass:     "readback_mismatch",
		}
	}
	if err := s.blobStore.Delete(ctx, key); err != nil {
		return OpsDependencyCheck{
			Name:           name,
			Required:       true,
			Status:         OpsStatusDegraded,
			DurationMillis: millisSince(start, s.now),
			Message:        "S3 probe write/read succeeded, but cleanup could not delete the probe object.",
			Action:         "Verify delete permission and remove leftover objects under ops/probes/ after fixing bucket policy.",
			ErrorClass:     classifyOpsError(err),
		}
	}
	return OpsDependencyCheck{
		Name:           name,
		Required:       true,
		Status:         OpsStatusOK,
		DurationMillis: millisSince(start, s.now),
		Message:        "S3 bucket read/write probe succeeded and the probe object was removed.",
	}
}

func (s *OpsService) checkBundleConsistency(ctx context.Context, limit int32) (OpsArtifactConsistency, []OpsConsistencyIssue) {
	artifact := OpsArtifactConsistency{Kind: "bundle"}
	if s.bundles == nil {
		artifact.Status = OpsStatusNotConfigured
		artifact.Message = "Bundle metadata store is not configured; bundle consistency cannot be checked."
		artifact.Action = "Verify PostgreSQL repository wiring and rerun the consistency check."
		return artifact, nil
	}
	if s.blobStore == nil {
		artifact.Status = OpsStatusNotConfigured
		artifact.Message = "Blob storage is not configured; bundle objects cannot be checked."
		artifact.Action = "Verify S3 storage wiring and rerun the consistency check."
		return artifact, nil
	}

	total, err := s.bundles.CountAll(ctx)
	if err != nil {
		return consistencyMetadataFailure("bundle", "Bundle metadata count failed.", "Verify PostgreSQL health and bundle table access.", err), nil
	}
	artifact.MetadataTotal = total
	bytes, err := s.bundles.SumSizeAll(ctx)
	if err != nil {
		return consistencyMetadataFailure("bundle", "Bundle metadata byte summary failed.", "Verify PostgreSQL health and bundle table access.", err), nil
	}
	artifact.MetadataBytes = bytes
	metas, err := s.bundles.ListForOpsConsistency(ctx, limit)
	if err != nil {
		return consistencyMetadataFailure("bundle", "Bundle metadata sample could not be loaded.", "Verify PostgreSQL health and bundle table access.", err), nil
	}
	artifact.CheckedMetadata = len(metas)
	artifact.MetadataTruncated = total > int64(len(metas))

	objects, err := s.blobStore.List(ctx, "bundles/")
	if err != nil {
		artifact.Status = OpsStatusUnhealthy
		artifact.Message = "Bundle object count could not be read from storage."
		artifact.Action = "Verify S3 list permission for the bundles/ prefix."
		artifact.ErrorClass = classifyOpsError(err)
		return artifact, nil
	}
	artifact.StorageObjectCount = len(objects)
	artifact.StorageBytes = sumObjectBytes(objects)
	artifact.StorageListTruncated = len(objects) >= int(DefaultOpsConsistencyLimit)

	var issues []OpsConsistencyIssue
	for _, meta := range metas {
		if err := ctx.Err(); err != nil {
			artifact.CheckFailures++
			artifact.IssueCount++
			issues = appendLimitedIssue(issues, consistencyIssue("bundle", bundleIssueID(meta), storage.BundleKey(meta.UserID.String(), meta.BundleID), "check_failed", "Bundle object check stopped before completion.", "Rerun after the request timeout or cancellation is resolved.", nil, nil, classifyOpsError(err)))
			break
		}
		key := storage.BundleKey(meta.UserID.String(), meta.BundleID)
		size, exists, err := s.blobStore.Size(ctx, key)
		if err != nil {
			artifact.CheckFailures++
			artifact.IssueCount++
			issues = appendLimitedIssue(issues, consistencyIssue("bundle", bundleIssueID(meta), key, "check_failed", "Bundle object metadata could not be checked.", "Verify S3 stat/head permission and rerun the consistency check.", int64Value(meta.SizeBytes), nil, classifyOpsError(err)))
			continue
		}
		if !exists {
			artifact.MissingObjects++
			artifact.IssueCount++
			issues = appendLimitedIssue(issues, consistencyIssue("bundle", bundleIssueID(meta), key, "missing_object", "PostgreSQL references a bundle object that is missing from storage.", "Restore the object from backup or investigate interrupted writes before accepting new sync traffic.", int64Value(meta.SizeBytes), nil, "missing"))
			continue
		}
		if size != meta.SizeBytes {
			artifact.SizeMismatches++
			artifact.IssueCount++
			issues = appendLimitedIssue(issues, consistencyIssue("bundle", bundleIssueID(meta), key, "size_mismatch", "Bundle object size does not match PostgreSQL metadata.", "Restore the expected object version from backup; do not parse or mutate the encrypted blob.", int64Value(meta.SizeBytes), int64Value(size), "metadata_mismatch"))
		}
	}

	return finalizeConsistencyArtifact(artifact), issues
}

func (s *OpsService) checkSnapshotConsistency(ctx context.Context, limit int32) (OpsArtifactConsistency, []OpsConsistencyIssue) {
	artifact := OpsArtifactConsistency{Kind: "snapshot"}
	if s.snapshots == nil {
		artifact.Status = OpsStatusNotConfigured
		artifact.Message = "Snapshot metadata store is not configured; snapshot consistency cannot be checked."
		artifact.Action = "Verify PostgreSQL repository wiring and rerun the consistency check."
		return artifact, nil
	}
	if s.blobStore == nil {
		artifact.Status = OpsStatusNotConfigured
		artifact.Message = "Blob storage is not configured; snapshot objects cannot be checked."
		artifact.Action = "Verify S3 storage wiring and rerun the consistency check."
		return artifact, nil
	}

	total, err := s.snapshots.CountAll(ctx)
	if err != nil {
		return consistencyMetadataFailure("snapshot", "Snapshot metadata count failed.", "Verify PostgreSQL health and snapshot table access.", err), nil
	}
	artifact.MetadataTotal = total
	bytes, err := s.snapshots.SumSizeAll(ctx)
	if err != nil {
		return consistencyMetadataFailure("snapshot", "Snapshot metadata byte summary failed.", "Verify PostgreSQL health and snapshot table access.", err), nil
	}
	artifact.MetadataBytes = bytes
	metas, err := s.snapshots.ListForOpsConsistency(ctx, limit)
	if err != nil {
		return consistencyMetadataFailure("snapshot", "Snapshot metadata sample could not be loaded.", "Verify PostgreSQL health and snapshot table access.", err), nil
	}
	artifact.CheckedMetadata = len(metas)
	artifact.MetadataTruncated = total > int64(len(metas))

	objects, err := s.blobStore.List(ctx, "snapshots/")
	if err != nil {
		artifact.Status = OpsStatusUnhealthy
		artifact.Message = "Snapshot object count could not be read from storage."
		artifact.Action = "Verify S3 list permission for the snapshots/ prefix."
		artifact.ErrorClass = classifyOpsError(err)
		return artifact, nil
	}
	artifact.StorageObjectCount = len(objects)
	artifact.StorageBytes = sumObjectBytes(objects)
	artifact.StorageListTruncated = len(objects) >= int(DefaultOpsConsistencyLimit)

	var issues []OpsConsistencyIssue
	for _, meta := range metas {
		if err := ctx.Err(); err != nil {
			artifact.CheckFailures++
			artifact.IssueCount++
			issues = appendLimitedIssue(issues, consistencyIssue("snapshot", snapshotIssueID(meta), storage.SnapshotKey(meta.UserID.String(), meta.SnapshotID), "check_failed", "Snapshot object check stopped before completion.", "Rerun after the request timeout or cancellation is resolved.", nil, nil, classifyOpsError(err)))
			break
		}
		key := storage.SnapshotKey(meta.UserID.String(), meta.SnapshotID)
		size, exists, err := s.blobStore.Size(ctx, key)
		if err != nil {
			artifact.CheckFailures++
			artifact.IssueCount++
			issues = appendLimitedIssue(issues, consistencyIssue("snapshot", snapshotIssueID(meta), key, "check_failed", "Snapshot object metadata could not be checked.", "Verify S3 stat/head permission and rerun the consistency check.", int64Value(meta.SizeBytes), nil, classifyOpsError(err)))
			continue
		}
		if !exists {
			artifact.MissingObjects++
			artifact.IssueCount++
			issues = appendLimitedIssue(issues, consistencyIssue("snapshot", snapshotIssueID(meta), key, "missing_object", "PostgreSQL references a snapshot object that is missing from storage.", "Restore the object from backup or investigate interrupted writes before accepting new sync traffic.", int64Value(meta.SizeBytes), nil, "missing"))
			continue
		}
		if size != meta.SizeBytes {
			artifact.SizeMismatches++
			artifact.IssueCount++
			issues = appendLimitedIssue(issues, consistencyIssue("snapshot", snapshotIssueID(meta), key, "size_mismatch", "Snapshot object size does not match PostgreSQL metadata.", "Restore the expected object version from backup; do not parse or mutate the encrypted blob.", int64Value(meta.SizeBytes), int64Value(size), "metadata_mismatch"))
		}
	}

	return finalizeConsistencyArtifact(artifact), issues
}

func consistencyMetadataFailure(kind, message, action string, err error) OpsArtifactConsistency {
	return OpsArtifactConsistency{
		Kind:       kind,
		Status:     OpsStatusUnhealthy,
		Message:    message,
		Action:     action,
		ErrorClass: classifyOpsError(err),
	}
}

func finalizeConsistencyArtifact(artifact OpsArtifactConsistency) OpsArtifactConsistency {
	switch {
	case artifact.MissingObjects > 0 || artifact.SizeMismatches > 0 || artifact.CheckFailures > 0:
		artifact.Status = OpsStatusUnhealthy
		artifact.Message = fmt.Sprintf("%s metadata sample found storage inconsistencies.", artifact.Kind)
		artifact.Action = "Review the issues list, restore missing or mismatched objects from backup, then rerun the check."
	case artifact.MetadataTruncated || artifact.StorageListTruncated:
		artifact.Status = OpsStatusDegraded
		artifact.Message = fmt.Sprintf("%s consistency check passed for the bounded sample; more rows or objects exist outside this check.", artifact.Kind)
		artifact.Action = "Increase backup verification coverage with an external full scan if the deployment has more objects than the built-in limit."
	case artifact.StorageObjectCount < int(artifact.MetadataTotal):
		artifact.Status = OpsStatusDegraded
		artifact.Message = fmt.Sprintf("%s object count is lower than active metadata count, though checked objects were present.", artifact.Kind)
		artifact.Action = "Run a broader consistency scan and inspect retention state; active metadata should normally have matching objects."
	default:
		artifact.Status = OpsStatusOK
		artifact.Message = fmt.Sprintf("%s metadata and storage sample are consistent.", artifact.Kind)
	}
	return artifact
}

func consistencyOverall(artifacts []OpsArtifactConsistency) string {
	overall := OpsStatusOK
	for _, artifact := range artifacts {
		switch artifact.Status {
		case OpsStatusOK:
			continue
		case OpsStatusDegraded:
			if overall == OpsStatusOK {
				overall = OpsStatusDegraded
			}
		default:
			return OpsStatusUnhealthy
		}
	}
	return overall
}

func sumObjectBytes(objects []storage.ObjectInfo) int64 {
	var total int64
	for _, obj := range objects {
		total += obj.Size
	}
	return total
}

func consistencyIssue(kind, id, key, status, message, action string, expectedSize, actualSize *int64, errorClass string) OpsConsistencyIssue {
	return OpsConsistencyIssue{
		Kind:         kind,
		ID:           id,
		Key:          key,
		Status:       status,
		Message:      message,
		Action:       action,
		ExpectedSize: expectedSize,
		ActualSize:   actualSize,
		ErrorClass:   errorClass,
	}
}

func appendLimitedIssue(issues []OpsConsistencyIssue, issue OpsConsistencyIssue) []OpsConsistencyIssue {
	if len(issues) >= maxOpsConsistencyIssues {
		return issues
	}
	return append(issues, issue)
}

func int64Value(v int64) *int64 {
	cp := v
	return &cp
}

func bundleIssueID(meta model.BundleMeta) string {
	return meta.UserID.String() + "/" + meta.BundleID
}

func snapshotIssueID(meta model.SnapshotMeta) string {
	return meta.UserID.String() + "/" + meta.SnapshotID
}

func storageProbeFailure(start time.Time, now func() time.Time, stage, message, action string, err error) OpsDependencyCheck {
	return OpsDependencyCheck{
		Name:           "storage_probe",
		Required:       true,
		Status:         OpsStatusUnhealthy,
		DurationMillis: millisSince(start, now),
		Message:        message,
		Action:         action,
		ErrorClass:     stage + "_" + classifyOpsError(err),
	}
}

func (s *OpsService) probeKey() string {
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("ops/probes/%d.txt", s.now().UnixNano())
	}
	return fmt.Sprintf("ops/probes/%s-%s.txt", s.now().UTC().Format("20060102T150405Z"), hex.EncodeToString(random[:]))
}

func (s *OpsService) lastDependencyReport() *OpsDependencyReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastDependency == nil {
		return nil
	}
	cp := cloneDependencyReport(*s.lastDependency)
	return &cp
}

func (s *OpsService) lastConsistencyReport() *OpsConsistencyReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastConsistency == nil {
		return nil
	}
	cp := cloneConsistencyReport(*s.lastConsistency)
	return &cp
}

func cloneDependencyReport(report OpsDependencyReport) OpsDependencyReport {
	cp := report
	if report.Checks != nil {
		cp.Checks = append([]OpsDependencyCheck(nil), report.Checks...)
	}
	return cp
}

func cloneConsistencyReport(report OpsConsistencyReport) OpsConsistencyReport {
	cp := report
	if report.Artifacts != nil {
		cp.Artifacts = append([]OpsArtifactConsistency(nil), report.Artifacts...)
	}
	if report.Issues != nil {
		cp.Issues = append([]OpsConsistencyIssue(nil), report.Issues...)
	}
	return cp
}

func lastStatusFor(report *OpsDependencyReport, names ...string) (string, *time.Time) {
	if report == nil {
		return OpsStatusNotChecked, nil
	}
	status := OpsStatusOK
	found := false
	for _, name := range names {
		for _, check := range report.Checks {
			if check.Name != name {
				continue
			}
			found = true
			status = worseOpsStatus(status, check.Status)
		}
	}
	if !found {
		return OpsStatusNotChecked, nil
	}
	checkedAt := report.CheckedAt
	return status, &checkedAt
}

func opsOverall(checks []OpsDependencyCheck) string {
	overall := OpsStatusOK
	for _, check := range checks {
		if check.Status == OpsStatusOK || check.Status == OpsStatusDisabled {
			continue
		}
		if check.Required && check.Status != OpsStatusDegraded {
			return OpsStatusUnhealthy
		}
		overall = OpsStatusDegraded
	}
	return overall
}

func worseOpsStatus(a, b string) string {
	rank := map[string]int{
		OpsStatusOK:            0,
		OpsStatusDisabled:      0,
		OpsStatusNotChecked:    1,
		OpsStatusDegraded:      2,
		OpsStatusSkipped:       3,
		OpsStatusNotConfigured: 4,
		OpsStatusUnhealthy:     5,
	}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func millisSince(start time.Time, now func() time.Time) int64 {
	if now == nil {
		now = time.Now
	}
	ms := now().Sub(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

func maskSecret(value string) MaskedValue {
	if strings.TrimSpace(value) == "" {
		return MaskedValue{Set: false}
	}
	return MaskedValue{Set: true, Value: "********"}
}

func maskEmailAddress(value string) MaskedValue {
	value = strings.TrimSpace(value)
	if value == "" {
		return MaskedValue{Set: false}
	}
	at := strings.LastIndex(value, "@")
	if at <= 0 {
		return MaskedValue{Set: true, Value: "***"}
	}
	return MaskedValue{Set: true, Value: "***" + value[at:]}
}

func maskWebhookURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<redacted>"
	}
	return parsed.Scheme + "://" + parsed.Host + "/..."
}

func redactConnectionURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" {
		return "<redacted>"
	}
	if parsed.User != nil {
		username := parsed.User.Username()
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword(username, "redacted")
		} else {
			parsed.User = url.User(username)
		}
	}
	query := parsed.Query()
	changed := false
	for key := range query {
		if sensitiveName(key) {
			query.Set(key, "redacted")
			changed = true
		}
	}
	if changed {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func sensitiveName(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "password") ||
		strings.Contains(name, "secret") ||
		strings.Contains(name, "token") ||
		strings.Contains(name, "key")
}

func classifyOpsError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "unavailable"
	}
}
