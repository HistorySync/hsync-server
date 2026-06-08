package preflight

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/historysync/hsync-server/migrations"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/migrate"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/service"
)

type CEOptions struct {
	Edition string
	Now     func() time.Time
	Timeout time.Duration
}

func RunCE(ctx context.Context, cfg *config.Config, opts CEOptions) Report {
	if opts.Edition == "" {
		opts.Edition = "community"
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	report := NewReport(opts.Edition, now())
	if cfg == nil {
		report.Add(Check{
			ID:       "config.load",
			Scope:    "config",
			Severity: SeverityError,
			Message:  "Configuration could not be loaded.",
			Action:   "Fix config.yaml or HSYNC_* environment variables, then rerun doctor.",
		})
		return report
	}

	report.Append(checkJWT(cfg), checkSecuritySecret(cfg), checkAdminKey(cfg))
	pool, dbOK := checkPostgres(ctx, &report, cfg)
	if pool != nil {
		defer pool.Close()
	}
	checkRedis(ctx, &report, cfg)
	checkS3(ctx, &report, cfg)
	report.Append(checkMetrics(cfg)...)
	report.Append(checkSMTP(cfg), checkOpsAlerts(cfg))
	checkMigrationReadiness(ctx, &report, pool, dbOK)
	checkRuntimeSettings(ctx, &report, pool, dbOK)
	return report
}

func checkJWT(cfg *config.Config) Check {
	details := SecretState(cfg.JWTPrivateKey)
	if strings.TrimSpace(cfg.JWTPrivateKey) == "" {
		return Check{
			ID:       "jwt_private_key",
			Scope:    "security",
			Severity: SeverityError,
			Message:  "JWT signing key is missing.",
			Action:   "Set HSYNC_JWT_PRIVATE_KEY to a base64-encoded Ed25519 seed.",
			Details:  details,
		}
	}
	if _, err := config.DecodeEd25519PrivateKey(cfg.JWTPrivateKey); err != nil {
		return Check{
			ID:       "jwt_private_key",
			Scope:    "security",
			Severity: SeverityError,
			Message:  "JWT signing key is present but invalid.",
			Action:   "Regenerate HSYNC_JWT_PRIVATE_KEY as a base64-encoded 32-byte Ed25519 seed.",
			Details:  withError(details, err),
		}
	}
	return Check{
		ID:       "jwt_private_key",
		Scope:    "security",
		Severity: SeverityOK,
		Message:  "JWT signing key is present and decodable.",
		Details:  details,
	}
}

func checkSecuritySecret(cfg *config.Config) Check {
	details := SecretState(cfg.SecuritySecret)
	if _, err := config.DecodeSecuritySecret(cfg.SecuritySecret); err != nil {
		return Check{
			ID:       "security_secret",
			Scope:    "security",
			Severity: SeverityError,
			Message:  "Secret encryption key is missing or invalid.",
			Action:   "Set HSYNC_SECURITY_SECRET to exactly 32 raw bytes or base64-encoded 32 bytes.",
			Details:  withError(details, err),
		}
	}
	return Check{
		ID:       "security_secret",
		Scope:    "security",
		Severity: SeverityOK,
		Message:  "Secret encryption key is present and decodable.",
		Details:  details,
	}
}

func checkAdminKey(cfg *config.Config) Check {
	details := SecretState(cfg.AdminKey)
	if strings.TrimSpace(cfg.AdminKey) == "" {
		return Check{
			ID:       "admin_key",
			Scope:    "security",
			Severity: SeverityWarn,
			Message:  "Admin key is not configured, so admin routes protected by X-Admin-Key are unavailable.",
			Action:   "Set HSYNC_ADMIN_KEY before exposing admin or ops routes.",
			Details:  details,
		}
	}
	return Check{
		ID:       "admin_key",
		Scope:    "security",
		Severity: SeverityOK,
		Message:  "Admin key is configured.",
		Details:  details,
	}
}

func checkPostgres(ctx context.Context, report *Report, cfg *config.Config) (*pgxpool.Pool, bool) {
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		report.Add(Check{
			ID:       "postgres",
			Scope:    "database",
			Severity: SeverityError,
			Message:  "PostgreSQL database_url is missing.",
			Action:   "Set HSYNC_DATABASE_URL to the PostgreSQL DSN for the deployment.",
		})
		return nil, false
	}
	pool, err := repository.NewPGXPool(ctx, cfg.DatabaseURL)
	if err != nil {
		report.Add(Check{
			ID:       "postgres",
			Scope:    "database",
			Severity: SeverityError,
			Message:  "PostgreSQL is configured but unreachable.",
			Action:   "Verify the DSN, network path, credentials, TLS settings, and that migrations have run.",
			Details: map[string]any{
				"database_url": RedactURL(cfg.DatabaseURL),
				"error":        err.Error(),
			},
		})
		return nil, false
	}
	report.Add(Check{
		ID:       "postgres",
		Scope:    "database",
		Severity: SeverityOK,
		Message:  "PostgreSQL connection succeeded.",
		Details:  map[string]any{"database_url": RedactURL(cfg.DatabaseURL)},
	})
	return pool, true
}

