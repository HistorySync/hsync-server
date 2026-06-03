package web

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// Options configures the lightweight web surface mounted on top of the API.
type Options struct {
	Enabled      bool
	AppName      string
	ConsolePath  string
	SupportEmail string
	Edition      string
	APIPrefix    string
	AdminPath    string
}

// Register mounts a minimal HTML landing page and console placeholder.
// This gives CE and EE one shared web entrypoint that can later be upgraded
// to serve compiled assets without changing route ownership again.
func Register(app *fiber.App, opts Options) {
	if !opts.Enabled {
		return
	}

	opts = normalizeOptions(opts)
	page := landingPage(opts)
	meta := metaPayload(opts)

	app.Get("/", func(c fiber.Ctx) error {
		c.Type("html", "utf-8")
		return c.SendString(page)
	})

	app.Get(opts.ConsolePath, func(c fiber.Ctx) error {
		c.Type("html", "utf-8")
		return c.SendString(page)
	})

	app.Get("/api/meta/web", func(c fiber.Ctx) error {
		return c.JSON(meta)
	})
}

func normalizeOptions(opts Options) Options {
	if strings.TrimSpace(opts.AppName) == "" {
		opts.AppName = "HistorySync"
	}
	if strings.TrimSpace(opts.ConsolePath) == "" {
		opts.ConsolePath = "/console"
	}
	if strings.TrimSpace(opts.SupportEmail) == "" {
		opts.SupportEmail = "support@historysync.app"
	}
	if strings.TrimSpace(opts.Edition) == "" {
		opts.Edition = "community"
	}
	if strings.TrimSpace(opts.APIPrefix) == "" {
		opts.APIPrefix = "/api/v1"
	}
	if strings.TrimSpace(opts.AdminPath) == "" {
		opts.AdminPath = "/admin"
	}
	return opts
}

