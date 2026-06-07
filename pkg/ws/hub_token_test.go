package ws

import (
	"net/http/httptest"
	"testing"
)

func TestDeviceTokenFromRequestPrefersAuthorizationHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws/push?token=query-token", nil)
	req.Header.Set("Authorization", "Bearer header-token")

	if got := deviceTokenFromRequest(req); got != "header-token" {
		t.Fatalf("deviceTokenFromRequest() = %q, want header-token", got)
	}
}

func TestDeviceTokenFromRequestSupportsAuthorizationForms(t *testing.T) {
	for _, tt := range []struct {
		name   string
		header string
		want   string
	}{
		{name: "bearer", header: "Bearer token-a", want: "token-a"},
		{name: "lowercase bearer", header: "bearer token-b", want: "token-b"},
		{name: "raw token", header: "token-c", want: "token-c"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ws/push", nil)
			req.Header.Set("Authorization", tt.header)

			if got := deviceTokenFromRequest(req); got != tt.want {
				t.Fatalf("deviceTokenFromRequest() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeviceTokenFromRequestFallsBackToQueryToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws/push?token=query-token", nil)

	if got := deviceTokenFromRequest(req); got != "query-token" {
		t.Fatalf("deviceTokenFromRequest() = %q, want query-token", got)
	}
}
