package handler

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/historysync/hsync-server/pkg/observability"
)

func TestMetricsEndpointCanBeScraped(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: New(Deps{}).ErrorHandler})
	app.Use(observability.HTTPMetricsMiddleware())
	h := New(Deps{Metrics: MetricsConfig{
		Enabled: true,
		Path:    "/metrics",
	}})
	h.RegisterRoutes(app)

	if _, err := app.Test(httptest.NewRequest("GET", "/healthz", nil)); err != nil {
		t.Fatalf("healthz app.Test() error = %v", err)
	}
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "hsync_http_requests_total") {
		t.Fatalf("metrics body missing http counter:\n%s", text)
	}
}

func TestMetricsEndpointRejectsDisallowedIP(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: New(Deps{}).ErrorHandler})
	h := New(Deps{Metrics: MetricsConfig{
		Enabled:      true,
		Path:         "/metrics",
		AllowedCIDRs: []string{"10.0.0.0/8"},
	}})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "203.0.113.9:12345"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

func TestReadyzRecordsDependencyMetricLabels(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: New(Deps{}).ErrorHandler})
	h := New(Deps{Metrics: MetricsConfig{
		Enabled: true,
		Path:    "/metrics",
	}})
	h.RegisterRoutes(app)

	readyReq := httptest.NewRequest("GET", "/readyz", nil)
	if _, err := app.Test(readyReq); err != nil {
		t.Fatalf("readyz app.Test() error = %v", err)
	}

	metricsReq := httptest.NewRequest("GET", "/metrics", nil)
	resp, err := app.Test(metricsReq)
	if err != nil {
		t.Fatalf("metrics app.Test() error = %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	want := `hsync_readiness_dependency_status{dependency="database",result="not_configured"} 1`
	if !strings.Contains(text, want) {
		t.Fatalf("metrics body missing readiness label %q:\n%s", want, text)
	}
}

func TestMetricsEndpointExposesWebSocketHardeningMetrics(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: New(Deps{}).ErrorHandler})
	h := New(Deps{Metrics: MetricsConfig{
		Enabled: true,
		Path:    "/metrics",
	}})
	h.RegisterRoutes(app)

	observability.SetWebSocketActiveConnections(7)
	observability.RecordWebSocketUpgradeRejected("origin")
	observability.RecordWebSocketUpgradeRejected("capacity")

	resp, err := app.Test(httptest.NewRequest("GET", "/metrics", nil))
	if err != nil {
		t.Fatalf("metrics app.Test() error = %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		`hsync_websocket_connections_active 7`,
		`hsync_websocket_upgrade_rejections_total{reason="origin"}`,
		`hsync_websocket_upgrade_rejections_total{reason="capacity"}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, text)
		}
	}
}
