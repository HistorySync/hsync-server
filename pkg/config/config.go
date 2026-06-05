// Package config provides configuration loading and validation for the
// HistorySync Cloud Server, supporting YAML files and environment variable
// overrides via viper.
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

// Config holds all server configuration.
type Config struct {
	ListenAddr      string        `mapstructure:"listen_addr"`
	LogLevel        zerolog.Level `mapstructure:"-"`
	WebEnabled      bool          `mapstructure:"web_enabled"`
	WebAppName      string        `mapstructure:"web_app_name"`
	WebConsolePath  string        `mapstructure:"web_console_path"`
	WebSupportEmail string        `mapstructure:"web_support_email"`

	// Prometheus metrics
	MetricsEnabled      bool     `mapstructure:"metrics_enabled"`
	MetricsPath         string   `mapstructure:"metrics_path"`
	MetricsAllowedCIDRs []string `mapstructure:"metrics_allowed_cidrs"`

	// Database
	DatabaseURL string `mapstructure:"database_url"`

	// Redis
	RedisURL string `mapstructure:"redis_url"`

	// S3 / MinIO
	S3Endpoint  string `mapstructure:"s3_endpoint"`
	S3Bucket    string `mapstructure:"s3_bucket"`
	S3AccessKey string `mapstructure:"s3_access_key"`
	S3SecretKey string `mapstructure:"s3_secret_key"`
	S3UseSSL    bool   `mapstructure:"s3_use_ssl"`

	// JWT
	JWTPrivateKey string `mapstructure:"jwt_private_key"` // Ed25519 private key, PEM or base64(raw seed)

	// Security
	SecuritySecret string `mapstructure:"security_secret"` // 32-byte AES-GCM key, raw or base64

	// Stripe (optional)
	StripeSecretKey     string `mapstructure:"stripe_secret_key"`
	StripeWebhookSecret string `mapstructure:"stripe_webhook_secret"`
	StripeDisabled      bool   `mapstructure:"stripe_disabled"`

	// Admin
	AdminKey string `mapstructure:"admin_key"`

	// OIDC (optional Enterprise login provider)
	OIDCEnabled      bool   `mapstructure:"oidc_enabled"`
	OIDCProviderID   string `mapstructure:"oidc_provider_id"`
	OIDCIssuerURL    string `mapstructure:"oidc_issuer_url"`
	OIDCClientID     string `mapstructure:"oidc_client_id"`
	OIDCClientSecret string `mapstructure:"oidc_client_secret"`
	OIDCRedirectURL  string `mapstructure:"oidc_redirect_url"`
	OIDCScopes       string `mapstructure:"oidc_scopes"`

	// Cloudflare Turnstile bot protection for public auth flows.
	TurnstileEnabled bool          `mapstructure:"turnstile_enabled"`
	TurnstileSecret  string        `mapstructure:"turnstile_secret"`
	TurnstileSiteKey string        `mapstructure:"turnstile_site_key"`
	TurnstileTimeout time.Duration `mapstructure:"turnstile_timeout"`

	// Background tasks
	BackgroundTasksEnabled     bool          `mapstructure:"background_tasks_enabled"`
	QuotaReconcileInterval     time.Duration `mapstructure:"quota_reconcile_interval"`
	RetentionCleanupInterval   time.Duration `mapstructure:"retention_cleanup_interval"`
	RetentionGracePeriod       time.Duration `mapstructure:"retention_grace_period"`
	RetentionDryRun            bool          `mapstructure:"retention_dry_run"`
	NotificationOutboxInterval time.Duration `mapstructure:"notification_outbox_interval"`

	// Runtime options
	OptionsFile string `mapstructure:"options_file"`

	// Public URL used to build links in user-facing emails.
	PublicURL string `mapstructure:"public_url"`

	// Notifications
	NotificationsEnabled    bool   `mapstructure:"notifications_enabled"`
	QuotaWarningThreshold   int    `mapstructure:"quota_warning_threshold"`
	QuotaExhaustedThreshold int    `mapstructure:"quota_exhausted_threshold"`
	EmailVerificationPath   string `mapstructure:"email_verification_path"`
	PasswordResetPath       string `mapstructure:"password_reset_path"`
	SMTPEnabled             bool   `mapstructure:"smtp_enabled"`
	SMTPServer              string `mapstructure:"smtp_server"`
	SMTPPort                int    `mapstructure:"smtp_port"`
	SMTPUsername            string `mapstructure:"smtp_username"`
	SMTPPassword            string `mapstructure:"smtp_password"`
	SMTPFrom                string `mapstructure:"smtp_from"`
	SMTPFromName            string `mapstructure:"smtp_from_name"`
	SMTPTLSMode             string `mapstructure:"smtp_tls_mode"`
}

