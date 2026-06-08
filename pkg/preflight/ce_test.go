package preflight

import (
	"testing"

	"github.com/historysync/hsync-server/pkg/config"
)

func TestParsePasskeyOrigins(t *testing.T) {
	origins, err := parsePasskeyOrigins("https://app.example.com, https://admin.example.com")
	if err != nil {
		t.Fatalf("parsePasskeyOrigins: %v", err)
	}
	if len(origins) != 2 || origins[0] != "https://app.example.com" || origins[1] != "https://admin.example.com" {
		t.Fatalf("origins = %#v", origins)
	}

	for _, raw := range []string{
		"",
		"http://app.example.com",
		"https://app.example.com/path",
		"https://app.example.com?x=1",
	} {
		if _, err := parsePasskeyOrigins(raw); err == nil {
			t.Fatalf("parsePasskeyOrigins(%q) succeeded, want error", raw)
		}
	}
}

func TestRPIDAllowedForOrigins(t *testing.T) {
	origins := []string{"https://app.example.com", "https://admin.example.com"}
	if !rpIDAllowedForOrigins("example.com", origins) {
		t.Fatal("parent domain should be allowed")
	}
	if !rpIDAllowedForOrigins("app.example.com", []string{"https://app.example.com"}) {
		t.Fatal("exact host should be allowed")
	}
	if rpIDAllowedForOrigins("evil.example.com", origins) {
		t.Fatal("unrelated subdomain should be rejected")
	}
}

func TestValidCIDROrAddr(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.0/8", "::1/128"} {
		if !validCIDROrAddr(raw) {
			t.Fatalf("validCIDROrAddr(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"", "10.0.0.0/99", "not-an-ip"} {
		if validCIDROrAddr(raw) {
			t.Fatalf("validCIDROrAddr(%q) = true, want false", raw)
		}
	}
}

func TestInvalidWebSocketOrigins(t *testing.T) {
	invalid := invalidWebSocketOrigins([]string{
		"https://app.example.com",
		"http://localhost:8080",
		"https://app.example.com/path",
		"ftp://app.example.com",
		"",
	})
	if len(invalid) != 3 {
		t.Fatalf("invalid origins = %#v, want 3 invalid entries", invalid)
	}
}

func TestCheckRateLimitReportsMemoryFallbackRisk(t *testing.T) {
	cfg := config.DefaultConfig()
	checks := checkRateLimit(cfg)

	fallback := findCheck(checks, "rate_limit_redis_fallback")
	if fallback == nil {
		t.Fatal("rate_limit_redis_fallback check missing")
	}
	if fallback.Severity != SeverityWarn {
		t.Fatalf("fallback severity = %s, want warn", fallback.Severity)
	}
	if fallback.Details["redis_unavailable_fallback"] != "memory" {
		t.Fatalf("fallback details = %#v, want memory fallback", fallback.Details)
	}

	failMode := findCheck(checks, "rate_limit_fail_mode")
	if failMode == nil {
		t.Fatal("rate_limit_fail_mode check missing")
	}
	if failMode.Severity != SeverityWarn {
		t.Fatalf("fail mode severity = %s, want warn for fail_open default", failMode.Severity)
	}
}

func TestCheckRateLimitReportsInvalidPolicies(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RateLimitFailMode = "open-ish"
	cfg.RateLimitRedisUnavailableFallback = "redis"

	checks := checkRateLimit(cfg)
	policy := findCheck(checks, "rate_limit_policy")
	if policy == nil {
		t.Fatal("rate_limit_policy check missing")
	}
	if policy.Severity != SeverityError {
		t.Fatalf("policy severity = %s, want error", policy.Severity)
	}
}

func TestCheckAdminKeyWarnsOnWeakFormat(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AdminKey = "admin-secret"

	check := checkAdminKey(cfg)
	if check.Severity != SeverityWarn {
		t.Fatalf("admin key severity = %s, want warn", check.Severity)
	}
	if check.Details["state"] != "present" {
		t.Fatalf("admin key details = %#v, want present state", check.Details)
	}
	if _, ok := check.Details["weak_reasons"]; !ok {
		t.Fatalf("admin key details = %#v, want weak_reasons", check.Details)
	}
}

func TestCheckAdminKeyAcceptsStrongFormat(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AdminKey = "v1_9YyR0h6Nq3xV7mK2pL5sD8fG1jH4zC6b"

	check := checkAdminKey(cfg)
	if check.Severity != SeverityOK {
		t.Fatalf("admin key severity = %s, want ok", check.Severity)
	}
}

func findCheck(checks []Check, id string) *Check {
	for i := range checks {
		if checks[i].ID == id {
			return &checks[i]
		}
	}
	return nil
}
