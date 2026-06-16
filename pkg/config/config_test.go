package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateRequiresJWTPrivateKey(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "jwt_private_key") {
		t.Fatalf("Validate() error = %v, want jwt_private_key error", err)
	}
}

func TestValidateDoesNotRequireStripeSecretsInCE(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.SecuritySecret = base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.StripeDisabled = false

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestDefaultConfigDisablesStripeBilling(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.StripeDisabled {
		t.Fatal("StripeDisabled = false, want true")
	}
}

func TestDefaultConfigSetsDatabasePoolDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.DatabasePoolMaxConns != 20 {
		t.Fatalf("DatabasePoolMaxConns = %d, want 20", cfg.DatabasePoolMaxConns)
	}
	if cfg.DatabasePoolMinConns != 2 {
		t.Fatalf("DatabasePoolMinConns = %d, want 2", cfg.DatabasePoolMinConns)
	}
	if cfg.DatabasePoolMaxConnLifetime != 0 {
		t.Fatalf("DatabasePoolMaxConnLifetime = %v, want pgx default sentinel 0", cfg.DatabasePoolMaxConnLifetime)
	}
	if cfg.DatabasePoolMaxConnIdleTime != 0 {
		t.Fatalf("DatabasePoolMaxConnIdleTime = %v, want pgx default sentinel 0", cfg.DatabasePoolMaxConnIdleTime)
	}
	if cfg.DatabasePoolHealthCheckPeriod != 0 {
		t.Fatalf("DatabasePoolHealthCheckPeriod = %v, want pgx default sentinel 0", cfg.DatabasePoolHealthCheckPeriod)
	}
}

func TestLoadWithExtraFilesMergesEnterpriseOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	seed := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	secret := base64.StdEncoding.EncodeToString(make([]byte, 32))
	base := "jwt_private_key: " + seed + "\nsecurity_secret: " + secret + "\nstripe_disabled: true\nweb_app_name: CE\n"
	extra := "stripe_disabled: false\nstripe_secret_key: sk_test\nstripe_webhook_secret: whsec_test\nweb_app_name: EE\n"
	if err := os.WriteFile(filepath.Join(dir, "configs", "config.yaml"), []byte(base), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "config.ee.yaml"), []byte(extra), 0o644); err != nil {
		t.Fatalf("write config.ee.yaml: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	cfg, err := LoadWithExtraFiles("config.ee")
	if err != nil {
		t.Fatalf("LoadWithExtraFiles() error = %v", err)
	}
	if cfg.StripeDisabled {
		t.Fatal("StripeDisabled = true, want merged false")
	}
	if cfg.StripeSecretKey != "sk_test" || cfg.StripeWebhookSecret != "whsec_test" {
		t.Fatalf("stripe secrets not merged: %q %q", cfg.StripeSecretKey, cfg.StripeWebhookSecret)
	}
	if cfg.WebAppName != "EE" {
		t.Fatalf("WebAppName = %q, want EE", cfg.WebAppName)
	}
}

func TestLoadWithExtraFilesAllowsMissingBaseConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	seed := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	secret := base64.StdEncoding.EncodeToString(make([]byte, 32))
	extra := "jwt_private_key: " + seed + "\nsecurity_secret: " + secret + "\nweb_app_name: EE Only\n"
	if err := os.WriteFile(filepath.Join(dir, "configs", "config.ee.yaml"), []byte(extra), 0o644); err != nil {
		t.Fatalf("write config.ee.yaml: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	cfg, err := LoadWithExtraFiles("config.ee")
	if err != nil {
		t.Fatalf("LoadWithExtraFiles() error = %v", err)
	}
	if cfg.WebAppName != "EE Only" {
		t.Fatalf("WebAppName = %q, want EE Only", cfg.WebAppName)
	}
}

func TestLoadWithEnvExtraFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	seed := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	secret := base64.StdEncoding.EncodeToString(make([]byte, 32))
	base := "jwt_private_key: " + seed + "\nsecurity_secret: " + secret + "\nweb_app_name: CE\n"
	extra := "web_app_name: load gate\nnotifications_enabled: true\n"
	if err := os.WriteFile(filepath.Join(dir, "configs", "config.yaml"), []byte(base), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "config.load.yaml"), []byte(extra), 0o644); err != nil {
		t.Fatalf("write config.load.yaml: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	oldExtra := os.Getenv("HSYNC_CONFIG_EXTRA_FILES")
	if err := os.Setenv("HSYNC_CONFIG_EXTRA_FILES", "config.load"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
		if err := os.Setenv("HSYNC_CONFIG_EXTRA_FILES", oldExtra); err != nil {
			t.Fatalf("restore env: %v", err)
		}
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WebAppName != "load gate" {
		t.Fatalf("WebAppName = %q, want load gate", cfg.WebAppName)
	}
	if !cfg.NotificationsEnabled {
		t.Fatal("NotificationsEnabled = false, want true")
	}
}

func TestValidateRequiresOIDCSettingsWhenEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.OIDCEnabled = true

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "oidc_issuer_url") || !strings.Contains(err.Error(), "oidc_client_id") || !strings.Contains(err.Error(), "oidc_client_secret") || !strings.Contains(err.Error(), "oidc_redirect_url") {
		t.Fatalf("Validate() error = %v, want oidc setting errors", err)
	}
}

func TestDefaultConfigSetsOIDCDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.OIDCProviderID != "default" {
		t.Fatalf("OIDCProviderID = %q, want default", cfg.OIDCProviderID)
	}
	if cfg.OIDCScopes != "openid profile email" {
		t.Fatalf("OIDCScopes = %q, want default scopes", cfg.OIDCScopes)
	}
}