func checkRedis(ctx context.Context, report *Report, cfg *config.Config) {
	if strings.TrimSpace(cfg.RedisURL) == "" {
		report.Add(Check{
			ID:       "redis",
			Scope:    "cache",
			Severity: SeverityWarn,
			Message:  "Redis is not configured; the server will use in-memory fallbacks where available.",
			Action:   "Set HSYNC_REDIS_URL for shared rate limiting and cache behavior in multi-instance deployments.",
		})
		return
	}
	client, err := repository.NewRedisClient(ctx, cfg.RedisURL)
	if err != nil {
		report.Add(Check{
			ID:       "redis",
			Scope:    "cache",
			Severity: SeverityWarn,
			Message:  "Redis is configured but unreachable; runtime can degrade without it.",
			Action:   "Verify HSYNC_REDIS_URL and network access, or intentionally leave Redis disabled for a single-node deployment.",
			Details: map[string]any{
				"redis_url": RedactURL(cfg.RedisURL),
				"error":     err.Error(),
			},
		})
		return
	}
	defer client.Close()
	report.Add(Check{
		ID:       "redis",
		Scope:    "cache",
		Severity: SeverityOK,
		Message:  "Redis connection succeeded.",
		Details:  map[string]any{"redis_url": RedactURL(cfg.RedisURL)},
	})
}

