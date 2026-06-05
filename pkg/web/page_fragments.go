package web

import "strings"

func pageHead(opts Options) string {
	return "<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>" + htmlEscape(opts.AppName) + " Console</title><style>" + pageStyles() + "</style></head><body><main><section>"
}

func pageStyles() string {
	return strings.Join([]string{
		":root{--ink:#14213d;--muted:#415a77;--paper:rgba(255,255,255,.78);--line:rgba(20,33,61,.08);--accent:#1f6f78;--accent-soft:#d7efe8;--danger:#b42318;--success:#027a48;}",
		"*{box-sizing:border-box}",
		"body{margin:0;font-family:Segoe UI,Helvetica,Arial,sans-serif;background:radial-gradient(circle at top left,#f7efe2 0%,transparent 34%),linear-gradient(135deg,#f4efe6 0%,#dce9f2 100%);color:var(--ink);}",
		"main{max-width:1180px;margin:0 auto;padding:48px 24px 72px;}",
		"section{background:var(--paper);backdrop-filter:blur(10px);border:1px solid var(--line);border-radius:28px;padding:32px;box-shadow:0 24px 80px rgba(20,33,61,.12);}",
		"header{display:flex;justify-content:space-between;gap:24px;align-items:flex-start;margin-bottom:24px;flex-wrap:wrap}",
		".eyebrow{display:inline-flex;padding:8px 12px;border-radius:999px;background:var(--accent-soft);color:var(--accent);font-size:.8rem;font-weight:700;letter-spacing:.04em;text-transform:uppercase}",
		"h1{font-size:clamp(2.4rem,5vw,4.8rem);line-height:1.02;margin:14px 0 16px;}",
		"p{font-size:1.05rem;line-height:1.7;max-width:720px;}",
		"ul{padding-left:20px;line-height:1.9;}",
		".grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:16px;margin:28px 0}",
		".card,.panel{padding:18px;border-radius:18px;background:rgba(255,255,255,.7);border:1px solid var(--line)}",
		".card{min-height:104px}",
		".label{font-size:.8rem;letter-spacing:.04em;text-transform:uppercase;color:var(--muted);margin-bottom:6px}",
		".value{font-size:1.05rem;font-weight:700}",
		".muted{color:var(--muted)}",
		".stack{display:grid;gap:16px}",
		".layout{display:grid;grid-template-columns:1.3fr .9fr;gap:18px;margin-top:18px}",
		".actions{display:flex;gap:12px;flex-wrap:wrap;margin-top:24px}",
		"a.button,button.button{display:inline-block;padding:14px 22px;border-radius:999px;background:var(--ink);color:#fff;text-decoration:none;font-weight:600;border:none;cursor:pointer;}",
		"a.subtle,button.subtle{background:transparent;color:var(--ink);border:1px solid var(--line)}",
		"pre{margin:0;padding:18px;border-radius:20px;background:#101828;color:#d0d5dd;overflow:auto;font-size:.92rem;line-height:1.5}",
		".endpoint-list{display:grid;gap:10px;margin:0;padding:0;list-style:none}",
		".endpoint-list li{display:flex;justify-content:space-between;gap:16px;padding:12px 14px;border-radius:14px;background:rgba(255,255,255,.62);border:1px solid var(--line);align-items:center}",
		".status{display:inline-flex;align-items:center;gap:8px;font-weight:600}",
		".dot{width:10px;height:10px;border-radius:50%;background:#98a2b3}",
		".dot.ok{background:var(--success)}",
		".dot.warn{background:#f79009}",
		".dot.err{background:var(--danger)}",
		"#status-banner{margin-top:14px;padding:14px 16px;border-radius:16px;background:#fff7ed;border:1px solid #fed7aa;color:#9a3412;display:none}",
		"#status-banner.show{display:block}",
		".metric-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}",
		".metric{padding:14px;border-radius:16px;background:#fff;border:1px solid var(--line)}",
		".metric strong{display:block;font-size:1.35rem;margin-top:4px}",
		"code.inline{font-family:Consolas,monospace;padding:2px 6px;border-radius:999px;background:#eef2f6}",
		"small{display:block;margin-top:28px;color:var(--muted);}",
		"@media (max-width:920px){.layout{grid-template-columns:1fr}}",
		"@media (max-width:720px){main{padding:28px 16px 48px;}section{padding:22px;border-radius:20px;}.metric-grid{grid-template-columns:1fr}}",
	}, "")
}

func pageHero(opts Options, metaJSON []byte) string {
	return "<header><div><span class=\"eyebrow\">" + htmlEscape(strings.ToUpper(opts.Edition)) + " edition</span><h1>" + htmlEscape(opts.AppName) + " console shell</h1><p>This server already exposes the HistorySync API. This shared console shell now probes runtime endpoints directly, so CE and EE can evolve one backend-owned web surface before a dedicated front-end build replaces it.</p></div><pre>" + htmlEscape(string(metaJSON)) + "</pre></header>"
}