func TestDefaultConfigDisablesTurnstile(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TurnstileEnabled {
		t.Fatal("TurnstileEnabled = true, want false")
	}
	if cfg.TurnstileTimeout <= 0 {
		t.Fatalf("TurnstileTimeout = %v, want > 0", cfg.TurnstileTimeout)
	}
}

func TestDefaultConfigSetsWebSocketCaps(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.WebSocketOriginCheckDisabled {
		t.Fatal("WebSocketOriginCheckDisabled = true, want false")
	}
	if cfg.WebSocketMaxConnections <= 0 {
		t.Fatalf("WebSocketMaxConnections = %d, want positive default", cfg.WebSocketMaxConnections)
	}
	if cfg.WebSocketMaxConnectionsPerUser <= 0 {
		t.Fatalf("WebSocketMaxConnectionsPerUser = %d, want positive default", cfg.WebSocketMaxConnectionsPerUser)
	}
}

func TestDefaultConfigSetsRateLimitDegradationDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.RateLimitFailMode != "fail_open" {
		t.Fatalf("RateLimitFailMode = %q, want fail_open", cfg.RateLimitFailMode)
	}
	if cfg.RateLimitPublicAuthFailMode != "fail_open" {
		t.Fatalf("RateLimitPublicAuthFailMode = %q, want fail_open", cfg.RateLimitPublicAuthFailMode)
	}
	if cfg.RateLimitRedisUnavailableFallback != "memory" {
		t.Fatalf("RateLimitRedisUnavailableFallback = %q, want memory", cfg.RateLimitRedisUnavailableFallback)
	}
}

func TestValidateRejectsInvalidRateLimitDegradationSettings(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.SecuritySecret = base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.RateLimitFailMode = "open-ish"
	cfg.RateLimitPublicAuthFailMode = "closed-ish"
	cfg.RateLimitEnterpriseAdminFailMode = "maybe"
	cfg.RateLimitEnterpriseBillingFailMode = "nope"
	cfg.RateLimitRedisUnavailableFallback = "redis"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want rate limit errors")
	}
	for _, want := range []string{
		"rate_limit_fail_mode",
		"rate_limit_public_auth_fail_mode",
		"rate_limit_enterprise_admin_fail_mode",
		"rate_limit_enterprise_billing_fail_mode",
		"rate_limit_redis_unavailable_fallback",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() error = %v, want %s", err, want)
		}
	}
}

func TestValidateRejectsInvalidWebSocketSettings(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.SecuritySecret = base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.WebSocketAllowedOrigins = []string{"https://app.example.com/path"}
	cfg.WebSocketMaxConnections = -1
	cfg.WebSocketMaxConnectionsPerUser = -1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want websocket errors")
	}
	for _, want := range []string{"websocket_allowed_origins", "websocket_max_connections", "websocket_max_connections_per_user"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() error = %v, want %s", err, want)
		}
	}
}

func TestValidateRequiresTurnstileSecretWhenEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.TurnstileEnabled = true

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "turnstile_secret") {
		t.Fatalf("Validate() error = %v, want turnstile_secret error", err)
	}
}

func TestDecodeEd25519PrivateKey(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}

	key, err := DecodeEd25519PrivateKey(base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatalf("DecodeEd25519PrivateKey() error = %v", err)
	}
	if len(key) != ed25519.PrivateKeySize {
		t.Fatalf("private key length = %d, want %d", len(key), ed25519.PrivateKeySize)
	}
}

