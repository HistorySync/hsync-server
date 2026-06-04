package provider

import (
	"strings"
	"testing"
)

func TestNewSMTPNotifierValidatesRequiredFields(t *testing.T) {
	if _, err := NewSMTPNotifier(SMTPConfig{}); err == nil || !strings.Contains(err.Error(), "smtp server") {
		t.Fatalf("NewSMTPNotifier() error = %v, want smtp server error", err)
	}
	if _, err := NewSMTPNotifier(SMTPConfig{Server: "smtp.example.com", From: "not-email"}); err == nil || !strings.Contains(err.Error(), "from address") {
		t.Fatalf("NewSMTPNotifier() error = %v, want from address error", err)
	}
	if _, err := NewSMTPNotifier(SMTPConfig{Server: "smtp.example.com", From: "noreply@example.com", TLSMode: "bad"}); err == nil || !strings.Contains(err.Error(), "tls mode") {
		t.Fatalf("NewSMTPNotifier() error = %v, want tls mode error", err)
	}
}

func TestNewSMTPNotifierDefaults(t *testing.T) {
	n, err := NewSMTPNotifier(SMTPConfig{
		Server: "smtp.example.com",
		From:   "noreply@example.com",
	})
	if err != nil {
		t.Fatalf("NewSMTPNotifier() error = %v", err)
	}
	if n.cfg.Port != 587 {
		t.Fatalf("port = %d, want 587", n.cfg.Port)
	}
	if n.cfg.TLSMode != SMTPTLSModeStartTLS {
		t.Fatalf("tls mode = %q, want starttls", n.cfg.TLSMode)
	}
	if !n.DeliveryEnabled() {
		t.Fatal("DeliveryEnabled() = false, want true")
	}
}

func TestSMTPMessageCleansHeaders(t *testing.T) {
	n, err := NewSMTPNotifier(SMTPConfig{
		Server: "smtp.example.com",
		From:   "noreply@example.com",
	})
	if err != nil {
		t.Fatalf("NewSMTPNotifier() error = %v", err)
	}
	msg := string(n.message("user@example.com", "hello\r\nInjected: yes", "<p>body</p>"))
	if strings.Contains(msg, "\r\nInjected: yes") {
		t.Fatalf("message contains injected header: %s", msg)
	}
	if !strings.Contains(msg, "Content-Type: text/html; charset=UTF-8") {
		t.Fatalf("message missing html content type: %s", msg)
	}
}

func TestEmailVerificationTemplateIncludesActionURL(t *testing.T) {
	body, err := renderEmailTemplate(emailVerificationTemplate, map[string]any{
		"AppName":         "HistorySync",
		"DisplayName":     "Alice",
		"VerificationURL": "https://cloud.example.com/verify-email?token=abc",
		"ExpiresIn":       "24 hours",
	})
	if err != nil {
		t.Fatalf("renderEmailTemplate() error = %v", err)
	}
	if !strings.Contains(body, "Verify email") || !strings.Contains(body, "https://cloud.example.com/verify-email?token=abc") {
		t.Fatalf("verification template missing action link: %s", body)
	}
}
