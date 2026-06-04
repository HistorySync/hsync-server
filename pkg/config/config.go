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
	LogLevel        zerolog.Level `mapstructure:"log_level"`
	WebEnabled      bool          `mapstructure:"web_enabled"`
	WebAppName      string        `mapstructure:"web_app_name"`
	WebConsolePath  string        `mapstructure:"web_console_path"`
	WebSupportEmail string        `mapstructure:"web_support_email"`

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

	// Background tasks
	BackgroundTasksEnabled   bool          `mapstructure:"background_tasks_enabled"`
	QuotaReconcileInterval   time.Duration `mapstructure:"quota_reconcile_interval"`
	RetentionCleanupInterval time.Duration `mapstructure:"retention_cleanup_interval"`
	RetentionGracePeriod     time.Duration `mapstructure:"retention_grace_period"`
	RetentionDryRun          bool          `mapstructure:"retention_dry_run"`
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
		DatabaseURL:     "postgres://hsync:hsync@localhost:5432/hsync?sslmode=disable",
		RedisURL:        "redis://localhost:6379/0",
		S3Endpoint:      "localhost:9000",
		S3Bucket:        "hsync-bundles",
		S3UseSSL:        false,
		StripeDisabled:  true,
		OIDCProviderID:  "default",
		OIDCScopes:      "openid profile email",

		BackgroundTasksEnabled:   true,
		QuotaReconcileInterval:   24 * time.Hour,
		RetentionCleanupInterval: 0,
		RetentionGracePeriod:     30 * 24 * time.Hour,
		RetentionDryRun:          true,
	}
}

// Load reads configuration from file and environment, then validates it fully.
func Load() (*Config, error) {
	cfg, err := load()
	if err != nil {
		return nil, err
	}
	return cfg, cfg.Validate()
}

// LoadForMigrations loads configuration for the `migrate` subcommand. It
// validates only the database connection so migrations can run without
// requiring unrelated settings such as the JWT signing key or billing secrets.
func LoadForMigrations() (*Config, error) {
	cfg, err := load()
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
//  3. Environment variables (HSYNC_ prefix, e.g. HSYNC_DATABASE_URL)
func load() (*Config, error) {
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
	v.SetDefault("database_url", cfg.DatabaseURL)
	v.SetDefault("redis_url", cfg.RedisURL)
	v.SetDefault("s3_endpoint", cfg.S3Endpoint)
	v.SetDefault("s3_bucket", cfg.S3Bucket)
	v.SetDefault("s3_access_key", cfg.S3AccessKey)
	v.SetDefault("s3_secret_key", cfg.S3SecretKey)
	v.SetDefault("s3_use_ssl", cfg.S3UseSSL)
	v.SetDefault("stripe_disabled", cfg.StripeDisabled)
	v.SetDefault("oidc_enabled", cfg.OIDCEnabled)
	v.SetDefault("oidc_provider_id", cfg.OIDCProviderID)
	v.SetDefault("oidc_scopes", cfg.OIDCScopes)
	v.SetDefault("background_tasks_enabled", cfg.BackgroundTasksEnabled)
	v.SetDefault("quota_reconcile_interval", cfg.QuotaReconcileInterval)
	v.SetDefault("retention_cleanup_interval", cfg.RetentionCleanupInterval)
	v.SetDefault("retention_grace_period", cfg.RetentionGracePeriod)
	v.SetDefault("retention_dry_run", cfg.RetentionDryRun)

	// Read config file (non-fatal if missing)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config: %w", err)
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

	if !c.StripeDisabled {
		if c.StripeSecretKey == "" {
			errs = append(errs, "stripe_secret_key is required when billing is enabled")
		}
		if c.StripeWebhookSecret == "" {
			errs = append(errs, "stripe_webhook_secret is required when billing is enabled")
		}
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
