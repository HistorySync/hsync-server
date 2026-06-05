package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestValidateRejectsInvalidNotificationThresholds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.QuotaWarningThreshold = 100
	cfg.QuotaExhaustedThreshold = 100

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "quota_warning_threshold") {
		t.Fatalf("Validate() error = %v, want quota threshold error", err)
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
