package supportbundle

import (
	"context"
	"time"

	"github.com/historysync/hsync-server/pkg/buildinfo"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/preflight"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/service"
)

const (
	SchemaVersion = 1
	defaultLimit  = int32(20)
	maxLimit      = int32(100)
	doctorTimeout = 2 * time.Second
)

type ReadyzProbe func(context.Context) ReadyzSummary

type Options struct {
	Config       *config.Config
	BuildInfo    buildinfo.Info
	Ops          *service.OpsService
	Readyz       ReadyzProbe
	Since        time.Time
	DoctorReport *preflight.Report
	OpenAPI      OpenAPIVersion
	Now          func() time.Time
	Extension    any
}

type OpenAPIVersion struct {
	Version string `json:"version"`
	Path    string `json:"path,omitempty"`
}

type Bundle struct {
	SchemaVersion       int                 `json:"schema_version"`
	GeneratedAt         time.Time           `json:"generated_at"`
	SafeToShareBoundary Boundary            `json:"safe_to_share_boundary"`
	Since               *time.Time          `json:"since,omitempty"`
	BuildInfo           buildinfo.Info      `json:"build_info"`
	DoctorReport        preflight.Report    `json:"doctor_report"`
	Readyz              ReadyzSummary       `json:"readyz"`
	OpsSummary          service.OpsSummary  `json:"ops_summary"`
	RecentSchedulerRuns []model.OpsCheckRun `json:"recent_scheduler_runs"`
	ConfigPresence      ConfigPresence      `json:"config_presence"`
	OpenAPI             OpenAPIVersion      `json:"openapi"`
	Extension           any                 `json:"extension,omitempty"`
}

type Boundary struct {
	Redacted                       bool     `json:"redacted"`
	IncludesBlobContents           bool     `json:"includes_blob_contents"`
	IncludesRawWebhookPayloads     bool     `json:"includes_raw_webhook_payloads"`
	IncludesPlaintextAuditMetadata bool     `json:"includes_plaintext_audit_metadata"`
	Notes                          []string `json:"notes"`
}

type ReadyzSummary struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

type ConfigPresence struct {
	Server       map[string]any `json:"server"`
	Database     map[string]any `json:"database"`
	Redis        map[string]any `json:"redis"`
	Storage      map[string]any `json:"storage"`
	Security     map[string]any `json:"security"`
	Features     map[string]any `json:"features"`
	Notification map[string]any `json:"notification"`
}

func Generate(ctx context.Context, opts Options) (any, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	info := normalizeBuildInfo(opts.BuildInfo)
	opsSummary := service.OpsSummary{}
	var runs []model.OpsCheckRun
	if opts.Ops != nil {
		opsSummary = opts.Ops.Summary(ctx)
		history, err := opts.Ops.History(ctx, maxLimit)
		if err != nil {
			return nil, err
		}
		runs = filterOpsRunsSince(history.RecentRuns, opts.Since, defaultLimit)
	}

	doctor := preflight.Report{}
	if opts.DoctorReport != nil {
		doctor = *opts.DoctorReport
	} else {
		timeoutCtx, cancel := context.WithTimeout(ctx, doctorTimeout)
		defer cancel()
		doctor = preflight.RunCE(timeoutCtx, opts.Config, preflight.CEOptions{
			Edition: info.Edition,
			Timeout: doctorTimeout,
		})
	}

	readyz := ReadyzSummary{Status: "not_checked", Checks: map[string]string{}}
	if opts.Readyz != nil {
		readyz = opts.Readyz(ctx)
	}
	openapi := opts.OpenAPI
	if openapi.Version == "" {
		openapi.Version = "1.0.0"
	}

	bundle := Bundle{
		SchemaVersion:       SchemaVersion,
		GeneratedAt:         now().UTC(),
		SafeToShareBoundary: defaultBoundary(),
		BuildInfo:           info,
		DoctorReport:        doctor,
		Readyz:              readyz,
		OpsSummary:          opsSummary,
		RecentSchedulerRuns: runs,
		ConfigPresence:      summarizeConfigPresence(opts.Config),
		OpenAPI:             openapi,
		Extension:           opts.Extension,
	}
	if !opts.Since.IsZero() {
		since := opts.Since.UTC()
		bundle.Since = &since
	}
	return Redact(bundle), nil
}

func ProviderReadyz(ctx context.Context) ReadyzSummary {
	summary := ReadyzSummary{Status: "ok", Checks: map[string]string{}}
	for _, check := range provider.Registry().Readiness.ReadinessChecks(ctx) {
		if check.Name == "" {
			continue
		}
		summary.Checks[check.Name] = check.Status
		if check.Healthy {
			continue
		}
		if check.Critical {
			summary.Status = "unhealthy"
		} else if summary.Status == "ok" {
			summary.Status = "degraded"
		}
	}
	return summary
}

