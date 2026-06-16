package web

import (
	"strconv"
	"strings"
)

func pageHead(opts Options) string {
	return "<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>" + htmlEscape(opts.AppName) + " Admin Console</title><style>" + pageStyles() + "</style></head><body><main class=\"console-shell\">"
}

func pageStyles() string {
	return strings.Join([]string{
		":root{--bg:#f6f7f9;--surface:#fff;--surface-alt:#f0f5f4;--ink:#1c2430;--muted:#667085;--line:#d6dde5;--accent:#23645a;--accent-ink:#fff;--warn:#b54708;--danger:#b42318;--success:#027a48;--code:#182230;}",
		"*{box-sizing:border-box}",
		"body{margin:0;font-family:Segoe UI,Helvetica,Arial,sans-serif;background:var(--bg);color:var(--ink);}",
		"button,input,select,textarea{font:inherit}",
		"button{border:0;cursor:pointer}",
		"main{max-width:1320px;margin:0 auto;padding:24px 20px 48px;}",
		".topbar{display:grid;grid-template-columns:minmax(0,1fr) minmax(320px,460px);gap:18px;align-items:end;margin-bottom:18px}",
		".eyebrow{display:inline-flex;align-items:center;padding:4px 8px;border-radius:6px;background:var(--surface-alt);color:var(--accent);font-size:.78rem;font-weight:700;text-transform:uppercase}",
		"h1{font-size:2rem;line-height:1.15;margin:8px 0 4px;letter-spacing:0}",
		"h2{font-size:1.05rem;margin:0;letter-spacing:0}",
		"h3{font-size:.98rem;margin:0 0 8px;letter-spacing:0}",
		"p{line-height:1.55;margin:0;color:var(--muted)}",
		"a{color:var(--accent)}",
		".auth-panel{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:10px;align-items:end;background:var(--surface);border:1px solid var(--line);border-radius:8px;padding:12px}",
		"label{display:block;font-size:.78rem;font-weight:700;color:var(--muted);text-transform:uppercase;margin-bottom:5px}",
		"input,select,textarea{width:100%;border:1px solid var(--line);border-radius:6px;background:#fff;color:var(--ink);padding:9px 10px;min-height:38px}",
		"textarea{min-height:76px;resize:vertical}",
		"input:focus,select:focus,textarea:focus{outline:2px solid rgba(35,100,90,.22);border-color:var(--accent)}",
		".button{display:inline-flex;align-items:center;justify-content:center;gap:8px;min-height:38px;padding:9px 13px;border-radius:6px;background:var(--accent);color:var(--accent-ink);font-weight:700;text-decoration:none;white-space:nowrap}",
		".button.secondary{background:#fff;color:var(--ink);border:1px solid var(--line)}",
		".button.ghost{background:transparent;color:var(--accent);border:1px solid transparent}",
		".button.danger{background:var(--danger);color:#fff}",
		".button.warn{background:var(--warn);color:#fff}",
		".button[disabled]{opacity:.5;cursor:not-allowed}",
		".nav{display:flex;gap:8px;flex-wrap:wrap;margin:0 0 18px}",
		".nav a{display:inline-flex;padding:8px 10px;border-radius:6px;background:var(--surface);border:1px solid var(--line);text-decoration:none;color:var(--ink);font-weight:600}",
		".banner{display:none;margin:0 0 14px;padding:11px 12px;border-radius:8px;border:1px solid var(--line);background:var(--surface);color:var(--ink)}",
		".banner.show{display:block}",
		".banner.warn{border-color:#fed7aa;background:#fff7ed;color:#9a3412}",
		".banner.err{border-color:#fecaca;background:#fef2f2;color:#991b1b}",
		".section{background:var(--surface);border:1px solid var(--line);border-radius:8px;padding:16px;margin-bottom:16px}",
		".section-header{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:14px;flex-wrap:wrap}",
		".subgrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:10px}",
		".stat{border:1px solid var(--line);border-radius:8px;padding:12px;background:#fff;min-height:78px}",
		".stat span,.field-note{display:block;color:var(--muted);font-size:.82rem}",
		".stat strong{display:block;font-size:1.3rem;line-height:1.25;margin-top:6px;overflow-wrap:anywhere}",
		".two-col{display:grid;grid-template-columns:minmax(0,1.1fr) minmax(0,.9fr);gap:14px}",
		".panel{border:1px solid var(--line);border-radius:8px;padding:12px;background:#fff}",
		".split-panel{display:grid;grid-template-columns:minmax(0,1fr) minmax(300px,.6fr);gap:12px}",
		".detail-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:10px}",
		".detail-item{border:1px solid var(--line);border-radius:8px;padding:10px;background:#fff;min-height:66px}",
		".detail-item span{display:block;color:var(--muted);font-size:.78rem;font-weight:700;text-transform:uppercase;margin-bottom:5px}",
		".detail-item strong{display:block;overflow-wrap:anywhere}",
		".action-row{display:flex;gap:6px;flex-wrap:wrap;align-items:center}",
		".compact-form{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:10px;align-items:end}",
		".toolbar{display:flex;gap:10px;align-items:end;flex-wrap:wrap}",
		".toolbar .field{min-width:170px;flex:1}",
		".table-wrap{overflow:auto;border:1px solid var(--line);border-radius:8px;background:#fff}",
		"table{width:100%;border-collapse:collapse;min-width:720px}",
		"th,td{padding:10px 11px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top;font-size:.92rem}",
		"th{font-size:.76rem;text-transform:uppercase;color:var(--muted);background:#f8fafc;white-space:nowrap}",
		"tr:last-child td{border-bottom:0}",
		".mono{font-family:Consolas,Menlo,monospace;font-size:.86rem;overflow-wrap:anywhere}",
		".muted{color:var(--muted)}",
		".pill{display:inline-flex;align-items:center;border:1px solid var(--line);border-radius:6px;padding:3px 7px;background:#fff;color:var(--muted);font-size:.78rem;font-weight:700}",
		".pill.ok{border-color:#abefc6;background:#ecfdf3;color:var(--success)}",
		".pill.warn{border-color:#fed7aa;background:#fff7ed;color:var(--warn)}",
		".pill.err{border-color:#fecaca;background:#fef2f2;color:var(--danger)}",
		".settings-groups{display:grid;gap:12px}",
		".settings-group{border:1px solid var(--line);border-radius:8px;background:#fff;padding:12px}",
		".setting-row{display:grid;grid-template-columns:minmax(180px,.8fr) minmax(240px,1fr) minmax(240px,1fr);gap:12px;align-items:start;border-top:1px solid var(--line);padding:12px 0}",
		".setting-row:first-of-type{border-top:0}",
		".setting-key{font-weight:700;overflow-wrap:anywhere}",
		".setting-meta{display:flex;gap:6px;flex-wrap:wrap;margin-top:7px}",
		".setting-editor{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:8px;align-items:start}",
		".value-mask{font-family:Consolas,Menlo,monospace;letter-spacing:0}",
		".json-cell{max-width:360px;white-space:pre-wrap}",
		".probe-list{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:10px}",
		".probe{border:1px solid var(--line);border-radius:8px;padding:12px;background:#fff}",
		".probe pre{margin:10px 0 0;max-height:220px;overflow:auto;background:var(--code);color:#e4e7ec;border-radius:6px;padding:10px;font-size:.84rem}",
		"small{display:block;color:var(--muted);margin-top:12px}",
		"@media (max-width:980px){.topbar,.two-col,.split-panel{grid-template-columns:1fr}.setting-row{grid-template-columns:1fr}.auth-panel{grid-template-columns:1fr}.button{width:100%}}",
		"@media (max-width:640px){main{padding:16px 12px 36px}.section{padding:12px}h1{font-size:1.55rem}.toolbar{display:grid;grid-template-columns:1fr}.toolbar .field{min-width:0}table{min-width:620px}}",
	}, "")
}

