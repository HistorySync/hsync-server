package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestRegisterMountsLandingPage(t *testing.T) {
	app := fiber.New()
	Register(app, Options{Enabled: true, AppName: "HistorySync CE", ConsolePath: "/console"})

	req := httptest.NewRequest("GET", "/", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
}

func TestRegisterDisablesRoutesWhenNotEnabled(t *testing.T) {
	app := fiber.New()
	Register(app, Options{Enabled: false})

	req := httptest.NewRequest("GET", "/", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusNotFound)
	}
}

func TestLandingPageEscapesConfiguredValues(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		AppName:      "A\"pp",
		ConsolePath:  "/console",
		SupportEmail: "ops@example.com<script>",
	}))

	if strings.Contains(body, "ops@example.com<script>") {
		t.Fatal("landing page contains raw support email value")
	}
	if !strings.Contains(body, "ops@example.com&amp;lt;script&amp;gt;") && !strings.Contains(body, "ops@example.com&lt;script&gt;") {
		t.Fatal("landing page did not escape support email")
	}
	if !strings.Contains(body, "A&quot;pp") {
		t.Fatal("landing page did not escape app name")
	}
}

func TestRegisterExposesWebMeta(t *testing.T) {
	app := fiber.New()
	Register(app, Options{
		Enabled:      true,
		AppName:      "HistorySync CE",
		ConsolePath:  "/console",
		SupportEmail: "ops@example.com",
		Edition:      "community",
		APIPrefix:    "/api/v1",
		AdminPath:    "/admin",
	})

	req := httptest.NewRequest("GET", "/api/meta/web", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
}

func TestNormalizeOptionsDefaultsAdminPath(t *testing.T) {
	opts := normalizeOptions(Options{})
	if opts.AdminPath != "/admin" {
		t.Fatalf("AdminPath = %q, want /admin", opts.AdminPath)
	}
	if opts.OverviewPath != "/api/meta/overview" {
		t.Fatalf("OverviewPath = %q, want /api/meta/overview", opts.OverviewPath)
	}
}

func TestNormalizeOptionsDefaultsEnterpriseOverviewPath(t *testing.T) {
	opts := normalizeOptions(Options{Edition: "enterprise"})
	if opts.OverviewPath != "/api/v1/meta/overview/enterprise" {
		t.Fatalf("OverviewPath = %q, want /api/v1/meta/overview/enterprise", opts.OverviewPath)
	}
}

func TestLandingPageIncludesRuntimeShell(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		Enabled:     true,
		AppName:     "HistorySync CE",
		ConsolePath: "/console",
		Edition:     "community",
		APIPrefix:   "/api/v1",
		AdminPath:   "/admin",
	}))

	if !strings.Contains(body, "Refresh probes") {
		t.Fatal("landing page missing refresh action")
	}
	if !strings.Contains(body, "/admin/stats") {
		t.Fatal("landing page missing admin stats reference")
	}
	if !strings.Contains(body, "runtime-checks") {
		t.Fatal("landing page missing runtime checks list")
	}
	if !strings.Contains(body, "/api/meta/overview") {
		t.Fatal("landing page missing overview api reference")
	}
}

func TestLandingPageIncludesEnterpriseOverviewRoute(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		Enabled:   true,
		AppName:   "HistorySync Enterprise",
		Edition:   "enterprise",
		APIPrefix: "/api/v1",
	}))

	if !strings.Contains(body, "/api/v1/meta/overview/enterprise") {
		t.Fatal("landing page missing enterprise overview route")
	}
}
