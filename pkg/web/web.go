package web

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/historysync/hsync-server/pkg/buildinfo"
)

// Options configures the lightweight web surface mounted on top of the API.
type Options struct {
	Enabled           bool
	AppName           string
	ConsolePath       string
	SupportEmail      string
	Edition           string
	BuildInfo         buildinfo.Info
	APIPrefix         string
	AdminPath         string
	OverviewPath      string
	ExtraNavHTML      string
	ExtraSectionsHTML string
	ExtraScript       string
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
	if opts.BuildInfo.Version == "" && opts.BuildInfo.Commit == "" && opts.BuildInfo.BuildTime == "" && opts.BuildInfo.Edition == "" && opts.BuildInfo.SchemaVersion == 0 {
		opts.BuildInfo = buildinfo.WithEdition(opts.Edition)
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
		"build_info":    opts.BuildInfo,
		"console_path":  opts.ConsolePath,
		"api_prefix":    opts.APIPrefix,
		"admin_path":    opts.AdminPath,
		"overview_path": opts.OverviewPath,
		"support_email": opts.SupportEmail,
	}
}

func jsStringLiteral(input string) string {
	encoded, _ := json.Marshal(input)
	return string(encoded)
}

func consoleScript(opts Options) string {
	return fmt.Sprintf(`(function(){
const apiPrefix=%s;
const adminPath=%s;
const overviewPath=%s;
const versionPath="/api/meta/version";
const adminKeyInput=document.getElementById("admin-key");
const statusText=document.getElementById("console-status");
const banner=document.getElementById("status-banner");

function text(id,value){
const node=document.getElementById(id);
if(node){node.textContent=value;}
}

function setStatus(message){
if(statusText){statusText.textContent=message;}
}

function setBanner(message,tone){
if(!banner){return;}
banner.textContent=message||"";
banner.className=message?("banner show "+(tone||"")):"banner";
}

function adminHeaders(){
const headers={Accept:"application/json"};
const key=(adminKeyInput&&adminKeyInput.value||"").trim();
if(key){headers["X-Admin-Key"]=key;}
return headers;
}

function writeJSON(node,value){
node.textContent=JSON.stringify(value||{},null,2);
}

async function requestJSON(url,options){
const response=await fetch(url,Object.assign({headers:{Accept:"application/json"}},options||{}));
const raw=await response.text();
let body={};
if(raw){
try{body=JSON.parse(raw);}catch(_err){body={raw:raw};}
}
if(!response.ok){
const message=(body.error&&body.error.message)||body.message||("HTTP "+response.status);
const error=new Error(message);
error.status=response.status;
error.body=body;
throw error;
}
return {status:response.status,body:body,headers:response.headers};
}

async function requestAdmin(url,options){
const headers=adminHeaders();
if(options&&options.headers){Object.assign(headers,options.headers);}
return requestJSON(url,Object.assign({},options||{},{headers:headers}));
}

function operatorError(error){
const body=error&&error.body||{};
const apiError=body.error||{};
const code=apiError.code||body.code||("HTTP "+(error&&error.status||"error"));
const message=apiError.message||body.message||(error&&error.message)||"request failed";
return code+": "+message;
}

function newIdempotencyKey(){
if(window.crypto&&window.crypto.randomUUID){return window.crypto.randomUUID();}
return "console-"+Date.now().toString(36)+"-"+Math.random().toString(36).slice(2);
}

function numberValue(value){
if(value===null||value===undefined||value===""){return "n/a";}
if(typeof value==="number"){return value.toLocaleString();}
return String(value);
}

function bytesValue(value){
if(typeof value!=="number"){return numberValue(value);}
const units=["B","KB","MB","GB","TB"];
let size=value;
let index=0;
while(size>=1024&&index<units.length-1){size=size/1024;index++;}
return (index===0?String(size):size.toFixed(1))+" "+units[index];
}

function dateValue(value){
if(!value){return "n/a";}
const date=new Date(value);
if(Number.isNaN(date.getTime())){return String(value);}
return date.toLocaleString();
}

function shortID(value){
if(!value){return "n/a";}
const text=String(value);
return text.length>18?text.slice(0,8)+"..."+text.slice(-6):text;
}

function makeCell(value,className){
const td=document.createElement("td");
if(className){td.className=className;}
td.textContent=value===null||value===undefined||value===""?"n/a":String(value);
return td;
}

function emptyRow(tbody,columns,message){
tbody.textContent="";
const tr=document.createElement("tr");
const td=document.createElement("td");
td.colSpan=columns;
td.className="muted";
td.textContent=message;
tr.appendChild(td);
tbody.appendChild(tr);
}

function renderKeyValues(container,obj){
container.textContent="";
const entries=Object.entries(obj||{});
if(entries.length===0){
container.textContent="none";
return;
}
for(const entry of entries){
const pill=document.createElement("span");
pill.className="pill";
pill.textContent=entry[0]+": "+numberValue(entry[1]);
container.appendChild(pill);
container.appendChild(document.createTextNode(" "));
}
}

async function loadBuildInfo(){
const response=await requestJSON(versionPath);
renderBuildInfo(response.body&&response.body.build_info||{});
}

function renderBuildInfo(info){
text("build-version",info.version||"dev");
text("build-commit",shortID(info.commit));
text("build-time",dateValue(info.build_time));
text("build-edition",info.edition||"community");
text("build-schema-version",numberValue(info.schema_version));
const root=document.getElementById("build-info-detail");
if(root){renderKeyValues(root,info);}
}

async function loadOverview(){
const stats=await requestAdmin(adminPath+"/stats");
const body=stats.body||{};
text("metric-total-users",numberValue(body.users&&body.users.total));
text("metric-active-devices",numberValue(body.devices&&body.devices.active));
text("metric-bundles",numberValue(body.bundles&&body.bundles.total));
text("metric-snapshots",numberValue(body.snapshots&&body.snapshots.total));
text("metric-storage",bytesValue(body.storage&&body.storage.total_bytes));
const websocket=body.websocket||{};
text("metric-websocket",numberValue(websocket.active_connections)+" connections");
renderKeyValues(document.getElementById("users-status-breakdown"),body.users&&body.users.by_status);

try{
const users=await requestAdmin(adminPath+"/users?limit=5");
renderUsers(users.body.users||[]);
}catch(error){
emptyRow(document.getElementById("recent-users"),4,error.status===401||error.status===403?"admin key required":error.message);
}
}

function renderUsers(users){
const tbody=document.getElementById("recent-users");
if(!tbody){return;}
tbody.textContent="";
if(!users.length){
emptyRow(tbody,4,"no users");
return;
}
for(const user of users){
const tr=document.createElement("tr");
tr.appendChild(makeCell(user.email||user.id));
tr.appendChild(makeCell(user.tier));
tr.appendChild(makeCell(user.status));
tr.appendChild(makeCell(dateValue(user.created_at)));
tbody.appendChild(tr);
}
}

async function loadSettings(){
const response=await requestAdmin(adminPath+"/settings");
renderSettings(response.body.settings||[]);
}

function renderSettings(settings){
const root=document.getElementById("settings-groups");
root.textContent="";
if(!settings.length){
root.appendChild(document.createTextNode("no settings"));
return;
}
const groups={};
for(const setting of settings){
const group=setting.group||"other";
if(!groups[group]){groups[group]=[];}
groups[group].push(setting);
}
for(const groupName of Object.keys(groups).sort()){
const group=document.createElement("div");
group.className="settings-group";
const heading=document.createElement("h3");
heading.textContent=groupName;
group.appendChild(heading);
for(const setting of groups[groupName]){
group.appendChild(settingRow(setting));
}
root.appendChild(group);
}
}

function settingRow(setting){
const row=document.createElement("div");
row.className="setting-row";

const summary=document.createElement("div");
const key=document.createElement("div");
key.className="setting-key mono";
key.textContent=setting.key;
summary.appendChild(key);
const description=document.createElement("p");
description.textContent=setting.description||"";
summary.appendChild(description);
const meta=document.createElement("div");
meta.className="setting-meta";
for(const label of [setting.value_type,setting.is_set?"override":"default",setting.requires_restart?"restart required":"live",setting.sensitive?"sensitive":"visible"]){
const pill=document.createElement("span");
pill.className="pill"+(label==="sensitive"?" warn":"");
pill.textContent=label;
meta.appendChild(pill);
}
summary.appendChild(meta);

const current=document.createElement("div");
const currentLabel=document.createElement("label");
currentLabel.textContent="Current value";
current.appendChild(currentLabel);
const currentValue=document.createElement("div");
currentValue.className="value-mask";
if(setting.sensitive){
currentValue.textContent=setting.is_set?"masked override":"not set";
}else if(setting.value===""){
currentValue.textContent="empty";
currentValue.className+=" muted";
}else{
currentValue.textContent=String(setting.value);
}
current.appendChild(currentValue);

const editor=document.createElement("form");
editor.className="setting-editor";
editor.dataset.key=setting.key;
let input;
if(setting.value_type==="bool"){
input=document.createElement("select");
for(const value of ["true","false"]){
const option=document.createElement("option");
option.value=value;
option.textContent=value;
input.appendChild(option);
}
input.value=String(setting.value||"false").toLowerCase()==="true"?"true":"false";
}else if(setting.value_type==="string"){
input=document.createElement(setting.sensitive?"input":"textarea");
if(setting.sensitive){input.type="password";}
input.value=setting.sensitive?"":String(setting.value||"");
input.placeholder=setting.sensitive?"Enter replacement value":"";
}else{
input=document.createElement("input");
input.value=String(setting.value||"");
	input.disabled=true;
}
input.name="value";
input.setAttribute("aria-label","Setting value for "+setting.key);
const save=document.createElement("button");
save.className="button";
save.type="submit";
save.textContent="Save";
save.setAttribute("aria-label","Save "+setting.key);
if(setting.value_type!=="bool"&&setting.value_type!=="string"){save.disabled=true;}
editor.appendChild(input);
editor.appendChild(save);
if(setting.value_type!=="bool"&&setting.value_type!=="string"){
const note=document.createElement("span");
note.className="field-note";
note.textContent="Read-only";
editor.appendChild(note);
}
editor.addEventListener("submit",async function(event){
event.preventDefault();
await saveSetting(setting.key,input.value,save);
});

row.appendChild(summary);
row.appendChild(current);
row.appendChild(editor);
return row;
}

async function saveSetting(key,value,button){
button.disabled=true;
try{
await requestAdmin(adminPath+"/settings/"+encodeURIComponent(key),{
method:"PUT",
headers:{"Content-Type":"application/json"},
body:JSON.stringify({value:value})
});
setBanner("Updated setting "+key,"");
await loadSettings();
}catch(error){
setBanner("Setting update failed: "+error.message,"err");
}finally{
button.disabled=false;
}
}

async function loadAuditLogs(){
const params=new URLSearchParams({limit:"50"});
for(const field of ["event_type","actor_user_id","target_type","target_id"]){
const input=document.querySelector('#audit-filter [name="'+field+'"]');
const value=input&&input.value.trim();
if(value){params.set(field,value);}
}
const response=await requestAdmin(adminPath+"/audit-logs?"+params.toString());
renderAuditLogs(response.body.audit_logs||[]);
}

function renderAuditLogs(logs){
const tbody=document.getElementById("audit-log-rows");
tbody.textContent="";
	if(!logs.length){
emptyRow(tbody,6,"no audit logs");
return;
}
for(const item of logs){
const tr=document.createElement("tr");
tr.appendChild(makeCell(dateValue(item.created_at)));
tr.appendChild(makeCell(item.event_type,"mono"));
tr.appendChild(makeCell(shortID(item.actor_user_id),"mono"));
tr.appendChild(makeCell((item.target_type||"n/a")+" / "+(item.target_id||"n/a"),"mono"));
tr.appendChild(makeCell(item.ip||"n/a","mono"));
tr.appendChild(makeCell(JSON.stringify(item.metadata||{}), "mono json-cell"));
tbody.appendChild(tr);
}
}

async function loadSupportTimelineLookup(){
const form=document.getElementById("support-timeline-form");
const params=new URLSearchParams();
for(const field of ["user_id","email","limit"]){
const input=form&&form.querySelector('[name="'+field+'"]');
const value=input&&input.value.trim();
if(value){params.set(field,value);}
}
const response=await requestAdmin(adminPath+"/support/context?"+params.toString());
renderSupportTimelineLookup(response.body.context||null);
setBanner("Timeline lookup loaded and audited.","");
}

function renderSupportTimelineLookup(context){
text("support-timeline-user-card",context&&context.user?(context.user.email||shortID(context.user.id)):"no match");
const erasure=context&&context.erasure_status||{};
let erasureState="not requested";
if(erasure.completed){erasureState="completed";}
else if(erasure.in_progress){erasureState="in progress";}
else if(erasure.requested){erasureState="requested";}
text("support-timeline-erasure-card",erasureState);
text("support-timeline-job-card",context?numberValue((context.job_status||[]).length):"0");
text("support-timeline-action-card",context?numberValue((context.recent_actions||[]).length):"0");
renderSupportTimelineDetails(context);
renderSupportTimelineActions(context&&context.recent_actions||[]);
renderSupportTimelineJobs(context&&context.job_status||[]);
writeJSON(document.getElementById("support-timeline-json"),context||{status:"no lookup"});
}

function renderSupportTimelineDetails(context){
const grid=document.getElementById("support-timeline-detail-grid");
if(!grid){return;}
grid.textContent="";
if(!context){
grid.appendChild(detailItem("Lookup","No lookup yet"));
return;
}
const user=context.user||{};
const quota=context.quota||{};
const usage=quota.usage||{};
const limit=quota.effective_limit||{};
grid.appendChild(detailItem("User",user.email||user.id||"n/a"));
grid.appendChild(detailItem("Tier",user.tier||"n/a"));
grid.appendChild(detailItem("Status",user.status||"n/a"));
grid.appendChild(detailItem("Storage",quota.usage?bytesValue(usage.total_bytes||0)+" / "+bytesValue(limit.storage_limit_bytes||0):"n/a"));
grid.appendChild(detailItem("Devices",numberValue((context.devices||[]).length)));
grid.appendChild(detailItem("Erasure jobs",numberValue((context.erasure_jobs||[]).length)));
grid.appendChild(detailItem("Account changes",numberValue((context.account_changes||[]).length)));
grid.appendChild(detailItem("Timeline events",context.incident_timeline?numberValue(context.incident_timeline.total):"0"));
}

function detailItem(label,value){
const item=document.createElement("div");
item.className="detail-item";
const span=document.createElement("span");
span.textContent=label;
const strong=document.createElement("strong");
strong.textContent=value===null||value===undefined||value===""?"n/a":String(value);
item.appendChild(span);
item.appendChild(strong);
return item;
}

function renderSupportTimelineActions(actions){
const tbody=document.getElementById("support-timeline-action-rows");
if(!tbody){return;}
tbody.textContent="";
if(!actions.length){
emptyRow(tbody,4,"no recent actions");
return;
}
for(const action of actions){
const tr=document.createElement("tr");
tr.appendChild(makeCell(dateValue(action.created_at)));
tr.appendChild(makeCell(action.action,"mono"));
tr.appendChild(makeCell(action.category||"n/a"));
tr.appendChild(makeCell((action.target_type||"n/a")+" / "+(action.target_id||"n/a"),"mono"));
tbody.appendChild(tr);
}
}

function renderSupportTimelineJobs(jobs){
const tbody=document.getElementById("support-timeline-job-rows");
if(!tbody){return;}
tbody.textContent="";
if(!jobs.length){
emptyRow(tbody,4,"no job status");
return;
}
for(const job of jobs){
const tr=document.createElement("tr");
tr.appendChild(makeCell(dateValue(job.updated_at)));
tr.appendChild(makeCell(job.name,"mono"));
tr.appendChild(makeCell(job.status||"n/a"));
tr.appendChild(makeCell(job.detail||JSON.stringify(job.summary||{}),"mono json-cell"));
tbody.appendChild(tr);
}
}

async function loadSecurity(){
const response=await requestAdmin(apiPrefix+"/admin/security/stats");
renderSecurity(response.body||{});
}

function renderSecurity(stats){
const root=document.getElementById("security-summary");
root.textContent="";
const last24=stats.last_24h||{};
const last7=stats.last_7d||{};
const twoFactor=stats.two_factor||{};
const cards=[
["Login success 24h",last24.login_success],
["Login failures 24h",last24.login_failure],
["2FA failures 24h",last24.two_factor_challenge_failure],
["Login failures 7d",last7.login_failure],
["2FA enabled users",numberValue(twoFactor.enabled_users)+" / "+numberValue(twoFactor.total_users)],
["2FA enabled ratio",typeof twoFactor.enabled_ratio==="number"?(Math.round(twoFactor.enabled_ratio*100)+"%%"):"n/a"],
];
for(const card of cards){
const node=document.createElement("div");
node.className="stat";
const label=document.createElement("span");
label.textContent=card[0];
const strong=document.createElement("strong");
strong.textContent=numberValue(card[1]);
node.appendChild(label);
node.appendChild(strong);
root.appendChild(node);
}
const tbody=document.getElementById("security-events");
tbody.textContent="";
const events=stats.events_by_type||[];
if(!events.length){
emptyRow(tbody,2,"no security events");
return;
}
for(const event of events){
const tr=document.createElement("tr");
tr.appendChild(makeCell(event.event_type,"mono"));
tr.appendChild(makeCell(numberValue(event.count)));
tbody.appendChild(tr);
}
}

async function loadOps(){
const response=await requestAdmin(adminPath+"/ops/summary");
renderOps(response.body||{});
}

function opsStatusPill(status){
const span=document.createElement("span");
let tone="";
if(status==="ok"){tone=" ok";}
else if(status==="degraded"||status==="disabled"||status==="skipped"){tone=" warn";}
else if(status){tone=" err";}
span.className="pill"+tone;
span.textContent=status||"not checked";
return span;
}

function renderOps(summary){
const readiness=summary.readiness||{};
const root=document.getElementById("ops-summary");
root.textContent="";
const dep=readiness.last_dependency_check||{};
const consistency=readiness.last_consistency_check||{};
const cards=[
["Dependency check",dep.overall,dateValue(dep.checked_at)],
["Consistency check",consistency.overall,dateValue(consistency.checked_at)],
];
for(const card of cards){
const node=document.createElement("div");
node.className="stat";
const label=document.createElement("span");
label.textContent=card[0];
const strong=document.createElement("strong");
strong.textContent=card[1]||"not checked";
const note=document.createElement("span");
note.textContent=card[2];
node.appendChild(label);
node.appendChild(strong);
node.appendChild(note);
root.appendChild(node);
}
renderOpsRuns(document.getElementById("ops-run-rows"),readiness.recent_runs||[],false);
renderOpsRuns(document.getElementById("ops-failure-rows"),readiness.recent_failures||[],true);
}

function renderOpsRuns(tbody,items,includeSummary){
tbody.textContent="";
if(!items.length){
emptyRow(tbody,4,includeSummary?"no recent failed checks":"no recent checks");
return;
}
for(const item of items){
const tr=document.createElement("tr");
tr.appendChild(makeCell(dateValue(item.started_at)));
tr.appendChild(makeCell(item.run_type));
const status=document.createElement("td");
status.appendChild(opsStatusPill(item.overall_status));
tr.appendChild(status);
const detail=includeSummary?item.summarized_findings:item.artifact_counts;
tr.appendChild(makeCell(JSON.stringify(detail||{}),"mono json-cell"));
tbody.appendChild(tr);
}
}

async function loadNotifications(){
const response=await requestAdmin(adminPath+"/notifications/failures?limit=50");
renderNotifications(response.body.notifications||[]);
}

function renderNotifications(items){
const tbody=document.getElementById("notification-failure-rows");
const batch=document.getElementById("retry-visible-notifications");
if(batch){
batch.disabled=!items.length;
batch.dataset.limit=String(items.length||0);
}
tbody.textContent="";
if(!items.length){
emptyRow(tbody,8,"no failed notifications");
return;
}
for(const item of items){
const tr=document.createElement("tr");
tr.appendChild(makeCell(dateValue(item.created_at)));
tr.appendChild(makeCell(shortID(item.user_id),"mono"));
tr.appendChild(makeCell(item.channel));
tr.appendChild(makeCell(item.category));
tr.appendChild(makeCell(item.type));
tr.appendChild(makeCell(numberValue(item.attempt_count)));
tr.appendChild(makeCell(item.error_summary||"n/a"));
const action=document.createElement("td");
action.className="row-actions";
action.appendChild(notificationActionButton("Retry",item.id,"retry","secondary"));
action.appendChild(notificationActionButton("Requeue",item.id,"requeue","warn"));
action.appendChild(notificationActionButton("Discard",item.id,"discard","danger"));
tr.appendChild(action);
tbody.appendChild(tr);
}
}

function notificationActionButton(label,id,action,tone){
const button=document.createElement("button");
button.type="button";
button.className="button compact "+tone;
button.textContent=label;
button.dataset.notificationAction=action;
button.dataset.notificationId=id||"";
button.setAttribute("aria-label",label+" notification "+shortID(id));
button.addEventListener("click",function(){runNotificationAction(button,id,action,label);});
if(!id){button.disabled=true;}
return button;
}

function notificationActionSummary(action,body){
const result=body&&body.result?String(body.result):"completed";
const replayed=body&&body.replayed?"replayed response":"fresh response";
const counts=[];
for(const field of ["sent","failed","scheduled_retry","retried","requeued","discarded","skipped","not_found"]){
if(body&&body[field]){counts.push(field.replace("_"," ")+": "+body[field]);}
}
return action+" succeeded: "+result+" ("+replayed+")"+(counts.length?" - "+counts.join(", "):"");
}

async function mutateNotificationFailure(url,body){
return requestAdmin(url,{
method:"POST",
headers:{"Content-Type":"application/json","Idempotency-Key":newIdempotencyKey()},
body:JSON.stringify(body||{})
});
}

async function runNotificationAction(button,id,action,label){
const buttons=button.parentElement?button.parentElement.querySelectorAll("button"):[button];
for(const item of buttons){item.disabled=true;}
const original=button.textContent;
button.textContent="Working";
try{
const response=await mutateNotificationFailure(adminPath+"/notifications/failures/"+encodeURIComponent(id)+"/"+action,{});
setBanner(notificationActionSummary(label,response.body||{}),"");
await loadNotifications();
}catch(error){
setBanner(label+" failed - "+operatorError(error),"err");
}finally{
if(!button.isConnected){return;}
button.textContent=original;
for(const item of buttons){item.disabled=false;}
}
}

async function retryVisibleNotifications(button){
const limit=Math.max(1,parseInt(button.dataset.limit||"50",10)||50);
button.disabled=true;
const original=button.textContent;
button.textContent="Working";
try{
const response=await mutateNotificationFailure(adminPath+"/notifications/failures/retry",{limit:limit});
setBanner(notificationActionSummary("Retry visible failures",response.body||{}),"");
await loadNotifications();
}catch(error){
setBanner("Retry visible failures failed - "+operatorError(error),"err");
}finally{
if(!button.isConnected){return;}
button.textContent=original;
button.disabled=false;
}
}

async function loadHealth(){
await loadProbe("/healthz","health-probe",[200]);
await loadProbe("/readyz","ready-probe",[200,503]);
}

async function loadProbe(url,id,expected){
const node=document.getElementById(id);
const pill=node.querySelector(".pill");
const pre=node.querySelector("pre");
try{
const response=await fetch(url,{headers:{Accept:"application/json"}});
const raw=await response.text();
let body={};
if(raw){try{body=JSON.parse(raw);}catch(_err){body={raw:raw};}}
const ok=expected.includes(response.status);
pill.className="pill "+(ok?(response.status===503?"warn":"ok"):"err");
pill.textContent="HTTP "+response.status;
writeJSON(pre,body);
if(url==="/readyz"&&response.status===503){
setBanner("Readiness reports unhealthy dependencies. Check database, redis, storage, or maintenance mode.","warn");
}
}catch(error){
pill.className="pill err";
pill.textContent="request failed";
writeJSON(pre,{error:error.message});
setBanner("A runtime probe failed. Check server logs and dependencies.","err");
}
}

function appendExtraConsoleTasks(_tasks){}
function bindExtraConsoleEvents(){}
%s

async function loadAll(){
setStatus("Loading");
setBanner("","");
const tasks=[
["build info",loadBuildInfo],
["overview",loadOverview],
["timeline lookup",loadSupportTimelineLookup],
["settings",loadSettings],
["audit logs",loadAuditLogs],
["security stats",loadSecurity],
["ops checks",loadOps],
["notification failures",loadNotifications],
["health",loadHealth],
];
appendExtraConsoleTasks(tasks);
const failures=[];
for(const task of tasks){
try{
await task[1]();
}catch(error){
failures.push(task[0]+": "+error.message);
if(error.status===401||error.status===403){
setBanner("Enter a valid admin key to load protected admin panels.","warn");
}
}
}
setStatus(failures.length?("Loaded with "+failures.length+" issue(s)"):"Loaded");
if(failures.length&&!(banner&&banner.className.includes("show"))){
setBanner(failures.join("; "),"warn");
}
}

document.getElementById("refresh-all").addEventListener("click",loadAll);
document.getElementById("refresh-overview").addEventListener("click",function(){loadOverview().catch(function(error){setBanner(error.message,"err");});});
document.getElementById("support-timeline-form").addEventListener("submit",function(event){event.preventDefault();loadSupportTimelineLookup().catch(function(error){writeJSON(document.getElementById("support-timeline-json"),{error:error.message,body:error.body||{}});setBanner(error.message,"err");});});
document.getElementById("refresh-support-timeline").addEventListener("click",function(){loadSupportTimelineLookup().catch(function(error){writeJSON(document.getElementById("support-timeline-json"),{error:error.message,body:error.body||{}});setBanner(error.message,"err");});});
document.getElementById("clear-support-timeline").addEventListener("click",function(){
for(const input of document.querySelectorAll("#support-timeline-form input")){input.value=input.name==="limit"?"10":"";}
renderSupportTimelineLookup(null);
});
document.getElementById("refresh-settings").addEventListener("click",function(){loadSettings().catch(function(error){setBanner(error.message,"err");});});
document.getElementById("refresh-security").addEventListener("click",function(){loadSecurity().catch(function(error){setBanner(error.message,"err");});});
document.getElementById("refresh-ops").addEventListener("click",function(){loadOps().catch(function(error){setBanner(error.message,"err");});});
document.getElementById("refresh-notifications").addEventListener("click",function(){loadNotifications().catch(function(error){setBanner(error.message,"err");});});
document.getElementById("retry-visible-notifications").addEventListener("click",function(event){retryVisibleNotifications(event.currentTarget).catch(function(error){setBanner(operatorError(error),"err");});});
document.getElementById("refresh-health").addEventListener("click",function(){loadHealth().catch(function(error){setBanner(error.message,"err");});});
document.getElementById("audit-filter").addEventListener("submit",function(event){event.preventDefault();loadAuditLogs().catch(function(error){setBanner(error.message,"err");});});
document.getElementById("clear-audit-filter").addEventListener("click",function(){
for(const input of document.querySelectorAll("#audit-filter input")){input.value="";}
loadAuditLogs().catch(function(error){setBanner(error.message,"err");});
});
adminKeyInput.addEventListener("keydown",function(event){
if(event.key==="Enter"){event.preventDefault();loadAll();}
});

bindExtraConsoleEvents();
loadHealth();
loadBuildInfo().catch(function(error){setBanner("Version metadata failed: "+error.message,"warn");});
setBanner("Enter an admin key and refresh to load protected admin panels.","warn");
})();`, jsStringLiteral(opts.APIPrefix), jsStringLiteral(opts.AdminPath), jsStringLiteral(opts.OverviewPath), opts.ExtraScript)
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