func TestDecodeEd25519PrivateKeyRejectsInvalidSeed(t *testing.T) {
	if _, err := DecodeEd25519PrivateKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("DecodeEd25519PrivateKey() error = nil, want error")
	}
}

func TestDecodeSecuritySecretAcceptsBase64Key(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	key, err := DecodeSecuritySecret(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("DecodeSecuritySecret() error = %v", err)
	}
	if string(key) != string(raw) {
		t.Fatal("DecodeSecuritySecret() returned unexpected key")
	}
}

func TestDecodeSecuritySecretRejectsShortKey(t *testing.T) {
	if _, err := DecodeSecuritySecret(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("DecodeSecuritySecret() error = nil, want error")
	}
}

func TestDefaultConfigEnablesWebSurface(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.WebEnabled {
		t.Fatal("WebEnabled = false, want true")
	}
	if cfg.WebConsolePath != "/console" {
		t.Fatalf("WebConsolePath = %q, want /console", cfg.WebConsolePath)
	}
	if cfg.WebAppName == "" {
		t.Fatal("WebAppName is empty")
	}
}

func TestDefaultConfigSetsNotificationDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.NotificationsEnabled {
		t.Fatal("NotificationsEnabled = true, want false")
	}
	if cfg.QuotaWarningThreshold != 80 {
		t.Fatalf("QuotaWarningThreshold = %d, want 80", cfg.QuotaWarningThreshold)
	}
	if cfg.QuotaExhaustedThreshold != 100 {
		t.Fatalf("QuotaExhaustedThreshold = %d, want 100", cfg.QuotaExhaustedThreshold)
	}
	if cfg.EmailVerificationPath != "/verify-email" {
		t.Fatalf("EmailVerificationPath = %q, want /verify-email", cfg.EmailVerificationPath)
	}
	if cfg.PasswordResetPath != "/reset-password" {
		t.Fatalf("PasswordResetPath = %q, want /reset-password", cfg.PasswordResetPath)
	}
	if cfg.SMTPEnabled {
		t.Fatal("SMTPEnabled = true, want false")
	}
	if cfg.SMTPPort != 587 {
		t.Fatalf("SMTPPort = %d, want 587", cfg.SMTPPort)
	}
	if cfg.SMTPTLSMode != "starttls" {
		t.Fatalf("SMTPTLSMode = %q, want starttls", cfg.SMTPTLSMode)
	}
}

func TestDefaultConfigSetsHistoryRetentionDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.HistoryRetentionInterval != 0 {
		t.Fatalf("HistoryRetentionInterval = %v, want disabled by default", cfg.HistoryRetentionInterval)
	}
	if cfg.HistoryHotRetention != 90*24*time.Hour {
		t.Fatalf("HistoryHotRetention = %v, want 90 days", cfg.HistoryHotRetention)
	}
	if cfg.HistoryArchiveRetention != 365*24*time.Hour {
		t.Fatalf("HistoryArchiveRetention = %v, want 365 days", cfg.HistoryArchiveRetention)
	}
	if !cfg.HistoryRetentionDryRun {
		t.Fatal("HistoryRetentionDryRun = false, want true")
	}
}

func TestValidateRejectsInvalidHistoryRetention(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.SecuritySecret = base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.HistoryHotRetention = 30 * 24 * time.Hour
	cfg.HistoryArchiveRetention = 30 * 24 * time.Hour

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "history_archive_retention") {
		t.Fatalf("Validate() error = %v, want history_archive_retention error", err)
	}
}

func TestValidateRejectsInvalidNotificationThresholds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.QuotaWarningThreshold = 100
	cfg.QuotaExhaustedThreshold = 100

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "quota_warning_threshold") {
		t.Fatalf("Validate() error = %v, want quota threshold error", err)
	}
}

func TestValidateRejectsInvalidDatabasePoolSettings(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.SecuritySecret = base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.DatabasePoolMaxConns = 0
	cfg.DatabasePoolMinConns = 5
	cfg.DatabasePoolMaxConnLifetime = -time.Minute
	cfg.DatabasePoolMaxConnIdleTime = -time.Minute
	cfg.DatabasePoolHealthCheckPeriod = -time.Minute

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want database pool errors")
	}
	for _, want := range []string{
		"database_pool_max_conns",
		"database_pool_max_conn_lifetime",
		"database_pool_max_conn_idle_time",
		"database_pool_health_check_period",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() error = %v, want %s", err, want)
		}
	}
}

func TestValidateRequiresSMTPSettingsWhenEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.SMTPEnabled = true

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "smtp_server") || !strings.Contains(err.Error(), "smtp_from") {
		t.Fatalf("Validate() error = %v, want smtp setting errors", err)
	}
}