func pageHero(opts Options, metaJSON []byte) string {
	return "<header class=\"topbar\"><div><span class=\"eyebrow\">" + htmlEscape(strings.ToUpper(opts.Edition)) + " edition</span><h1>" + htmlEscape(opts.AppName) + " admin console</h1><p id=\"console-status\">Ready</p></div><div class=\"auth-panel\"><div><label for=\"admin-key\">Admin key</label><input id=\"admin-key\" name=\"admin-key\" type=\"password\" autocomplete=\"off\" spellcheck=\"false\" placeholder=\"X-Admin-Key\"></div><button class=\"button\" id=\"refresh-all\" type=\"button\">Refresh</button></div><script type=\"application/json\" id=\"web-meta\">" + string(metaJSON) + "</script></header>"
}

func pageOverviewCards(opts Options) string {
	return "<nav class=\"nav\" aria-label=\"Console sections\">" +
		"<a href=\"#overview\">Overview</a>" +
		opts.ExtraNavHTML +
		"<a href=\"#support-timeline\">Timeline lookup</a>" +
		"<a href=\"#settings\">Settings</a>" +
		"<a href=\"#audit-logs\">Audit logs</a>" +
		"<a href=\"#security-stats\">Security stats</a>" +
		"<a href=\"#ops-checks\">Ops checks</a>" +
		"<a href=\"#notification-failures\">Notification failures</a>" +
		"<a href=\"#health-readiness\">Health</a>" +
		"</nav><div id=\"status-banner\" class=\"banner\"></div>" +
		"<section class=\"section\" id=\"overview\"><div class=\"section-header\"><h2>Overview</h2><button class=\"button secondary\" id=\"refresh-overview\" type=\"button\">Refresh overview</button></div>" +
		"<div class=\"subgrid\">" +
		"<div class=\"stat\"><span>Total users</span><strong id=\"metric-total-users\">pending</strong></div>" +
		"<div class=\"stat\"><span>Active devices</span><strong id=\"metric-active-devices\">pending</strong></div>" +
		"<div class=\"stat\"><span>Bundles</span><strong id=\"metric-bundles\">pending</strong></div>" +
		"<div class=\"stat\"><span>Snapshots</span><strong id=\"metric-snapshots\">pending</strong></div>" +
		"<div class=\"stat\"><span>Storage</span><strong id=\"metric-storage\">pending</strong></div>" +
		"<div class=\"stat\"><span>WebSocket</span><strong id=\"metric-websocket\">pending</strong></div>" +
		"</div><div class=\"subgrid\" style=\"margin-top:14px\">" +
		"<div class=\"stat\"><span>Version</span><strong class=\"mono\" id=\"build-version\">" + htmlEscape(opts.BuildInfo.Version) + "</strong></div>" +
		"<div class=\"stat\"><span>Commit</span><strong class=\"mono\" id=\"build-commit\">" + htmlEscape(opts.BuildInfo.Commit) + "</strong></div>" +
		"<div class=\"stat\"><span>Build time</span><strong class=\"mono\" id=\"build-time\">" + htmlEscape(opts.BuildInfo.BuildTime) + "</strong></div>" +
		"<div class=\"stat\"><span>Schema version</span><strong class=\"mono\" id=\"build-schema-version\">" + htmlEscape(monoNumber(opts.BuildInfo.SchemaVersion)) + "</strong></div>" +
		"</div><div class=\"two-col\" style=\"margin-top:14px\">" +
		"<div class=\"panel\"><h3>Users by status</h3><div id=\"users-status-breakdown\" class=\"muted\">pending</div></div>" +
		"<div class=\"panel\"><h3>Build info</h3><div id=\"build-info-detail\" class=\"muted\">pending</div></div>" +
		"</div><div class=\"panel\" style=\"margin-top:14px\"><h3>Recent users</h3><div class=\"table-wrap\"><table><thead><tr><th>Email</th><th>Tier</th><th>Status</th><th>Created</th></tr></thead><tbody id=\"recent-users\"><tr><td colspan=\"4\" class=\"muted\">pending</td></tr></tbody></table></div></div>" +
		"</div></section>"
}