func checkS3(ctx context.Context, report *Report, cfg *config.Config) {
	details := map[string]any{
		"endpoint":          strings.TrimSpace(cfg.S3Endpoint),
		"bucket":            strings.TrimSpace(cfg.S3Bucket),
		"access_key":        Presence(cfg.S3AccessKey),
		"secret_key":        Presence(cfg.S3SecretKey),
		"use_ssl":           cfg.S3UseSSL,
		"probe_side_effect": "read_only",
	}
	var missing []string
	if strings.TrimSpace(cfg.S3Endpoint) == "" {
		missing = append(missing, "s3_endpoint")
	}
	if strings.TrimSpace(cfg.S3Bucket) == "" {
		missing = append(missing, "s3_bucket")
	}
	if strings.TrimSpace(cfg.S3AccessKey) == "" {
		missing = append(missing, "s3_access_key")
	}
	if strings.TrimSpace(cfg.S3SecretKey) == "" {
		missing = append(missing, "s3_secret_key")
	}
	if len(missing) > 0 {
		details["missing"] = missing
		report.Add(Check{
			ID:       "s3_bucket",
			Scope:    "storage",
			Severity: SeverityError,
			Message:  "S3-compatible storage configuration is incomplete.",
			Action:   "Set S3 endpoint, bucket, access key, and secret key before accepting sync traffic.",
			Details:  details,
		})
		return
	}
	client, err := minio.New(cfg.S3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: cfg.S3UseSSL,
		Region: "us-east-1",
	})
	if err != nil {
		report.Add(Check{
			ID:       "s3_bucket",
			Scope:    "storage",
			Severity: SeverityError,
			Message:  "S3 client could not be initialized.",
			Action:   "Verify HSYNC_S3_ENDPOINT and TLS mode.",
			Details:  withError(details, err),
		})
		return
	}
	exists, err := client.BucketExists(ctx, cfg.S3Bucket)
	if err != nil {
		report.Add(Check{
			ID:       "s3_bucket",
			Scope:    "storage",
			Severity: SeverityError,
			Message:  "S3 bucket could not be checked.",
			Action:   "Verify bucket permissions include read/list/head checks and that the endpoint is reachable.",
			Details:  withError(details, err),
		})
		return
	}
	if !exists {
		report.Add(Check{
			ID:       "s3_bucket",
			Scope:    "storage",
			Severity: SeverityError,
			Message:  "S3 bucket does not exist.",
			Action:   "Create the bucket out of band or correct HSYNC_S3_BUCKET, then rerun doctor.",
			Details:  details,
		})
		return
	}
	objects := client.ListObjects(ctx, cfg.S3Bucket, minio.ListObjectsOptions{Recursive: true, MaxKeys: 1})
	for obj := range objects {
		if obj.Err != nil {
			report.Add(Check{
				ID:       "s3_bucket",
				Scope:    "storage",
				Severity: SeverityError,
				Message:  "S3 bucket exists but list permission failed.",
				Action:   "Grant list permission for the configured bucket and prefixes.",
				Details:  withError(details, obj.Err),
			})
			return
		}
		break
	}
	report.Add(Check{
		ID:       "s3_bucket",
		Scope:    "storage",
		Severity: SeverityOK,
		Message:  "S3 bucket exists and can be listed with a read-only probe.",
		Details:  details,
	})
}

func checkMetrics(cfg *config.Config) []Check {
	if !cfg.MetricsEnabled {
		return []Check{{
			ID:       "metrics",
			Scope:    "observability",
			Severity: SeverityOK,
			Message:  "Prometheus metrics endpoint is disabled.",
		}}
	}
	var checks []Check
	if !strings.HasPrefix(strings.TrimSpace(cfg.MetricsPath), "/") {
		checks = append(checks, Check{
			ID:       "metrics_path",
			Scope:    "observability",
			Severity: SeverityError,
			Message:  "Metrics endpoint path must start with /.",
			Action:   "Set HSYNC_METRICS_PATH to a path such as /metrics.",
			Details:  map[string]any{"path": cfg.MetricsPath},
		})
	} else {
		checks = append(checks, Check{
			ID:       "metrics_path",
			Scope:    "observability",
			Severity: SeverityOK,
			Message:  "Metrics endpoint path is valid.",
			Details:  map[string]any{"path": cfg.MetricsPath},
		})
	}
	if len(cfg.MetricsAllowedCIDRs) == 0 {
		checks = append(checks, Check{
			ID:       "metrics_cidr",
			Scope:    "observability",
			Severity: SeverityWarn,
			Message:  "Metrics endpoint is enabled without CIDR restrictions.",
			Action:   "Set HSYNC_METRICS_ALLOWED_CIDRS or protect the endpoint with a reverse proxy/network policy.",
		})
		return checks
	}
	var invalid []string
	for _, raw := range cfg.MetricsAllowedCIDRs {
		if !validCIDROrAddr(raw) {
			invalid = append(invalid, raw)
		}
	}
	if len(invalid) > 0 {
		checks = append(checks, Check{
			ID:       "metrics_cidr",
			Scope:    "observability",
			Severity: SeverityError,
			Message:  "One or more metrics CIDR entries are invalid.",
			Action:   "Use valid CIDR prefixes or single IP addresses in HSYNC_METRICS_ALLOWED_CIDRS.",
			Details:  map[string]any{"invalid": invalid},
		})
	} else {
		checks = append(checks, Check{
			ID:       "metrics_cidr",
			Scope:    "observability",
			Severity: SeverityOK,
			Message:  "Metrics CIDR restrictions are valid.",
			Details:  map[string]any{"allowed_cidrs": cfg.MetricsAllowedCIDRs},
		})
	}
	return checks
}