// DefaultConfig returns a Config with reasonable defaults.
func DefaultConfig() *Config {
	return &Config{
		ListenAddr:      ":8080",
		LogLevel:        zerolog.InfoLevel,
		WebEnabled:      true,
		WebAppName:      "HistorySync Cloud Server",
		WebConsolePath:  "/console",
		WebSupportEmail: "support@historysync.app",
		MetricsEnabled:  false,
		MetricsPath:     "/metrics",
		MetricsAllowedCIDRs: []string{
			"127.0.0.0/8",
			"::1/128",
			"10.0.0.0/8",
			"172.16.0.0/12",
			"192.168.0.0/16",
		},
		DatabaseURL:      "postgres://hsync:hsync@localhost:5432/hsync?sslmode=disable",
		RedisURL:         "redis://localhost:6379/0",
		S3Endpoint:       "localhost:9000",
		S3Bucket:         "hsync-bundles",
		S3UseSSL:         false,
		StripeDisabled:   true,
		OIDCProviderID:   "default",
		OIDCScopes:       "openid profile email",
		TurnstileTimeout: 3 * time.Second,

		BackgroundTasksEnabled:     true,
		QuotaReconcileInterval:     24 * time.Hour,
		RetentionCleanupInterval:   0,
		RetentionGracePeriod:       30 * 24 * time.Hour,
		RetentionDryRun:            true,
		NotificationOutboxInterval: time.Minute,
		OptionsFile:                "",
		PublicURL:                  "http://localhost:8080",
		NotificationsEnabled:       false,
		QuotaWarningThreshold:      80,
		QuotaExhaustedThreshold:    100,
		EmailVerificationPath:      "/verify-email",
		PasswordResetPath:          "/reset-password",
		SMTPEnabled:                false,
		SMTPPort:                   587,
		SMTPFromName:               "HistorySync Cloud",
		SMTPTLSMode:                "starttls",
	}
}

// Load reads configuration from file and environment, then validates it fully.
func Load() (*Config, error) {
	cfg, err := load(nil)
	if err != nil {
		return nil, err
	}
	return cfg, cfg.Validate()
}

// LoadWithExtraFiles loads the base CE configuration and then merges any extra
// YAML config files by name before environment variables are applied. Enterprise
// uses this to layer configs/config.ee.yaml over the CE defaults.
func LoadWithExtraFiles(extraNames ...string) (*Config, error) {
	cfg, err := load(extraNames)
	if err != nil {
		return nil, err
	}
	return cfg, cfg.Validate()
}

// LoadForMigrations loads configuration for the `migrate` subcommand. It
// validates only the database connection so migrations can run without
// requiring unrelated settings such as the JWT signing key or billing secrets.
func LoadForMigrations() (*Config, error) {
	return LoadForMigrationsWithExtraFiles()
}

// LoadForMigrationsWithExtraFiles loads configuration for migration commands
// that layer deployment-specific config files over the CE defaults.
func LoadForMigrationsWithExtraFiles(extraNames ...string) (*Config, error) {
	cfg, err := load(extraNames)
	if err != nil {
		return nil, err
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config validation: database_url is required")
	}
	return cfg, nil
}