func pageOverviewCards(opts Options) string {
	return "<div class=\"grid\">" +
		"<div class=\"card\"><div class=\"label\">Health</div><div class=\"value\">/healthz and /readyz</div></div>" +
		"<div class=\"card\"><div class=\"label\">API prefix</div><div class=\"value\">" + htmlEscape(opts.APIPrefix) + "</div></div>" +
		"<div class=\"card\"><div class=\"label\">Console route</div><div class=\"value\">" + htmlEscape(opts.ConsolePath) + "</div></div>" +
		"<div class=\"card\"><div class=\"label\">Admin route</div><div class=\"value\">" + htmlEscape(opts.AdminPath) + "</div></div>" +
		"<div class=\"card\"><div class=\"label\">Overview route</div><div class=\"value\">" + htmlEscape(opts.OverviewPath) + "</div></div>" +
		"</div>"
}

func pageConsoleLayout(opts Options) string {
	return "<div id=\"status-banner\"></div>" +
		"<div class=\"layout\">" +
		"<div class=\"stack\">" +
		"<div class=\"panel\"><div class=\"label\">Runtime checks</div><ul class=\"endpoint-list\" id=\"runtime-checks\">" +
		"<li><span>Web metadata</span><span class=\"status\" data-check=\"meta\"><span class=\"dot\"></span>pending</span></li>" +
		"<li><span>Health probe</span><span class=\"status\" data-check=\"health\"><span class=\"dot\"></span>pending</span></li>" +
		"<li><span>Readiness probe</span><span class=\"status\" data-check=\"ready\"><span class=\"dot\"></span>pending</span></li>" +
		"<li><span>Overview API</span><span class=\"status\" data-check=\"overview\"><span class=\"dot\"></span>pending</span></li>" +
		"<li><span>Admin surface</span><span class=\"status\" data-check=\"admin\"><span class=\"dot\"></span>pending</span></li>" +
		"</ul><div class=\"actions\"><button class=\"button\" id=\"refresh-checks\" type=\"button\">Refresh probes</button><a class=\"button subtle\" href=\"" + htmlEscape(opts.ConsolePath) + "\">Reload console route</a></div></div>" +
		"<div class=\"panel\"><div class=\"label\">Platform contract</div><ul><li>Public web mount stays separate from product APIs</li><li>Future SPA assets can be served here without moving backend routes</li><li>Both CE and EE read the same meta contract from <code class=\"inline\">/api/meta/web</code></li></ul></div>" +
		"</div>" +
		"<div class=\"stack\">" +
		"<div class=\"panel\"><div class=\"label\">Quick metrics</div><div class=\"metric-grid\">" +
		"<div class=\"metric\"><span class=\"muted\">Edition</span><strong id=\"metric-edition\">" + htmlEscape(opts.Edition) + "</strong></div>" +
		"<div class=\"metric\"><span class=\"muted\">API prefix</span><strong id=\"metric-prefix\">" + htmlEscape(opts.APIPrefix) + "</strong></div>" +
		"<div class=\"metric\"><span class=\"muted\">Health status</span><strong id=\"metric-health\">pending</strong></div>" +
		"<div class=\"metric\"><span class=\"muted\">Admin status</span><strong id=\"metric-admin\">pending</strong></div>" +
		"<div class=\"metric\"><span class=\"muted\">Overview users</span><strong id=\"metric-users\">pending</strong></div>" +
		"<div class=\"metric\"><span class=\"muted\">Overview storage</span><strong id=\"metric-storage\">pending</strong></div>" +
		"<div class=\"metric\"><span class=\"muted\">Overview active users</span><strong id=\"metric-active-users\">pending</strong></div>" +
		"<div class=\"metric\"><span class=\"muted\">Overview cost</span><strong id=\"metric-cost\">pending</strong></div>" +
		"</div></div>" +
		"<div class=\"panel\"><div class=\"label\">Priority routes</div><ul class=\"endpoint-list\">" +
		"<li><span>Quota API</span><span>" + htmlEscape(opts.APIPrefix) + "/quota</span></li>" +
		"<li><span>Billing extension seam</span><span>" + htmlEscape(opts.APIPrefix) + "/billing/*</span></li>" +
		"<li><span>Admin overview</span><span>" + htmlEscape(opts.AdminPath) + "/stats</span></li>" +
		"</ul></div>" +
		"</div></div>"
}

func pageFooter(opts Options) string {
	return "<div class=\"actions\"><a class=\"button\" href=\"" + htmlEscape(opts.APIPrefix) + "/quota\">Inspect protected API route</a><a class=\"button subtle\" href=\"/api/meta/web\">Read web metadata</a></div><small>Support contact: " + htmlEscape(opts.SupportEmail) + "</small></section></main>"
}
