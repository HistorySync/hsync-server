package web

import (
	"encoding/json"
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

func TestRegisterMountsConsoleRoute(t *testing.T) {
	app := fiber.New()
	Register(app, Options{Enabled: true, AppName: "HistorySync CE", ConsolePath: "/console"})

	req := httptest.NewRequest("GET", "/console", nil)
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

func TestLandingPageEmbedsParseableSafeWebMeta(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		AppName:      `A"pp<script>`,
		ConsolePath:  "/console",
		SupportEmail: "ops@example.com<script>",
	}))

	const marker = `<script type="application/json" id="web-meta">`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatal("landing page missing web meta script")
	}
	start += len(marker)
	end := strings.Index(body[start:], "</script>")
	if end < 0 {
		t.Fatal("landing page missing web meta script close tag")
	}
	raw := body[start : start+end]
	if strings.Contains(raw, "<script>") {
		t.Fatalf("web meta JSON contains raw script tag: %s", raw)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("web meta JSON is not parseable: %v", err)
	}
	if meta["app_name"] != `A"pp<script>` {
		t.Fatalf("app_name = %q, want original value", meta["app_name"])
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

	if !strings.Contains(body, "Refresh overview") {
		t.Fatal("landing page missing overview refresh action")
	}
	if !strings.Contains(body, "/admin/stats") {
		t.Fatal("landing page missing admin stats reference")
	}
	if !strings.Contains(body, "settings-groups") {
		t.Fatal("landing page missing settings panel")
	}
	if !strings.Contains(body, "audit-filter") {
		t.Fatal("landing page missing audit filter form")
	}
	if !strings.Contains(body, "/api/v1/admin/security/stats") {
		t.Fatal("landing page missing security stats endpoint")
	}
	if !strings.Contains(body, `adminPath+"/ops/summary"`) {
		t.Fatal("landing page missing ops summary endpoint")
	}
	if !strings.Contains(body, "/api/meta/version") {
		t.Fatal("landing page missing version metadata endpoint")
	}
	if !strings.Contains(body, "Build info") {
		t.Fatal("landing page missing build info panel")
	}
	if !strings.Contains(body, "/admin/notifications/failures") {
		t.Fatal("landing page missing notification failures endpoint")
	}
	if !strings.Contains(body, "/readyz") {
		t.Fatal("landing page missing readiness probe")
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

func TestLandingPageIncludesExtensionHooks(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		Enabled:           true,
		AppName:           "HistorySync Enterprise",
		ConsolePath:       "/console",
		Edition:           "enterprise",
		APIPrefix:         "/api/v1",
		AdminPath:         "/admin",
		ExtraNavHTML:      `<a href="#extra-panel">Extra panel</a>`,
		ExtraSectionsHTML: `<section id="extra-panel">Extra section</section>`,
		ExtraScript:       `function appendExtraConsoleTasks(tasks){tasks.push(["extra",function(){}]);}`,
	}))

	checks := []string{
		`href="#extra-panel"`,
		`<section id="extra-panel">Extra section</section>`,
		`function appendExtraConsoleTasks(tasks){tasks.push(["extra",function(){}]);}`,
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Fatalf("landing page missing %q", check)
		}
	}
}

func TestLandingPageIncludesAdminConsoleInteractions(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		Enabled:     true,
		AppName:     "HistorySync CE",
		ConsolePath: "/console",
		Edition:     "community",
		APIPrefix:   "/api/v1",
		AdminPath:   "/admin",
	}))

	checks := []string{
		`id="admin-key"`,
		`name="event_type"`,
		`name="actor_user_id"`,
		`name="target_type"`,
		`name="target_id"`,
		`method:"PUT"`,
		`adminPath+"/settings/`,
		`masked override`,
		`id="ops-run-rows"`,
		`refresh-ops`,
		`retry-visible-notifications`,
		`Retry visible failures`,
		`notificationActionButton("Retry"`,
		`notificationActionButton("Requeue"`,
		`notificationActionButton("Discard"`,
		`Idempotency-Key`,
		`newIdempotencyKey()`,
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Fatalf("landing page missing %q", check)
		}
	}
	if strings.Contains(body, `button.disabled=true;
button.textContent="Redrive";`) {
		t.Fatal("landing page still contains disabled notification action placeholder")
	}
	if strings.Contains(body, "localStorage") {
		t.Fatal("landing page should not persist admin key in localStorage")
	}
}