func checkSMTP(cfg *config.Config) Check {
	if !cfg.SMTPEnabled {
		return Check{
			ID:       "smtp",
			Scope:    "notifications",
			Severity: SeverityOK,
			Message:  "SMTP delivery is disabled.",
		}
	}
	details := map[string]any{
		"server":   cfg.SMTPServer,
		"port":     cfg.SMTPPort,
		"username": Presence(cfg.SMTPUsername),
		"password": Presence(cfg.SMTPPassword),
		"from":     cfg.SMTPFrom,
		"tls_mode": cfg.SMTPTLSMode,
	}
	var missing []string
	if strings.TrimSpace(cfg.SMTPServer) == "" {
		missing = append(missing, "smtp_server")
	}
	if cfg.SMTPPort <= 0 || cfg.SMTPPort > 65535 {
		missing = append(missing, "smtp_port")
	}
	if strings.TrimSpace(cfg.SMTPFrom) == "" {
		missing = append(missing, "smtp_from")
	}
	if len(missing) > 0 {
		details["missing"] = missing
		return Check{
			ID:       "smtp",
			Scope:    "notifications",
			Severity: SeverityError,
			Message:  "SMTP is enabled but required delivery settings are incomplete.",
			Action:   "Set SMTP host, port, from address, and credentials expected by your mail provider.",
			Details:  details,
		}
	}
	switch strings.TrimSpace(cfg.SMTPTLSMode) {
	case "", "none", "starttls", "tls":
	default:
		return Check{
			ID:       "smtp",
			Scope:    "notifications",
			Severity: SeverityError,
			Message:  "SMTP TLS mode is invalid.",
			Action:   "Set HSYNC_SMTP_TLS_MODE to none, starttls, or tls.",
			Details:  details,
		}
	}
	return Check{
		ID:       "smtp",
		Scope:    "notifications",
		Severity: SeverityOK,
		Message:  "SMTP delivery settings are structurally valid.",
		Details:  details,
	}
}

func checkOpsAlerts(cfg *config.Config) Check {
	details := map[string]any{
		"email":          Presence(cfg.OpsAlertEmail),
		"webhook_url":    Presence(cfg.OpsAlertWebhookURL),
		"webhook_secret": Presence(cfg.OpsAlertWebhookSecret),
	}
	if strings.TrimSpace(cfg.OpsAlertEmail) == "" && strings.TrimSpace(cfg.OpsAlertWebhookURL) == "" {
		return Check{
			ID:       "ops_alerts",
			Scope:    "notifications",
			Severity: SeverityWarn,
			Message:  "Ops failure alerts are not configured.",
			Action:   "Set HSYNC_OPS_ALERT_EMAIL or HSYNC_OPS_ALERT_WEBHOOK_URL so scheduled ops failures notify an operator.",
			Details:  details,
		}
	}
	if strings.TrimSpace(cfg.OpsAlertWebhookURL) != "" {
		parsed, err := url.Parse(cfg.OpsAlertWebhookURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Check{
				ID:       "ops_alerts",
				Scope:    "notifications",
				Severity: SeverityError,
				Message:  "Ops alert webhook URL is invalid.",
				Action:   "Set HSYNC_OPS_ALERT_WEBHOOK_URL to a valid HTTPS endpoint.",
				Details:  details,
			}
		}
	}
	return Check{
		ID:       "ops_alerts",
		Scope:    "notifications",
		Severity: SeverityOK,
		Message:  "Ops alert destination is configured.",
		Details:  details,
	}
}

