package provider

import "testing"

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