func filterOpsRunsSince(runs []model.OpsCheckRun, since time.Time, limit int32) []model.OpsCheckRun {
	if limit <= 0 || limit > maxLimit {
		limit = defaultLimit
	}
	out := make([]model.OpsCheckRun, 0, len(runs))
	for _, run := range runs {
		if !since.IsZero() && run.StartedAt.Before(since.UTC()) {
			continue
		}
		out = append(out, run)
		if len(out) >= int(limit) {
			break
		}
	}
	return out
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

func defaultBoundary() Boundary {
	return Boundary{
		Redacted:                       true,
		IncludesBlobContents:           false,
		IncludesRawWebhookPayloads:     false,
		IncludesPlaintextAuditMetadata: false,
		Notes: []string{
			"Generated support bundles are intended for operator-to-support sharing after review.",
			"Encrypted bundle and snapshot blobs are never exported.",
			"Secrets, tokens, license keys, webhook secrets, raw payloads, and email addresses are redacted or masked.",
		},
	}
}

func summarizeConfigPresence(cfg *config.Config) ConfigPresence {
	if cfg == nil {
		missing := map[string]any{"configured": false}
		return ConfigPresence{Server: missing, Database: missing, Redis: missing, Storage: missing, Security: missing, Features: missing, Notification: missing}
	}
	return ConfigPresence{
		Server: map[string]any{
			"listen_addr_set":      present(cfg.ListenAddr),
			"public_url_set":       present(cfg.PublicURL),
			"web_enabled":          cfg.WebEnabled,
			"web_console_path_set": present(cfg.WebConsolePath),
			"metrics_enabled":      cfg.MetricsEnabled,
			"metrics_path_set":     present(cfg.MetricsPath),
		},
		Database: map[string]any{
			"database_url":                      present(cfg.DatabaseURL),
			"database_pool_max_conns":           cfg.DatabasePoolMaxConns,
			"database_pool_min_conns":           cfg.DatabasePoolMinConns,
			"database_pool_max_conn_lifetime":   durationPresence(cfg.DatabasePoolMaxConnLifetime),
			"database_pool_max_conn_idle_time":  durationPresence(cfg.DatabasePoolMaxConnIdleTime),
			"database_pool_health_check_period": durationPresence(cfg.DatabasePoolHealthCheckPeriod),
		},
		Redis: map[string]any{
			"redis_url": present(cfg.RedisURL),
			"optional":  true,
		},
		Storage: map[string]any{
			"s3_endpoint_set":   present(cfg.S3Endpoint),
			"s3_bucket_set":     present(cfg.S3Bucket),
			"s3_access_key_set": present(cfg.S3AccessKey),
			"s3_secret_key_set": present(cfg.S3SecretKey),
			"s3_use_ssl":        cfg.S3UseSSL,
		},
		Security: map[string]any{
			"admin_key_set":             present(cfg.AdminKey),
			"jwt_private_key_set":       present(cfg.JWTPrivateKey),
			"security_secret_set":       present(cfg.SecuritySecret),
			"stripe_secret_key_set":     present(cfg.StripeSecretKey),
			"stripe_webhook_secret_set": present(cfg.StripeWebhookSecret),
			"oidc_client_secret_set":    present(cfg.OIDCClientSecret),
			"turnstile_secret_set":      present(cfg.TurnstileSecret),
		},
		Features: map[string]any{
			"stripe_disabled":               cfg.StripeDisabled,
			"oidc_enabled":                  cfg.OIDCEnabled,
			"turnstile_enabled":             cfg.TurnstileEnabled,
			"background_tasks_enabled":      cfg.BackgroundTasksEnabled,
			"ops_dependency_check_enabled":  cfg.OpsDependencyCheckInterval > 0,
			"ops_consistency_check_enabled": cfg.OpsConsistencyCheckInterval > 0,
			"notification_outbox_enabled":   cfg.NotificationOutboxInterval > 0,
			"history_retention_enabled":     cfg.HistoryRetentionInterval > 0,
		},
		Notification: map[string]any{
			"notifications_enabled": cfg.NotificationsEnabled,
			"smtp_enabled":          cfg.SMTPEnabled,
			"smtp_server_set":       present(cfg.SMTPServer),
			"smtp_username_set":     present(cfg.SMTPUsername),
			"smtp_password_set":     present(cfg.SMTPPassword),
			"smtp_from_set":         present(cfg.SMTPFrom),
			"ops_alert_email_set":   present(cfg.OpsAlertEmail),
			"ops_alert_webhook_set": present(cfg.OpsAlertWebhookURL),
			"ops_alert_secret_set":  present(cfg.OpsAlertWebhookSecret),
		},
	}
}

func present(value string) string {
	if value == "" {
		return "missing"
	}
	return "present"
}

func durationPresence(value time.Duration) string {
	if value <= 0 {
		return "pgx_default"
	}
	return value.String()
}
