package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateRequiresJWTPrivateKey(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "jwt_private_key") {
		t.Fatalf("Validate() error = %v, want jwt_private_key error", err)
	}
}

func TestValidateRequiresStripeSecretsWhenEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.StripeDisabled = false

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "stripe_secret_key") || !strings.Contains(err.Error(), "stripe_webhook_secret") {
		t.Fatalf("Validate() error = %v, want stripe secret errors", err)
	}
}

func TestValidateRequiresOIDCSettingsWhenEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.OIDCEnabled = true

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "oidc_issuer_url") || !strings.Contains(err.Error(), "oidc_client_id") || !strings.Contains(err.Error(), "oidc_redirect_url") {
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