func TestLandingPageIncludesNotificationFailureActionWiring(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		Enabled:     true,
		AppName:     "HistorySync CE",
		ConsolePath: "/console",
		Edition:     "community",
		APIPrefix:   "/api/v1",
		AdminPath:   "/admin",
	}))

	checks := []string{
		`adminPath+"/notifications/failures/"+encodeURIComponent(id)+"/"+action`,
		`adminPath+"/notifications/failures/retry"`,
		`setBanner(notificationActionSummary(label,response.body||{}),"");`,
		`operatorError(error)`,
		`body&&body.replayed?"replayed response":"fresh response"`,
		`await loadNotifications();`,
		`button.dataset.notificationAction=action;`,
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Fatalf("landing page missing notification action wiring %q", check)
		}
	}
}

func TestLandingPageIncludesOpsActionPanel(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		Enabled:     true,
		AppName:     "HistorySync CE",
		ConsolePath: "/console",
		Edition:     "community",
		APIPrefix:   "/api/v1",
		AdminPath:   "/admin",
	}))

	checks := []string{
		`id="ops-actions"`,
		`Ops actions`,
		`id="run-dependency-check"`,
		`Run dependency check`,
		`id="run-consistency-check"`,
		`Run consistency check`,
		`id="ops-consistency-limit"`,
		`id="restore-rehearsal-form"`,
		`id="restore-rehearsal-mode"`,
		`value="baseline">Generate restore baseline`,
		`value="verify">Verify restore manifest`,
		`id="restore-manifest-json"`,
		`Manifest JSON`,
		`id="support-bundle-form"`,
		`Download support bundle`,
		`id="operational-export-form"`,
		`id="export-record-type"`,
		`value="audit_logs"`,
		`value="ops_history"`,
		`value="notification_outbox"`,
		`id="export-format"`,
		`id="export-source"`,
		`id="export-limit"`,
		`id="export-from"`,
		`id="export-to"`,
		`id="ops-action-result"`,
		`id="ops-action-summary"`,
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Fatalf("landing page missing %q", check)
		}
	}
}

func TestLandingPageIncludesOpsActionWiring(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		Enabled:     true,
		AppName:     "HistorySync CE",
		ConsolePath: "/console",
		Edition:     "community",
		APIPrefix:   "/api/v1",
		AdminPath:   "/admin",
	}))

	checks := []string{
		`headers:{"Content-Type":"application/json","Idempotency-Key":newIdempotencyKey()}`,
		`runOpsPost("Run dependency check",adminPath+"/ops/check",{})`,
		`runOpsPost("Run consistency check",adminPath+"/ops/consistency?limit="+encodeURIComponent(String(limit)),{})`,
		`adminPath+"/ops/restore-rehearsal"`,
		`restoreManifestPayload(document.getElementById("restore-manifest-json").value)`,
		`parsed.manifest||parsed`,
		`downloadAdminURL("Download support bundle",adminPath+"/support-bundle"`,
		`downloadAdminURL("Export operational records",adminPath+"/exports/operational-records?"+params.toString()`,
		`URL.createObjectURL(raw)`,
		`link.download=filename`,
		`renderOpsActionResult("Restore rehearsal failed"`,
		`setBanner(operatorError(error),"err")`,
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Fatalf("landing page missing ops action wiring %q", check)
		}
	}
}

func TestLandingPageIncludesTimelineLookupPanel(t *testing.T) {
	body := landingPage(normalizeOptions(Options{
		Enabled:     true,
		AppName:     "HistorySync CE",
		ConsolePath: "/console",
		Edition:     "community",
		APIPrefix:   "/api/v1",
		AdminPath:   "/admin",
	}))

	checks := []string{
		`href="#support-timeline"`,
		`id="support-timeline"`,
		`id="support-timeline-form"`,
		`id="support-timeline-user-id" name="user_id"`,
		`id="support-timeline-email" name="email"`,
		`id="support-timeline-limit" name="limit" type="number"`,
		`id="support-timeline-detail-grid"`,
		`id="support-timeline-action-rows"`,
		`id="support-timeline-job-rows"`,
		`requestAdmin(adminPath+"/support/context?"+params.toString())`,
		`renderSupportTimelineLookup(response.body.context||null);`,
		`["timeline lookup",loadSupportTimelineLookup]`,
		`Timeline lookup loaded and audited.`,
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Fatalf("landing page missing %q", check)
		}
	}
}

func TestJSStringLiteralEscapesScriptBreakout(t *testing.T) {
	got := jsStringLiteral(`</script><script>alert("x")</script>`)
	if strings.Contains(got, "</script>") {
		t.Fatalf("jsStringLiteral() = %s, contains raw script close tag", got)
	}
	if !strings.Contains(got, `\u003c/script\u003e`) {
		t.Fatalf("jsStringLiteral() = %s, want JSON/HTML-safe escaping", got)
	}
}
