package provider

import (
	"strings"
	"testing"
)

func TestValidateWebhookURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "https", raw: "https://example.com/hooks/hsync", wantErr: false},
		{name: "http", raw: "http://example.com/hooks/hsync", wantErr: false},
		{name: "missing", raw: "", wantErr: true},
		{name: "ftp", raw: "ftp://example.com/hooks/hsync", wantErr: true},
		{name: "relative", raw: "/hooks/hsync", wantErr: true},
		{name: "userinfo", raw: "https://token@example.com/hooks/hsync", wantErr: true},
		{name: "localhost", raw: "https://localhost/hooks/hsync", wantErr: true},
		{name: "loopback ip", raw: "http://127.0.0.1/hooks/hsync", wantErr: true},
		{name: "private ip", raw: "http://192.168.1.10/hooks/hsync", wantErr: true},
		{name: "link local ip", raw: "http://169.254.1.10/hooks/hsync", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWebhookURL(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateWebhookURL(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
		})
	}
}

func TestSanitizeWebhookErrorRedactsSensitiveURLParts(t *testing.T) {
	got := SanitizeWebhookError(`send webhook request: Post "https://example.com/hook?token=secret&ok=1": timeout secret=abc123`)
	if strings.Contains(got, "token=secret") || strings.Contains(got, "abc123") {
		t.Fatalf("SanitizeWebhookError() = %q, leaked sensitive value", got)
	}
	if !strings.Contains(got, "https://example.com/...") {
		t.Fatalf("SanitizeWebhookError() = %q, want sanitized host", got)
	}
}