// load reads configuration from file and environment without validating it.
//
// Precedence (lowest to highest):
//  1. Default values
//  2. config.yaml / configs/config.yaml
//  3. Optional extra YAML files, if requested
//  4. Environment variables (HSYNC_ prefix, e.g. HSYNC_DATABASE_URL)
func load(extraNames []string) (*Config, error) {
	cfg := DefaultConfig()

	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./configs")
	v.SetEnvPrefix("HSYNC")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Bind defaults
	v.SetDefault("listen_addr", cfg.ListenAddr)
	v.SetDefault("log_level", "info")
	v.SetDefault("web_enabled", cfg.WebEnabled)
	v.SetDefault("web_app_name", cfg.WebAppName)
	v.SetDefault("web_console_path", cfg.WebConsolePath)
	v.SetDefault("web_support_email", cfg.WebSupportEmail)
	v.SetDefault("metrics_enabled", cfg.MetricsEnabled)
	v.SetDefault("metrics_path", cfg.MetricsPath)
	v.SetDefault("metrics_allowed_cidrs", cfg.MetricsAllowedCIDRs)
	v.SetDefault("database_url", cfg.DatabaseURL)
	v.SetDefault("redis_url", cfg.RedisURL)
	v.SetDefault("s3_endpoint", cfg.S3Endpoint)
	v.SetDefault("s3_bucket", cfg.S3Bucket)
	v.SetDefault("s3_access_key", cfg.S3AccessKey)
	v.SetDefault("s3_secret_key", cfg.S3SecretKey)
	v.SetDefault("s3_use_ssl", cfg.S3UseSSL)
	v.SetDefault("security_secret", cfg.SecuritySecret)
	v.SetDefault("stripe_disabled", cfg.StripeDisabled)
	v.SetDefault("oidc_enabled", cfg.OIDCEnabled)
	v.SetDefault("oidc_provider_id", cfg.OIDCProviderID)
	v.SetDefault("oidc_scopes", cfg.OIDCScopes)
	v.SetDefault("turnstile_enabled", cfg.TurnstileEnabled)
	v.SetDefault("turnstile_secret", cfg.TurnstileSecret)
	v.SetDefault("turnstile_site_key", cfg.TurnstileSiteKey)
	v.SetDefault("turnstile_timeout", cfg.TurnstileTimeout)
	v.SetDefault("background_tasks_enabled", cfg.BackgroundTasksEnabled)
	v.SetDefault("quota_reconcile_interval", cfg.QuotaReconcileInterval)
	v.SetDefault("retention_cleanup_interval", cfg.RetentionCleanupInterval)
	v.SetDefault("retention_grace_period", cfg.RetentionGracePeriod)
	v.SetDefault("retention_dry_run", cfg.RetentionDryRun)
	v.SetDefault("notification_outbox_interval", cfg.NotificationOutboxInterval)
	v.SetDefault("options_file", cfg.OptionsFile)
	v.SetDefault("public_url", cfg.PublicURL)
	v.SetDefault("notifications_enabled", cfg.NotificationsEnabled)
	v.SetDefault("quota_warning_threshold", cfg.QuotaWarningThreshold)
	v.SetDefault("quota_exhausted_threshold", cfg.QuotaExhaustedThreshold)
	v.SetDefault("email_verification_path", cfg.EmailVerificationPath)
	v.SetDefault("password_reset_path", cfg.PasswordResetPath)
	v.SetDefault("smtp_enabled", cfg.SMTPEnabled)
	v.SetDefault("smtp_server", cfg.SMTPServer)
	v.SetDefault("smtp_port", cfg.SMTPPort)
	v.SetDefault("smtp_username", cfg.SMTPUsername)
	v.SetDefault("smtp_password", cfg.SMTPPassword)
	v.SetDefault("smtp_from", cfg.SMTPFrom)
	v.SetDefault("smtp_from_name", cfg.SMTPFromName)
	v.SetDefault("smtp_tls_mode", cfg.SMTPTLSMode)

	// Read config file (non-fatal if missing)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}
	for _, name := range extraNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		v.SetConfigName(name)
		if err := v.MergeInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return nil, fmt.Errorf("merge config %s: %w", name, err)
			}
		}
	}

	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Parse log level
	level, err := zerolog.ParseLevel(v.GetString("log_level"))
	if err != nil {
		level = zerolog.InfoLevel
	}
	cfg.LogLevel = level

	return cfg, nil
}