func checkRuntimeSettings(ctx context.Context, report *Report, pool *pgxpool.Pool, dbOK bool) {
	settingsSvc := service.NewSettingsService(nil, service.DefaultSettingDefinitions())
	if pool != nil {
		settingsSvc = service.NewSettingsService(repository.NewSystemSettingRepo(pool), service.DefaultSettingDefinitions())
	}
	if !dbOK {
		report.Add(Check{
			ID:       "runtime_settings",
			Scope:    "settings",
			Severity: SeverityWarn,
			Message:  "Database-backed runtime settings could not be read; doctor is reporting code defaults.",
			Action:   "Fix PostgreSQL connectivity and rerun doctor to verify maintenance, signups, and passkey settings.",
		})
	}

	maintenance, maintenanceErr := settingsSvc.GetBool(ctx, service.SettingKeyMaintenanceMode)
	signups, signupsErr := settingsSvc.GetBool(ctx, service.SettingKeySignupsEnabled)
	report.Add(settingBoolCheck("maintenance_mode", "settings", maintenance, maintenanceErr,
		"Maintenance mode is disabled.",
		"Maintenance mode is enabled; readiness will fail and ordinary API writes are rejected.",
		"Disable maintenance mode before declaring the deployment ready for normal traffic."))
	report.Add(settingBoolCheck("signups_enabled", "settings", signups, signupsErr,
		"Self-service signups are enabled.",
		"Self-service signups are disabled.",
		"Confirm this is intended for the launch window; enable signups if public registration should be open."))
	checkPasskey(ctx, report, settingsSvc)
}

func checkMigrationReadiness(ctx context.Context, report *Report, pool *pgxpool.Pool, dbOK bool) {
	if !dbOK || pool == nil {
		report.Add(Check{
			ID:       "migration_readiness",
			Scope:    "database",
			Severity: SeverityWarn,
			Message:  "Migration readiness could not be checked because PostgreSQL is unavailable.",
			Action:   "Fix PostgreSQL connectivity, then run `hsync-server migrate status --json`.",
		})
		return
	}
	status, err := migrate.Status(ctx, pool, migrations.FS, "schema_migrations", "community")
	if err != nil {
		report.Add(Check{
			ID:       "migration_readiness",
			Scope:    "database",
			Severity: SeverityError,
			Message:  "Migration readiness could not be inspected.",
			Action:   "Verify database permissions and rerun doctor.",
			Details:  map[string]any{"error": err.Error()},
		})
		return
	}
	severity := SeverityOK
	message := "Database migration tracking matches embedded CE migrations."
	action := ""
	if !status.Consistent {
		severity = SeverityError
		message = "Database migration tracking does not match embedded CE migrations."
		action = "Deploy matching code and database versions before applying more migrations."
	} else if len(status.Pending) > 0 {
		severity = SeverityWarn
		message = "CE migrations are pending."
		action = "Run `hsync-server migrate up` during the upgrade window before starting normal traffic."
	} else if !status.TrackingTableOk {
		severity = SeverityWarn
		message = "CE migration tracking table does not exist."
		action = "Run `hsync-server migrate up` before starting the server."
	}
	report.Add(Check{
		ID:       "migration_readiness",
		Scope:    "database",
		Severity: severity,
		Message:  message,
		Action:   action,
		Details:  map[string]any{"status": status},
	})

	findings, err := migrate.Drift(ctx, pool, CEDriftRequirements())
	if err != nil {
		report.Add(Check{
			ID:       "schema_drift",
			Scope:    "database",
			Severity: SeverityError,
			Message:  "Schema drift could not be inspected.",
			Action:   "Verify database permissions and rerun doctor.",
			Details:  map[string]any{"error": err.Error()},
		})
		return
	}
	if len(findings) == 0 {
		report.Add(Check{
			ID:       "schema_drift",
			Scope:    "database",
			Severity: SeverityOK,
			Message:  "Required CE tables, columns, and indexes are present.",
		})
		return
	}
	driftSeverity := SeverityWarn
	for _, finding := range findings {
		if finding.Severity == "error" {
			driftSeverity = SeverityError
			break
		}
	}
	report.Add(Check{
		ID:       "schema_drift",
		Scope:    "database",
		Severity: driftSeverity,
		Message:  "Required CE schema objects are missing.",
		Action:   "Run `hsync-server migrate up`; if drift remains, restore from a matching backup or repair the schema manually.",
		Details:  map[string]any{"findings": findings},
	})
}

