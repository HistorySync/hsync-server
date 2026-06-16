package supportbundle

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/historysync/hsync-server/pkg/config"
)

func TestRedactRemovesSensitiveValues(t *testing.T) {
	input := map[string]any{
		"email":          "alice@example.com",
		"webhook_secret": "whsec_live",
		"license_key":    "lic_live",
		"nested": map[string]any{
			"database_url": "postgres://user:pass@db.example/hsync?sslkey=abc",
			"message":      "contact bob@example.com",
		},
	}
	redacted := Redact(input)
	body, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal redacted: %v", err)
	}
	text := string(body)
	for _, forbidden := range []string{"alice@example.com", "bob@example.com", "whsec_live", "lic_live", "pass", "sslkey=abc"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("redacted body contains %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "email:") {
		t.Fatalf("redacted body does not include masked email fingerprint: %s", text)
	}
}

func TestGenerateSupportBundleRedactsConfigAndHonorsSince(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DatabaseURL = "postgres://hsync:secret@db.example/hsync"
	cfg.AdminKey = "admin-secret"
	cfg.OpsAlertEmail = "ops@example.com"
	since := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	bundle, err := Generate(context.Background(), Options{
		Config: cfg,
		Since:  since,
		Readyz: func(context.Context) ReadyzSummary {
			return ReadyzSummary{Status: "ok", Checks: map[string]string{"database": "ok"}}
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	body, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	text := string(body)
	for _, forbidden := range []string{"admin-secret", "ops@example.com", "secret@db.example"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("bundle contains forbidden value %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"since":"2026-06-08T00:00:00Z"`) {
		t.Fatalf("bundle missing since: %s", text)
	}
	if !strings.Contains(text, `"database_pool_max_conns":20`) || !strings.Contains(text, `"database_pool_min_conns":2`) {
		t.Fatalf("bundle missing database pool summary: %s", text)
	}
	if !strings.Contains(text, `"includes_blob_contents":false`) {
		t.Fatalf("bundle missing safe boundary: %s", text)
	}
}