// Validate checks that required fields are set and values are sensible.
func (c *Config) Validate() error {
	var errs []string

	if c.DatabaseURL == "" {
		errs = append(errs, "database_url is required")
	}

	if c.S3Bucket == "" {
		errs = append(errs, "s3_bucket is required")
	}

	if c.JWTPrivateKey == "" {
		errs = append(errs, "jwt_private_key is required")
	}
	if _, err := DecodeSecuritySecret(c.SecuritySecret); err != nil {
		errs = append(errs, "security_secret "+err.Error())
	}

	if c.OIDCEnabled {
		if c.OIDCIssuerURL == "" {
			errs = append(errs, "oidc_issuer_url is required when OIDC is enabled")
		}
		if c.OIDCClientID == "" {
			errs = append(errs, "oidc_client_id is required when OIDC is enabled")
		}
		if c.OIDCClientSecret == "" {
			errs = append(errs, "oidc_client_secret is required when OIDC is enabled")
		}
		if c.OIDCRedirectURL == "" {
			errs = append(errs, "oidc_redirect_url is required when OIDC is enabled")
		}
	}
	if c.TurnstileEnabled {
		if strings.TrimSpace(c.TurnstileSecret) == "" {
			errs = append(errs, "turnstile_secret is required when turnstile is enabled")
		}
		if c.TurnstileTimeout <= 0 {
			errs = append(errs, "turnstile_timeout must be greater than 0 when turnstile is enabled")
		}
	}
	if c.MetricsEnabled {
		if !strings.HasPrefix(strings.TrimSpace(c.MetricsPath), "/") {
			errs = append(errs, "metrics_path must start with /")
		}
	}

	if c.QuotaWarningThreshold < 0 || c.QuotaWarningThreshold > 100 {
		errs = append(errs, "quota_warning_threshold must be between 0 and 100")
	}
	if c.QuotaExhaustedThreshold <= 0 || c.QuotaExhaustedThreshold > 100 {
		errs = append(errs, "quota_exhausted_threshold must be between 1 and 100")
	}
	if c.QuotaWarningThreshold >= c.QuotaExhaustedThreshold {
		errs = append(errs, "quota_warning_threshold must be lower than quota_exhausted_threshold")
	}
	switch c.SMTPTLSMode {
	case "", "none", "starttls", "tls":
	default:
		errs = append(errs, "smtp_tls_mode must be one of none, starttls, tls")
	}
	if c.SMTPEnabled {
		if c.SMTPServer == "" {
			errs = append(errs, "smtp_server is required when smtp is enabled")
		}
		if c.SMTPPort <= 0 || c.SMTPPort > 65535 {
			errs = append(errs, "smtp_port must be between 1 and 65535")
		}
		if c.SMTPFrom == "" {
			errs = append(errs, "smtp_from is required when smtp is enabled")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation: %s", strings.Join(errs, "; "))
	}

	return nil
}

// DecodeEd25519PrivateKey attempts to decode a private key from PEM or raw base64 seed.
func DecodeEd25519PrivateKey(encoded string) (ed25519.PrivateKey, error) {
	// Try raw base64 seed (32 bytes)
	seed, err := base64.StdEncoding.DecodeString(encoded)
	if err == nil && len(seed) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(seed), nil
	}

	return nil, fmt.Errorf("unable to decode Ed25519 key: must be base64-encoded 32-byte seed")
}

// DecodeSecuritySecret decodes the AES-GCM key used for local secret
// encryption. It accepts either a raw 32-byte value or a base64-encoded 32-byte
// value so operators can generate it with `openssl rand -base64 32`.
func DecodeSecuritySecret(secret string) ([]byte, error) {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return nil, fmt.Errorf("is required")
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, err := enc.DecodeString(trimmed)
		if err == nil {
			if len(decoded) == 32 {
				return decoded, nil
			}
			return nil, fmt.Errorf("must decode to exactly 32 bytes")
		}
	}
	if len([]byte(trimmed)) == 32 {
		return []byte(trimmed), nil
	}
	return nil, fmt.Errorf("must be exactly 32 raw bytes or base64-encoded 32 bytes")
}