func CEDriftRequirements() []migrate.SchemaRequirement {
	return []migrate.SchemaRequirement{
		{Kind: migrate.SchemaRequirementTable, Table: "users", Severity: "error"},
		{Kind: migrate.SchemaRequirementTable, Table: "bundles", Severity: "error"},
		{Kind: migrate.SchemaRequirementTable, Table: "snapshots", Severity: "error"},
		{Kind: migrate.SchemaRequirementTable, Table: "system_settings", Severity: "error"},
		{Kind: migrate.SchemaRequirementTable, Table: "notification_outbox", Severity: "error"},
		{Kind: migrate.SchemaRequirementTable, Table: "passkey_credentials", Severity: "error"},
		{Kind: migrate.SchemaRequirementTable, Table: "ops_check_runs", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "users", Name: "email", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "users", Name: "password_hash", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "users", Name: "deleted_at", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "bundles", Name: "bundle_id", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "bundles", Name: "key_generation", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "snapshots", Name: "snapshot_id", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "system_settings", Name: "key", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "notification_outbox", Name: "status", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "passkey_credentials", Name: "credential_id", Severity: "error"},
		{Kind: migrate.SchemaRequirementColumn, Table: "ops_check_runs", Name: "run_type", Severity: "error"},
		{Kind: migrate.SchemaRequirementIndex, Name: "idx_users_email_lower_unique", Severity: "error"},
		{Kind: migrate.SchemaRequirementIndex, Name: "idx_bundles_device_lamport", Severity: "warn", Action: "Run migrate up or recreate the index before accepting high-volume sync traffic."},
		{Kind: migrate.SchemaRequirementIndex, Name: "idx_notification_outbox_due", Severity: "warn", Action: "Run migrate up or recreate the index before enabling notification workers."},
		{Kind: migrate.SchemaRequirementIndex, Name: "idx_ops_check_runs_recent", Severity: "warn", Action: "Run migrate up or recreate the index before relying on ops history queries."},
	}
}

func settingBoolCheck(id, scope string, value bool, err error, okMessage, warnMessage, action string) Check {
	if err != nil {
		return Check{
			ID:       id,
			Scope:    scope,
			Severity: SeverityWarn,
			Message:  "Runtime setting could not be read.",
			Action:   "Verify the system_settings table exists and is readable.",
			Details:  map[string]any{"error": err.Error()},
		}
	}
	if id == "maintenance_mode" && value {
		return Check{ID: id, Scope: scope, Severity: SeverityWarn, Message: warnMessage, Action: action, Details: map[string]any{"value": value}}
	}
	if id == "signups_enabled" && !value {
		return Check{ID: id, Scope: scope, Severity: SeverityWarn, Message: warnMessage, Action: action, Details: map[string]any{"value": value}}
	}
	return Check{ID: id, Scope: scope, Severity: SeverityOK, Message: okMessage, Details: map[string]any{"value": value}}
}