func landingPage(opts Options) string {
	metaJSON, _ := json.Marshal(metaPayload(opts))
	apiPrefixEscaped := htmlEscape(opts.APIPrefix)
	adminPathEscaped := htmlEscape(opts.AdminPath)
	consolePathEscaped := htmlEscape(opts.ConsolePath)
	var builder strings.Builder
	builder.WriteString("<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	builder.WriteString("<title>")
	builder.WriteString(htmlEscape(opts.AppName))
	builder.WriteString(" Console</title><style>")
	builder.WriteString(":root{--ink:#14213d;--muted:#415a77;--paper:rgba(255,255,255,.78);--line:rgba(20,33,61,.08);--accent:#1f6f78;--accent-soft:#d7efe8;--danger:#b42318;--success:#027a48;}*{box-sizing:border-box}body{margin:0;font-family:Segoe UI,Helvetica,Arial,sans-serif;background:radial-gradient(circle at top left,#f7efe2 0%,transparent 34%),linear-gradient(135deg,#f4efe6 0%,#dce9f2 100%);color:var(--ink);}main{max-width:1180px;margin:0 auto;padding:48px 24px 72px;}section{background:var(--paper);backdrop-filter:blur(10px);border:1px solid var(--line);border-radius:28px;padding:32px;box-shadow:0 24px 80px rgba(20,33,61,.12);}header{display:flex;justify-content:space-between;gap:24px;align-items:flex-start;margin-bottom:24px;flex-wrap:wrap}.eyebrow{display:inline-flex;padding:8px 12px;border-radius:999px;background:var(--accent-soft);color:var(--accent);font-size:.8rem;font-weight:700;letter-spacing:.04em;text-transform:uppercase}h1{font-size:clamp(2.4rem,5vw,4.8rem);line-height:1.02;margin:14px 0 16px;}p{font-size:1.05rem;line-height:1.7;max-width:720px;}ul{padding-left:20px;line-height:1.9;}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:16px;margin:28px 0}.card,.panel{padding:18px;border-radius:18px;background:rgba(255,255,255,.7);border:1px solid var(--line)}.card{min-height:104px}.label{font-size:.8rem;letter-spacing:.04em;text-transform:uppercase;color:var(--muted);margin-bottom:6px}.value{font-size:1.05rem;font-weight:700}.muted{color:var(--muted)}.stack{display:grid;gap:16px}.layout{display:grid;grid-template-columns:1.3fr .9fr;gap:18px;margin-top:18px}.actions{display:flex;gap:12px;flex-wrap:wrap;margin-top:24px}a.button,button.button{display:inline-block;padding:14px 22px;border-radius:999px;background:var(--ink);color:#fff;text-decoration:none;font-weight:600;border:none;cursor:pointer;}a.subtle,button.subtle{background:transparent;color:var(--ink);border:1px solid var(--line)}pre{margin:0;padding:18px;border-radius:20px;background:#101828;color:#d0d5dd;overflow:auto;font-size:.92rem;line-height:1.5}.endpoint-list{display:grid;gap:10px;margin:0;padding:0;list-style:none}.endpoint-list li{display:flex;justify-content:space-between;gap:16px;padding:12px 14px;border-radius:14px;background:rgba(255,255,255,.62);border:1px solid var(--line);align-items:center}.status{display:inline-flex;align-items:center;gap:8px;font-weight:600}.dot{width:10px;height:10px;border-radius:50%;background:#98a2b3}.dot.ok{background:var(--success)}.dot.warn{background:#f79009}.dot.err{background:var(--danger)}#status-banner{margin-top:14px;padding:14px 16px;border-radius:16px;background:#fff7ed;border:1px solid #fed7aa;color:#9a3412;display:none}#status-banner.show{display:block}.metric-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.metric{padding:14px;border-radius:16px;background:#fff;border:1px solid var(--line)}.metric strong{display:block;font-size:1.35rem;margin-top:4px}code.inline{font-family:Consolas,monospace;padding:2px 6px;border-radius:999px;background:#eef2f6}small{display:block;margin-top:28px;color:var(--muted);}@media (max-width:920px){.layout{grid-template-columns:1fr}}@media (max-width:720px){main{padding:28px 16px 48px;}section{padding:22px;border-radius:20px;}.metric-grid{grid-template-columns:1fr}}</style></head><body><main><section>")
	builder.WriteString("<header><div><span class=\"eyebrow\">")
	builder.WriteString(htmlEscape(strings.ToUpper(opts.Edition)))
	builder.WriteString(" edition</span>")
	builder.WriteString("<h1>")
	builder.WriteString(htmlEscape(opts.AppName))
	builder.WriteString(" console shell</h1><p>This server already exposes the HistorySync API. This shared console shell now probes runtime endpoints directly, so CE and EE can evolve one backend-owned web surface before a dedicated front-end build replaces it.</p></div><pre>")
	builder.WriteString(htmlEscape(string(metaJSON)))
	builder.WriteString("</pre></header><div class=\"grid\"><div class=\"card\"><div class=\"label\">Health</div><div class=\"value\">/healthz and /readyz</div></div><div class=\"card\"><div class=\"label\">API prefix</div><div class=\"value\">")
	builder.WriteString(htmlEscape(opts.APIPrefix))
	builder.WriteString("</div></div><div class=\"card\"><div class=\"label\">Console route</div><div class=\"value\">")
	builder.WriteString(htmlEscape(opts.ConsolePath))
	builder.WriteString("</div></div><div class=\"card\"><div class=\"label\">Admin route</div><div class=\"value\">")
	builder.WriteString(adminPathEscaped)
	builder.WriteString("</div></div></div><div id=\"status-banner\"></div><div class=\"layout\"><div class=\"stack\"><div class=\"panel\"><div class=\"label\">Runtime checks</div><ul class=\"endpoint-list\" id=\"runtime-checks\"><li><span>Web metadata</span><span class=\"status\" data-check=\"meta\"><span class=\"dot\"></span>pending</span></li><li><span>Health probe</span><span class=\"status\" data-check=\"health\"><span class=\"dot\"></span>pending</span></li><li><span>Readiness probe</span><span class=\"status\" data-check=\"ready\"><span class=\"dot\"></span>pending</span></li><li><span>Admin surface</span><span class=\"status\" data-check=\"admin\"><span class=\"dot\"></span>pending</span></li></ul><div class=\"actions\"><button class=\"button\" id=\"refresh-checks\" type=\"button\">Refresh probes</button><a class=\"button subtle\" href=\"")
	builder.WriteString(consolePathEscaped)
	builder.WriteString("\">Reload console route</a></div></div><div class=\"panel\"><div class=\"label\">Platform contract</div><ul><li>Public web mount stays separate from product APIs</li><li>Future SPA assets can be served here without moving backend routes</li><li>Both CE and EE read the same meta contract from <code class=\"inline\">/api/meta/web</code></li></ul></div></div><div class=\"stack\"><div class=\"panel\"><div class=\"label\">Quick metrics</div><div class=\"metric-grid\"><div class=\"metric\"><span class=\"muted\">Edition</span><strong id=\"metric-edition\">")
	builder.WriteString(htmlEscape(opts.Edition))
	builder.WriteString("</strong></div><div class=\"metric\"><span class=\"muted\">API prefix</span><strong id=\"metric-prefix\">")
	builder.WriteString(apiPrefixEscaped)
	builder.WriteString("</strong></div><div class=\"metric\"><span class=\"muted\">Health status</span><strong id=\"metric-health\">pending</strong></div><div class=\"metric\"><span class=\"muted\">Admin status</span><strong id=\"metric-admin\">pending</strong></div></div></div><div class=\"panel\"><div class=\"label\">Priority routes</div><ul class=\"endpoint-list\"><li><span>Quota API</span><span>")
	builder.WriteString(htmlEscape(opts.APIPrefix))
	builder.WriteString("/quota</span></li><li><span>Billing webhook</span><span>")
	builder.WriteString(htmlEscape(opts.APIPrefix))
	builder.WriteString("/billing/webhook</span></li><li><span>Admin overview</span><span>")
	builder.WriteString(adminPathEscaped)
	builder.WriteString("/stats</span></li></ul></div></div></div><div class=\"actions\"><a class=\"button\" href=\"")
	builder.WriteString(apiPrefixEscaped)
	builder.WriteString("/quota\">Inspect protected API route</a><a class=\"button subtle\" href=\"/api/meta/web\">Read web metadata</a></div><small>Support contact: ")
	builder.WriteString(htmlEscape(opts.SupportEmail))
	builder.WriteString("</small></section></main><script>")
	builder.WriteString(consoleScript(opts))
	builder.WriteString("</script></body></html>")
	return builder.String()
}

func metaPayload(opts Options) fiber.Map {
	return fiber.Map{
		"app_name":      opts.AppName,
		"edition":       opts.Edition,
		"console_path":  opts.ConsolePath,
		"api_prefix":    opts.APIPrefix,
		"admin_path":    opts.AdminPath,
		"support_email": opts.SupportEmail,
	}
}

func jsStringLiteral(input string) string {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	).Replace(input)
	return fmt.Sprintf(`"%s"`, escaped)
}

