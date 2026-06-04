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
OverviewPath string
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
if strings.TrimSpace(opts.OverviewPath) == "" {
if strings.EqualFold(strings.TrimSpace(opts.Edition), "enterprise") {
opts.OverviewPath = "/api/v1/meta/overview/enterprise"
} else {
opts.OverviewPath = "/api/meta/overview"
}
}
return opts
}

func landingPage(opts Options) string {
metaJSON, _ := json.Marshal(metaPayload(opts))
var builder strings.Builder
builder.WriteString(pageHead(opts))
builder.WriteString(pageHero(opts, metaJSON))
builder.WriteString(pageOverviewCards(opts))
builder.WriteString(pageConsoleLayout(opts))
builder.WriteString(pageFooter(opts))
builder.WriteString("<script>")
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
"overview_path": opts.OverviewPath,
"support_email": opts.SupportEmail,
}
}

func jsStringLiteral(input string) string {
escaped := strings.NewReplacer(
`\\`, `\\\\`,
`"`, `\\"`,
"\n", `\\n`,
"\r", `\\r`,
"\t", `\\t`,
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
const usersMetric=document.getElementById("metric-users");
const storageMetric=document.getElementById("metric-storage");
const activeUsersMetric=document.getElementById("metric-active-users");
const costMetric=document.getElementById("metric-cost");

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
{name:'overview',url:%s,ok:[200]},
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

if(check.name==='overview'){
try{
const body=await response.clone().json();
const summary=body.summary||{};
usersMetric.textContent=String(summary.total_users??'n/a');
storageMetric.textContent=String(summary.total_storage_bytes??'n/a');
activeUsersMetric.textContent=String(summary.monthly_active_users??summary.active_devices??'n/a');
costMetric.textContent=String(summary.estimated_cost_usd??'n/a');
}catch(_err){
usersMetric.textContent='unavailable';
storageMetric.textContent='unavailable';
activeUsersMetric.textContent='unavailable';
costMetric.textContent='unavailable';
}
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
if(check.name==='overview'){
usersMetric.textContent='offline';
storageMetric.textContent='offline';
activeUsersMetric.textContent='offline';
costMetric.textContent='offline';
}
setBanner('At least one runtime probe failed. Inspect server logs and infrastructure dependencies.');
}
}
}

document.getElementById('refresh-checks').addEventListener('click',probe);
probe();
})();`, jsStringLiteral(opts.APIPrefix), jsStringLiteral(opts.AdminPath), jsStringLiteral(opts.OverviewPath))
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