func checkPasskey(ctx context.Context, report *Report, settingsSvc *service.SettingsService) {
	enabled, err := settingsSvc.GetBool(ctx, service.SettingKeyPasskeyEnabled)
	if err != nil {
		report.Add(Check{
			ID:       "passkey_enabled",
			Scope:    "webauthn",
			Severity: SeverityWarn,
			Message:  "Passkey enabled setting could not be read.",
			Action:   "Verify system_settings and rerun doctor.",
			Details:  map[string]any{"error": err.Error()},
		})
		return
	}
	originsRaw, _ := settingsSvc.GetString(ctx, service.SettingKeyPasskeyOrigins)
	rpIDRaw, _ := settingsSvc.GetString(ctx, service.SettingKeyPasskeyRPID)
	rpName, _ := settingsSvc.GetString(ctx, service.SettingKeyPasskeyRPName)
	details := map[string]any{
		"enabled":       enabled,
		"origins_state": Presence(originsRaw),
		"rp_id_state":   Presence(rpIDRaw),
		"rp_name":       strings.TrimSpace(rpName),
	}
	if !enabled {
		report.Add(Check{
			ID:       "passkey_origin",
			Scope:    "webauthn",
			Severity: SeverityOK,
			Message:  "Passkey/WebAuthn is disabled.",
			Details:  details,
		})
		return
	}
	origins, err := parsePasskeyOrigins(originsRaw)
	if err != nil {
		report.Add(Check{
			ID:       "passkey_origin",
			Scope:    "webauthn",
			Severity: SeverityError,
			Message:  "Passkey/WebAuthn is enabled but origins are missing or invalid.",
			Action:   "Set passkey_origins to comma-separated HTTPS origins with no path, such as https://app.example.com.",
			Details:  withError(details, err),
		})
		return
	}
	details["origins"] = origins
	rpID := strings.TrimSpace(rpIDRaw)
	if rpID == "" {
		parsed, _ := url.Parse(origins[0])
		rpID = hostWithoutPort(parsed.Host)
	}
	rpID = hostWithoutPort(rpID)
	details["rp_id"] = rpID
	if !rpIDAllowedForOrigins(rpID, origins) {
		report.Add(Check{
			ID:       "passkey_rp_id",
			Scope:    "webauthn",
			Severity: SeverityError,
			Message:  "Passkey RP ID does not match configured origins.",
			Action:   "Set passkey_rp_id to the origin host or a parent registrable domain shared by all configured origins.",
			Details:  details,
		})
		return
	}
	report.Add(Check{
		ID:       "passkey_origin",
		Scope:    "webauthn",
		Severity: SeverityOK,
		Message:  "Passkey/WebAuthn origins and RP ID are valid.",
		Details:  details,
	})
}

func parsePasskeyOrigins(configured string) ([]string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return nil, fmt.Errorf("passkey origins are required when passkeys are enabled")
	}
	parts := strings.Split(configured, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("invalid passkey origin %q", origin)
		}
		origins = append(origins, origin)
	}
	if len(origins) == 0 {
		return nil, fmt.Errorf("passkey origins are empty")
	}
	return origins, nil
}

func rpIDAllowedForOrigins(rpID string, origins []string) bool {
	rpID = strings.ToLower(strings.TrimSpace(rpID))
	if rpID == "" {
		return false
	}
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" {
			return false
		}
		host := strings.ToLower(hostWithoutPort(parsed.Host))
		if host == rpID || strings.HasSuffix(host, "."+rpID) {
			continue
		}
		return false
	}
	return true
}

func hostWithoutPort(host string) string {
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		return parsedHost
	}
	return strings.Trim(host, "[]")
}

func validCIDROrAddr(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if _, err := netip.ParsePrefix(raw); err == nil {
		return true
	}
	_, err := netip.ParseAddr(raw)
	return err == nil
}

func withError(details map[string]any, err error) map[string]any {
	out := make(map[string]any, len(details)+1)
	for k, v := range details {
		out[k] = v
	}
	if err != nil {
		out["error"] = err.Error()
	}
	return out
}