func consoleScript(opts Options) string {
	return fmt.Sprintf(`(function(){
const apiPrefix=%s;
const adminPath=%s;
const banner=document.getElementById("status-banner");
const healthMetric=document.getElementById("metric-health");
const adminMetric=document.getElementById("metric-admin");

function setCheck(name,state,text){
	const node=document.querySelector('[data-check="'+name+'"]');
	if(!node){return;}
	const dot=node.querySelector('.dot');
	node.lastChild.textContent=' '+text;
	dot.className='dot '+state;
}

function setBanner(message){
	if(!message){
		banner.textContent='';
		banner.className='';
		return;
	}
	banner.textContent=message;
	banner.className='show';
}

function healthLabel(status){
	if(!status){return 'unknown';}
	return status;
}

async function probe(){
	setBanner('');
	const checks=[
		{name:'meta',url:'/api/meta/web',ok:[200]},
		{name:'health',url:'/healthz',ok:[200]},
		{name:'ready',url:'/readyz',ok:[200,503]},
		{name:'admin',url:adminPath+'/stats',ok:[200,401,403]},
	];

	for(const check of checks){
		try{
			const response=await fetch(check.url,{headers:{Accept:'application/json'}});
			const allowed=check.ok.includes(response.status);
			setCheck(check.name,allowed?'ok':'warn',allowed?('HTTP '+response.status):('unexpected HTTP '+response.status));

			if(check.name==='health'){
				let label='HTTP '+response.status;
				try{
					const body=await response.clone().json();
					label=healthLabel(body.status);
				}catch(_err){}
				healthMetric.textContent=label;
			}

			if(check.name==='admin'){
				const label=response.status===200?'reachable':(response.status===401||response.status===403?'protected':'HTTP '+response.status);
				adminMetric.textContent=label;
			}

			if(check.name==='ready'&&response.status===503){
				setBanner('Readiness probe reports degraded or unhealthy dependencies. Review database, redis, or blob storage connectivity.');
			}
		}catch(error){
			setCheck(check.name,'err','request failed');
			if(check.name==='health'){
				healthMetric.textContent='offline';
			}
			if(check.name==='admin'){
				adminMetric.textContent='unreachable';
			}
			setBanner('At least one runtime probe failed. Inspect server logs and infrastructure dependencies.');
		}
	}
}

document.getElementById('refresh-checks').addEventListener('click',probe);
probe();
})();`, jsStringLiteral(opts.APIPrefix), jsStringLiteral(opts.AdminPath))
}

func htmlEscape(input string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(input)
}
