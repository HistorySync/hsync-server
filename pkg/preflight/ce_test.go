package preflight

import "testing"

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
