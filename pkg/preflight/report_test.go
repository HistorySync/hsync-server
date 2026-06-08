package preflight

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestReportOverallAndHumanOutput(t *testing.T) {
	report := NewReport("community", time.Date(2026, 6, 8, 1, 2, 3, 0, time.UTC))
	report.Append(
		Check{ID: "ok", Scope: "test", Severity: SeverityOK, Message: "ok"},
		Check{ID: "warn", Scope: "test", Severity: SeverityWarn, Message: "warn", Action: "fix warning"},
		Check{ID: "error", Scope: "test", Severity: SeverityError, Message: "error"},
	)

	if report.Overall != SeverityError {
		t.Fatalf("overall = %s, want error", report.Overall)
	}
	if report.Summary[SeverityOK] != 1 || report.Summary[SeverityWarn] != 1 || report.Summary[SeverityError] != 1 {
		t.Fatalf("summary = %#v, want one of each", report.Summary)
	}

	var buf bytes.Buffer
	if err := WriteHuman(&buf, report); err != nil {
		t.Fatalf("WriteHuman: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"HistorySync community deployment preflight",
		"Overall: error",
		"[warn] test/warn: warn",
		"action: fix warning",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("human output missing %q:\n%s", want, out)
		}
	}
}

func TestSecretStateRedactsValue(t *testing.T) {
	state := SecretState("super-secret-value")
	if state["state"] != "present" {
		t.Fatalf("state = %#v, want present", state)
	}
	if got := state["fingerprint"]; got == "" || strings.Contains(got.(string), "super-secret") {
		t.Fatalf("fingerprint leaked secret: %#v", got)
	}

	var buf bytes.Buffer
	report := NewReport("community", time.Now())
	report.Add(Check{
		ID:       "secret",
		Scope:    "security",
		Severity: SeverityOK,
		Message:  "secret present",
		Details:  state,
	})
	if err := WriteJSON(&buf, report); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if strings.Contains(buf.String(), "super-secret-value") {
		t.Fatalf("json output leaked secret: %s", buf.String())
	}
}