func pageConsoleLayout(opts Options) string {
	return opts.ExtraSectionsHTML +
		"<section class=\"section\" id=\"support-timeline\"><div class=\"section-header\"><h2>Timeline lookup</h2><div class=\"action-row\"><button class=\"button secondary\" id=\"refresh-support-timeline\" type=\"button\">Refresh lookup</button></div></div>" +
		"<form class=\"compact-form\" id=\"support-timeline-form\"><div><label for=\"support-timeline-user-id\">User ID</label><input id=\"support-timeline-user-id\" name=\"user_id\" autocomplete=\"off\" placeholder=\"optional UUID\"></div><div><label for=\"support-timeline-email\">Email</label><input id=\"support-timeline-email\" name=\"email\" type=\"email\" autocomplete=\"off\" placeholder=\"user@example.com\"></div><div><label for=\"support-timeline-limit\">Limit</label><input id=\"support-timeline-limit\" name=\"limit\" type=\"number\" min=\"1\" max=\"25\" step=\"1\" value=\"10\"></div><div class=\"action-row\"><button class=\"button\" type=\"submit\">Look up</button><button class=\"button secondary\" id=\"clear-support-timeline\" type=\"button\">Clear</button></div></form>" +
		"<div class=\"subgrid\" style=\"margin-top:12px\" id=\"support-timeline-summary\"><div class=\"stat\"><span>Context</span><strong id=\"support-timeline-user-card\">pending</strong></div><div class=\"stat\"><span>Erasure</span><strong id=\"support-timeline-erasure-card\">pending</strong></div><div class=\"stat\"><span>Jobs</span><strong id=\"support-timeline-job-card\">pending</strong></div><div class=\"stat\"><span>Actions</span><strong id=\"support-timeline-action-card\">pending</strong></div></div>" +
		"<div class=\"split-panel\" style=\"margin-top:12px\"><div class=\"panel\"><h3>Incident summary</h3><div class=\"detail-grid\" id=\"support-timeline-detail-grid\"></div><pre id=\"support-timeline-json\">{}</pre></div><div class=\"panel\"><h3>Recent operations</h3><div class=\"table-wrap\"><table><thead><tr><th>Created</th><th>Action</th><th>Category</th><th>Target</th></tr></thead><tbody id=\"support-timeline-action-rows\"><tr><td colspan=\"4\" class=\"muted\">no lookup</td></tr></tbody></table></div><div class=\"table-wrap\" style=\"margin-top:12px\"><table><thead><tr><th>Updated</th><th>Job</th><th>Status</th><th>Detail</th></tr></thead><tbody id=\"support-timeline-job-rows\"><tr><td colspan=\"4\" class=\"muted\">no lookup</td></tr></tbody></table></div></div></div></section>" +
		"<section class=\"section\" id=\"settings\"><div class=\"section-header\"><h2>Settings</h2><button class=\"button secondary\" id=\"refresh-settings\" type=\"button\">Refresh settings</button></div><div id=\"settings-groups\" class=\"settings-groups\"><p>pending</p></div></section>" +
		"<section class=\"section\" id=\"audit-logs\"><div class=\"section-header\"><h2>Audit logs</h2></div><form class=\"toolbar\" id=\"audit-filter\">" +
		"<div class=\"field\"><label for=\"audit-event-type\">Event type</label><input id=\"audit-event-type\" name=\"event_type\" autocomplete=\"off\"></div>" +
		"<div class=\"field\"><label for=\"audit-actor-user-id\">Actor user ID</label><input id=\"audit-actor-user-id\" name=\"actor_user_id\" autocomplete=\"off\"></div>" +
		"<div class=\"field\"><label for=\"audit-target-type\">Target type</label><input id=\"audit-target-type\" name=\"target_type\" autocomplete=\"off\"></div>" +
		"<div class=\"field\"><label for=\"audit-target-id\">Target ID</label><input id=\"audit-target-id\" name=\"target_id\" autocomplete=\"off\"></div>" +
		"<button class=\"button\" type=\"submit\">Apply</button><button class=\"button secondary\" id=\"clear-audit-filter\" type=\"button\">Clear</button></form>" +
		"<div class=\"table-wrap\" style=\"margin-top:12px\"><table><thead><tr><th>Created</th><th>Event</th><th>Actor</th><th>Target</th><th>IP</th><th>Metadata</th></tr></thead><tbody id=\"audit-log-rows\"><tr><td colspan=\"6\" class=\"muted\">pending</td></tr></tbody></table></div></section>" +
		"<section class=\"section\" id=\"security-stats\"><div class=\"section-header\"><h2>Security stats</h2><button class=\"button secondary\" id=\"refresh-security\" type=\"button\">Refresh security</button></div><div class=\"subgrid\" id=\"security-summary\"><div class=\"stat\"><span>Login success 24h</span><strong>pending</strong></div></div><div class=\"table-wrap\" style=\"margin-top:12px\"><table><thead><tr><th>Event type</th><th>Count</th></tr></thead><tbody id=\"security-events\"><tr><td colspan=\"2\" class=\"muted\">pending</td></tr></tbody></table></div></section>" +
		"<section class=\"section\" id=\"ops-checks\"><div class=\"section-header\"><h2>Ops checks</h2><button class=\"button secondary\" id=\"refresh-ops\" type=\"button\">Refresh ops</button></div><div class=\"subgrid\" id=\"ops-summary\"><div class=\"stat\"><span>Dependency check</span><strong>pending</strong></div><div class=\"stat\"><span>Consistency check</span><strong>pending</strong></div></div><div class=\"two-col\" style=\"margin-top:12px\"><div class=\"panel\"><h3>Recent runs</h3><div class=\"table-wrap\"><table><thead><tr><th>Started</th><th>Type</th><th>Status</th><th>Artifacts</th></tr></thead><tbody id=\"ops-run-rows\"><tr><td colspan=\"4\" class=\"muted\">pending</td></tr></tbody></table></div></div><div class=\"panel\"><h3>Recent failures</h3><div class=\"table-wrap\"><table><thead><tr><th>Started</th><th>Type</th><th>Status</th><th>Summary</th></tr></thead><tbody id=\"ops-failure-rows\"><tr><td colspan=\"4\" class=\"muted\">pending</td></tr></tbody></table></div></div></div></section>" +
		"<section class=\"section\" id=\"notification-failures\"><div class=\"section-header\"><h2>Notification failures</h2><button class=\"button secondary\" id=\"refresh-notifications\" type=\"button\">Refresh notifications</button></div><div class=\"table-wrap\"><table><thead><tr><th>Created</th><th>User</th><th>Channel</th><th>Category</th><th>Type</th><th>Attempts</th><th>Error</th><th>Action</th></tr></thead><tbody id=\"notification-failure-rows\"><tr><td colspan=\"8\" class=\"muted\">pending</td></tr></tbody></table></div></section>" +
		"<section class=\"section\" id=\"health-readiness\"><div class=\"section-header\"><h2>Health/readiness</h2><button class=\"button secondary\" id=\"refresh-health\" type=\"button\">Refresh health</button></div><div class=\"probe-list\"><div class=\"probe\" id=\"health-probe\"><h3>/healthz</h3><span class=\"pill\">pending</span><pre>{}</pre></div><div class=\"probe\" id=\"ready-probe\"><h3>/readyz</h3><span class=\"pill\">pending</span><pre>{}</pre></div></div></section>" +
		"<div class=\"section\"><div class=\"subgrid\"><div class=\"stat\"><span>API prefix</span><strong class=\"mono\">" + htmlEscape(opts.APIPrefix) + "</strong></div><div class=\"stat\"><span>Admin stats</span><strong class=\"mono\">" + htmlEscape(opts.AdminPath) + "/stats</strong></div><div class=\"stat\"><span>Admin settings</span><strong class=\"mono\">" + htmlEscape(opts.AdminPath) + "/settings</strong></div><div class=\"stat\"><span>Security stats</span><strong class=\"mono\">" + htmlEscape(opts.APIPrefix) + "/admin/security/stats</strong></div><div class=\"stat\"><span>Notification failures</span><strong class=\"mono\">" + htmlEscape(opts.AdminPath) + "/notifications/failures</strong></div><div class=\"stat\"><span>Console route</span><strong class=\"mono\">" + htmlEscape(opts.ConsolePath) + "</strong></div><div class=\"stat\"><span>Overview route</span><strong class=\"mono\">" + htmlEscape(opts.OverviewPath) + "</strong></div></div></div>"
}

func pageFooter(opts Options) string {
	return "<footer><small>Support contact: " + htmlEscape(opts.SupportEmail) + " - <a href=\"/api/meta/web\">Web metadata</a> - <a href=\"/api/meta/version\">Version metadata</a> - <a href=\"" + htmlEscape(opts.ConsolePath) + "\">Console route</a></small></footer></main>"
}

func monoNumber(value int64) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatInt(value, 10)
}
