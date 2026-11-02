#!/usr/bin/env node
// Production React/Vue/Go/GIS with parent-verified PostgreSQL checkpoints.
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { constants, closeSync, cpSync, existsSync, fstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { createInterface } from 'node:readline';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { connect, freePort, ready, stop } from './session-e2e.mjs';
import { renderGateway } from './gateway-browser-e2e.mjs';
import { requestViolatesBoundary } from './farm-browser-e2e.mjs';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
export const CHECKPOINTS = ['created', 'revised', 'raced', 'revoke-request', 'revoked-verified'];
export const REQUIRED_CHECKS = [
  'production_nginx_configuration_valid', 'real_react_login_shared_vue_session', 'http_only_platform_cookie',
  'explicit_parent_parcel_capabilities', 'create_preview_bound_to_parent', 'duplicate_click_single_create',
  'parcel_survives_reload', 'current_geometry_crs_area_hash', 'ui_revision_two_same_identity',
  'historical_revision_one_readonly', 'historical_at_selector_version_one', 'current_revision_restored',
  'concurrent_api_revision_three', 'stale_revision_409_preserves_draft', 'foreign_workspace_denied',
  'same_tenant_other_workspace_denied', 'same_cookie_after_revocation', 'revoked_old_page_clears_content',
  'revoked_history_navigation_no_content', 'all_database_checkpoints_acknowledged',
  'no_internal_credentials_identity_spoofing_or_external_requests',
];
export const STAGES = ['preflight', 'login_navigation', 'login_account', 'login_password', 'login_submit', 'workspace',
  'parent_open', 'parcel_open', 'create_form', 'create_preview', 'create_confirm', 'create_reload', 'revision_form',
  'revision_confirm', 'history', 'at_time', 'race', 'scope_negatives', 'revocation', 'complete'];
export const UI_FLAGS = ['login','session_ready','context','create','detail','editor','preview','busy','conflict'];
export const ERROR_CODES = ['REVISION_CONFLICT','STATE_CONFLICT','INVALID_GEOMETRY','INVALID_ARGUMENT','INVALID_RESPONSE','DEPENDENCY_UNAVAILABLE','AUTHORITY_UNAVAILABLE','NETWORK_ERROR','TIMEOUT','RESOURCE_NOT_FOUND','FORBIDDEN'];
export function safeUIFlags(value) { if(!value||typeof value!=='object'||Array.isArray(value))return {};return Object.fromEntries(UI_FLAGS.filter(k=>typeof value[k]==='boolean').map(k=>[k,value[k]])); }
const id = value => typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value);
const port = value => Number.isInteger(value) && value >= 1024 && value <= 65535;
export function validateConfig(v, path) {
  const keys = ['platform_url','gateway_port','username','password','workspace_id','foreign_workspace_id','second_workspace_id','farm_id','report_path'];
  if (!v || typeof v !== 'object' || Array.isArray(v) || Object.keys(v).sort().join() !== keys.sort().join()) throw Error('invalid private parcel configuration');
  if (!/^http:\/\/127\.0\.0\.1:[0-9]+$/.test(v.platform_url) || !port(Number(new URL(v.platform_url).port)) || !port(v.gateway_port) || Number(new URL(v.platform_url).port) === v.gateway_port) throw Error('literal loopback ports required');
  for (const key of ['username','workspace_id','foreign_workspace_id','second_workspace_id']) if (!id(v[key])) throw Error('invalid fixture identifier');
  if (new Set([v.workspace_id,v.foreign_workspace_id,v.second_workspace_id]).size !== 3 || !/^farm_[a-f0-9]{32}$/.test(v.farm_id)) throw Error('distinct scopes and real farm required');
  if (typeof v.password !== 'string' || v.password.length < 12 || v.password.length > 256) throw Error('invalid fixture account');
  if (typeof v.report_path !== 'string' || resolve(v.report_path) !== v.report_path || dirname(v.report_path) !== dirname(resolve(path)) || v.report_path === resolve(path) || !/^[A-Za-z0-9_-]+\.json$/.test(v.report_path.split('/').at(-1))) throw Error('owned report path required');
  return v;
}
export function readPrivateConfig(path) {
  const directory = statSync(dirname(resolve(path)));
  if (!directory.isDirectory() || (directory.mode & 0o777) !== 0o700 || directory.uid !== process.getuid?.()) throw Error('owned private directory required');
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const info = fstatSync(fd);
    if (!info.isFile() || (info.mode & 0o777) !== 0o600 || info.nlink !== 1 || info.uid !== process.getuid?.() || info.size > 16384) throw Error('private configuration mode invalid');
    return validateConfig(JSON.parse(readFileSync(fd, 'utf8')), path);
  } finally { closeSync(fd); }
}
export function validAck(line, checkpoint) {
  if (!CHECKPOINTS.includes(checkpoint) || typeof line !== 'string' || line.length > 128 || (line.match(/"event"\s*:/g) || []).length !== 1 || (line.match(/"checkpoint"\s*:/g) || []).length !== 1) return false;
  try { const v = JSON.parse(line); return !!v && Object.keys(v).sort().join() === 'checkpoint,event' && v.event === 'ack' && v.checkpoint === checkpoint; } catch { return false; }
}
export function checkpointControl(input, output, deadline = 60000) {
  const lines = createInterface({ input, crlfDelay: Infinity }); let pending, index = 0, broken = false;
  lines.on('line', line => { if (!pending || !validAck(line, CHECKPOINTS[index])) { broken = true; pending?.reject(Error('invalid checkpoint')); return; } const item = pending; pending = null; index++; item.resolve(); });
  lines.on('close', () => { if (index !== CHECKPOINTS.length) { broken = true; pending?.reject(Error('checkpoint channel closed')); } });
  return { wait(checkpoint) { if (broken || pending || CHECKPOINTS[index] !== checkpoint) return Promise.reject(Error('checkpoint order invalid')); return new Promise((resolve,reject) => { const timer = setTimeout(() => { broken=true;pending=null;reject(Error('checkpoint timeout')); }, deadline); pending={resolve:()=>{clearTimeout(timer);resolve();},reject:e=>{clearTimeout(timer);reject(e);}};output.write(JSON.stringify({event:'checkpoint',checkpoint})+'\n'); }); }, complete:()=>!broken&&index===CHECKPOINTS.length, close:()=>lines.close() };
}
export function safeFailure(error) {
  const name = String(error?.message || '').replace(/^assertion failed: /, '');
  return REQUIRED_CHECKS.includes(name) ? `assertion failed: ${name}` : 'parcel browser fixture failed; private diagnostics withheld';
}
async function observer(debugPort, origin) {
  const targets=await (await fetch(`http://127.0.0.1:${debugPort}/json/list`)).json();
  const socket=new WebSocket(targets.find(v=>v.type==='page').webSocketDebuggerUrl);
  await new Promise((ok,no)=>{socket.addEventListener('open',ok,{once:true});socket.addEventListener('error',no,{once:true});});
  const counts={observed:0,violations:0,creates:0,revisions:0};
  socket.addEventListener('message',event=>{const v=JSON.parse(event.data);if(v.method!=='Network.requestWillBeSent')return;const r=v.params.request;counts.observed++;if(requestViolatesBoundary(r,origin))counts.violations++;let url;try{url=new URL(r.url);}catch{return;}if(r.method==='POST'&&/^\/api\/agri\/v1\/farms\/farm_[a-f0-9]{32}\/parcels$/.test(url.pathname))counts.creates++;if(r.method==='POST'&&/^\/api\/agri\/v1\/parcels\/parcel_[a-f0-9]{32}\/revisions$/.test(url.pathname))counts.revisions++;});
  await new Promise((ok,no)=>{const timer=setTimeout(()=>no(Error('network observer unavailable')),10000);const reply=e=>{const v=JSON.parse(e.data);if(v.id===1){clearTimeout(timer);socket.removeEventListener('message',reply);v.error?no(Error('network observer rejected')):ok();}};socket.addEventListener('message',reply);socket.send(JSON.stringify({id:1,method:'Network.enable'}));});
  return {counts:()=>({...counts}),close:()=>socket.close()};
}
async function click(cdp, selector, twice=false) {
  await cdp.poll(`!!document.querySelector(${JSON.stringify(selector)}) && !document.querySelector(${JSON.stringify(selector)}).matches(':disabled')`, 'enabled parcel action');
  await cdp.evaluate(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.click();${twice?'e.click();':''}})()`);
}
async function input(cdp, name, value) {
  const selector=`[name=${name}]`;await cdp.poll(`!!document.querySelector(${JSON.stringify(selector)}) && !document.querySelector(${JSON.stringify(selector)}).matches(':disabled')`, 'enabled parcel field');
  await cdp.evaluate(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.value=${JSON.stringify(value)};e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));})()`);
}
export function rectangle(offset=0) { const x=110.001+offset,y=20.001;return {type:'Polygon',coordinates:[[[x,y],[x+0.002,y],[x+0.002,y+0.002],[x,y+0.002],[x,y]]]}; }
async function api(cdp, expression) {
  return cdp.evaluate(`(async()=>{const request=async(path,method='GET',body)=>{const headers={Accept:'application/json'};if(method!=='GET'){const c=await fetch('/api/auth/csrf',{credentials:'same-origin',cache:'no-store'});const e=await c.json();if(!c.ok||e.code!==0)throw Error('csrf unavailable');headers['X-CSRF-Token']=e.data.token;headers['Content-Type']='application/json';headers['Idempotency-Key']=crypto.randomUUID();}const r=await fetch(path,{method,credentials:'same-origin',cache:'no-store',headers,...(body?{body:JSON.stringify(body)}:{})});const e=await r.json();return {status:r.status,data:e.data};};${expression}})()`,'private parcel fixture');
}
async function completed(cdp) {
  for(let i=0;i<80;i++){const state=await cdp.evaluate(`document.querySelector('[data-testid=parcel-operation]')?.dataset.operationState`);if(state==='completed')return;if(['rejected','reconciliation_required'].includes(state))throw Error('parcel mutation rejected');if(await cdp.evaluate(`!!document.querySelector('[data-testid=parcel-error]')`))throw Error('parcel API failed');if(await cdp.evaluate(`!!document.querySelector('[data-testid=parcel-operation-poll]') && !document.querySelector('[data-testid=parcel-operation-poll]').matches(':disabled')`))await click(cdp,'[data-testid=parcel-operation-poll]');await new Promise(r=>setTimeout(r,100));}throw Error('parcel operation deadline');
}
export async function main(configPath=process.argv[2]) {
  const report={schema_version:'1.0',result:'not_run',stage:'preflight',results:[],data_class:'fixture',production_acceptance:false};
  let config,temp,nginx,browser,cdp,network,control,cleanupPromise;
  const cleanup=()=>cleanupPromise||=(async()=>{control?.close();network?.close();cdp?.close();await Promise.all([stop(browser),stop(nginx)]);if(temp)rmSync(temp,{recursive:true,force:true});})();
  const interrupted=()=>{report.result='fail';report.failure='owned parcel fixture interrupted';void cleanup().finally(()=>process.exit(1));};
  process.once('SIGINT',interrupted);process.once('SIGTERM',interrupted);
  const check=(value,name)=>{if(!REQUIRED_CHECKS.includes(name)||!value||report.results.some(v=>v.name===name))throw Error(`assertion failed: ${name}`);report.results.push({name,result:'pass'});};
  try {
    if(process.getuid?.()===0||!configPath)throw Error('private non-root fixture required');config=readPrivateConfig(configPath);if(existsSync(config.report_path))throw Error('report exists');control=checkpointControl(process.stdin,process.stdout);
    const chrome=process.env.CHROME_BIN||'/usr/bin/chromium',nginxBin=process.env.NGINX_BIN||'/usr/sbin/nginx',mimeTypes=resolve(process.env.NGINX_MIME_TYPES||'/etc/nginx/mime.types');
    const react=resolve(process.env.AGRI_E2E_REACT_DIST||join(ROOT,'web/dist-agriculture')),vue=resolve(process.env.AGRI_E2E_VUE_DIST||join(ROOT,'services/agri-gis/frontend/dist-agriculture')),template=join(ROOT,'deploy/agriculture/nginx.conf.template');
    for(const p of [chrome,nginxBin,mimeTypes,template,join(react,'index.html'),join(vue,'index.html')])if(!existsSync(p))throw Error('actual runtime missing');
    const hash=p=>createHash('sha256').update(readFileSync(p)).digest('hex');report.artifact_hashes={nginx_template:hash(template),react_index:hash(join(react,'index.html')),vue_index:hash(join(vue,'index.html')),runner:hash(fileURLToPath(import.meta.url))};report.source_commit=process.env.AGRI_E2E_SOURCE_COMMIT||spawnSync('git',['rev-parse','HEAD'],{cwd:ROOT,encoding:'utf8'}).stdout.trim();if(!/^[a-f0-9]{40}$/.test(report.source_commit))throw Error('invalid source commit');
    const provenance=join(ROOT,'.local/evidence/session-ci-provenance.json');if(existsSync(provenance)){if(JSON.parse(readFileSync(provenance,'utf8')).source_commit!==report.source_commit)throw Error('provenance mismatch');report.ci_provenance_sha256=hash(provenance);}
    temp=mkdtempSync(join(tmpdir(),'agri-parcel-browser-'));cpSync(react,join(temp,'html'),{recursive:true});cpSync(vue,join(temp,'html/agri'),{recursive:true});cpSync(join(ROOT,'deploy/agriculture/errors'),join(temp,'html/_agri_errors'),{recursive:true});for(const n of ['client','proxy','fastcgi','uwsgi','scgi'])mkdirSync(join(temp,n));
    const origin=`http://127.0.0.1:${config.gateway_port}`,nginxConfig=join(temp,'nginx.conf');writeFileSync(nginxConfig,renderGateway(readFileSync(template,'utf8'),{directory:temp,port:config.gateway_port,apiPort:Number(new URL(config.platform_url).port),mimeTypes}),{mode:0o600});check(spawnSync(nginxBin,['-t','-p',temp+'/','-c',nginxConfig],{stdio:'ignore'}).status===0,'production_nginx_configuration_valid');
    nginx=spawn(nginxBin,['-p',temp+'/','-c',nginxConfig,'-g','daemon off;'],{stdio:'ignore',env:{PATH:process.env.PATH}});await ready(origin+'/api/auth/settings',nginx,30000);
    const chromePort=await freePort();browser=spawn(chrome,['--headless=new','--disable-background-networking','--no-first-run',`--remote-debugging-port=${chromePort}`,`--user-data-dir=${join(temp,'chrome')}`,'about:blank'],{stdio:'ignore',env:{PATH:process.env.PATH,HOME:temp,XDG_CONFIG_HOME:join(temp,'config'),XDG_CACHE_HOME:join(temp,'cache')}});await ready(`http://127.0.0.1:${chromePort}/json/version`,browser,30000);cdp=await connect(chromePort,origin);network=await observer(chromePort,origin);
    const workspace='/agri/workspaces/'+config.workspace_id;
    const workspaceReady=()=>cdp.poll(`!!document.querySelector('main[data-session-status=ready]') && !!document.querySelector('[data-testid=farm-list]') && !document.querySelector('[data-testid=farm-busy]')`,'farm workspace ready');
    const parentOpen=async()=>{report.stage='parent_open';await cdp.poll(`[...document.querySelectorAll('[data-testid=farm-list] li')].some(e=>e.textContent.includes('parcel-browser-parent'))`,'actual parent farm');await cdp.evaluate(`[...document.querySelectorAll('[data-testid=farm-list] li')].find(e=>e.textContent.includes('parcel-browser-parent')).querySelector('button').click()`);report.stage='parcel_open';await click(cdp,'[data-testid=parcel-workspace-toggle]');await cdp.poll(`!!document.querySelector('[data-testid=parcel-context]')&&!document.querySelector('[data-testid=parcel-busy]')`,'parcel context ready');};
    const reload=async()=>{await cdp.navigate(workspace);await workspaceReady();await parentOpen();};
    const open=async()=>{await cdp.poll(`document.querySelectorAll('[data-testid=parcel-list] li').length===1`,'persisted parcel');await click(cdp,'[data-testid=parcel-list] li button');await cdp.poll(`!!document.querySelector('[data-testid=parcel-detail]')&&!document.querySelector('[data-testid=parcel-busy]')`,'parcel detail ready');};
    report.stage='login_navigation';await cdp.navigate('/login?next='+encodeURIComponent(workspace));report.stage='login_account';await cdp.fill('#login-account',config.username,'fixture account');report.stage='login_password';await cdp.fill('#login-password',config.password,'fixture password');report.stage='login_submit';await click(cdp,'form button[type=submit]');report.stage='workspace';await workspaceReady();check(await cdp.evaluate(`document.querySelector('[data-testid=agri-workspace-context]')?.dataset.workspaceId===${JSON.stringify(config.workspace_id)}`),'real_react_login_shared_vue_session');
    const cookie=(await cdp.send('Network.getCookies',{urls:[origin]})).cookies.find(v=>v.name==='open_ai_canvas_session');check(!!cookie?.value&&cookie.httpOnly,'http_only_platform_cookie');await parentOpen();check(await cdp.evaluate(`!!document.querySelector('[data-testid=parcel-create]')&&!!document.querySelector('[data-testid=parcel-refresh]')&&document.querySelector('[data-testid=parcel-workspace]')?.dataset.farmId===${JSON.stringify(config.farm_id)}`),'explicit_parent_parcel_capabilities');
    report.stage='create_form';await click(cdp,'[data-testid=parcel-create]');for(const [name,value] of Object.entries({parcel_external_code:'browser-parcel',parcel_name:'Synthetic browser parcel',parcel_source_crs:'EPSG:4326',parcel_coordinate_order:'longitude_latitude',parcel_geometry:JSON.stringify(rectangle()),parcel_reason:'Synthetic initial boundary'}))await input(cdp,name,value);
    report.stage='create_preview';await click(cdp,'[data-testid=parcel-preview]');await cdp.poll(`!!document.querySelector('[data-testid=parcel-confirm]')`,'real parcel preview');check(await cdp.evaluate(`document.querySelector('[data-testid=parcel-operation-preview]').textContent.includes(${JSON.stringify(config.farm_id)})&&document.querySelector('[data-testid=parcel-preview-summary]').textContent.includes('geometry')`),'create_preview_bound_to_parent');report.stage='create_confirm';await click(cdp,'[data-testid=parcel-confirm]',true);await completed(cdp);check(network.counts().creates===1,'duplicate_click_single_create');await control.wait('created');
    report.stage='create_reload';await reload();await open();check(await cdp.evaluate(`document.querySelector('[data-testid=parcel-detail]').dataset.revision==='1'`),'parcel_survives_reload');
    const parcelID=await cdp.evaluate(`document.querySelector('[data-testid=parcel-list] li').dataset.parcelId`);if(!/^parcel_[a-f0-9]{32}$/.test(parcelID))throw Error('invalid persisted parcel id');
    const current=await api(cdp,`return request('/api/agri/v1/parcels/'+${JSON.stringify(parcelID)}+'?workspace_id='+${JSON.stringify(config.workspace_id)});`);check(current.status===200&&current.data.geometry_revision===1&&current.data.source_crs==='EPSG:4326'&&current.data.coordinate_order==='longitude_latitude'&&Number(current.data.area_m2)>0&&/^[a-f0-9]{64}$/.test(current.data.geometry_hash),'current_geometry_crs_area_hash');const firstTime=current.data.valid_from;
    report.stage='revision_form';await click(cdp,'[data-testid=parcel-edit]');await input(cdp,'parcel_geometry',JSON.stringify(rectangle(0.001)));await input(cdp,'parcel_reason','Synthetic second boundary');await click(cdp,'[data-testid=parcel-preview]');await cdp.poll(`!!document.querySelector('[data-testid=parcel-confirm]')`,'revision preview');report.stage='revision_confirm';await click(cdp,'[data-testid=parcel-confirm]');await completed(cdp);await control.wait('revised');await reload();await open();check(await cdp.evaluate(`document.querySelector('[data-testid=parcel-list] li').dataset.parcelId===${JSON.stringify(parcelID)}&&document.querySelector('[data-testid=parcel-detail]').dataset.revision==='2'`),'ui_revision_two_same_identity');
    report.stage='history';await click(cdp,'[data-testid=parcel-history-load]');await cdp.poll(`document.querySelectorAll('[data-testid=parcel-history] li').length===2`,'immutable revisions');await cdp.evaluate(`[...document.querySelectorAll('[data-testid=parcel-history] li button')].find(e=>e.textContent.trim()==='边界版本 1').click()`);await cdp.poll(`!!document.querySelector('[data-testid=parcel-historical]')`,'historical readonly');check(await cdp.evaluate(`document.querySelector('[data-testid=parcel-detail]').dataset.revision==='1'&&!document.querySelector('[data-testid=parcel-edit]')`),'historical_revision_one_readonly');
    report.stage='at_time';await click(cdp,'[data-testid=parcel-current]');await cdp.poll(`document.querySelector('[data-testid=parcel-detail]').dataset.revision==='2'`,'current before at');await input(cdp,'parcel_at',firstTime);await click(cdp,'[data-testid=parcel-at-submit]');await cdp.poll(`document.querySelector('[data-testid=parcel-detail]').dataset.revision==='1'`,'at historical version');check(await cdp.evaluate(`!!document.querySelector('[data-testid=parcel-historical]')&&!document.querySelector('[data-testid=parcel-edit]')`),'historical_at_selector_version_one');await click(cdp,'[data-testid=parcel-current]');await cdp.poll(`document.querySelector('[data-testid=parcel-detail]').dataset.revision==='2'`,'current version');check(true,'current_revision_restored');
    report.stage='race';await click(cdp,'[data-testid=parcel-edit]');const draft=JSON.stringify(rectangle(0.002));await input(cdp,'parcel_geometry',draft);await input(cdp,'parcel_reason','Synthetic stale boundary');
    const raced=await api(cdp,`const base={workspace_id:${JSON.stringify(config.workspace_id)},expected_revision:2,geometry:${JSON.stringify(rectangle(0.003))},source_crs:'EPSG:4326',coordinate_order:'longitude_latitude',reason:'Synthetic competing boundary'};const p=await request('/api/agri/v1/parcel-previews','POST',{...base,action:'revise',parcel_id:${JSON.stringify(parcelID)}});if(p.status!==200)return false;let r=await request('/api/agri/v1/parcels/'+${JSON.stringify(parcelID)}+'/revisions','POST',{...base,preview_id:p.data.preview_id});for(let i=0;i<50&&r.status===202&&r.data?.status_url;i++){await new Promise(ok=>setTimeout(ok,100));r=await request(r.data.status_url);if(r.data?.state==='pending')r.status=202;}if(r.status!==200||r.data.state!=='completed')return false;const d=await request('/api/agri/v1/parcels/'+${JSON.stringify(parcelID)}+'?workspace_id='+${JSON.stringify(config.workspace_id)});return d.status===200&&d.data.geometry_revision===3;`);check(raced===true,'concurrent_api_revision_three');await click(cdp,'[data-testid=parcel-preview]');await cdp.poll(`!!document.querySelector('[data-testid=parcel-conflict]')`,'actual version conflict');check(await cdp.evaluate(`document.querySelector('[data-testid=parcel-error]')?.dataset.errorCode==='REVISION_CONFLICT'&&document.querySelector('[name=parcel_geometry]').value===${JSON.stringify(draft)}`),'stale_revision_409_preserves_draft');await control.wait('raced');
    report.stage='scope_negatives';for(const [scope,name] of [[config.foreign_workspace_id,'foreign_workspace_denied'],[config.second_workspace_id,'same_tenant_other_workspace_denied']]){const denied=await api(cdp,`const r=await request('/api/agri/v1/parcels/'+${JSON.stringify(parcelID)}+'?workspace_id='+${JSON.stringify(scope)}+'&revision=1');return [403,404].includes(r.status)&&r.data==null;`);check(denied===true,name);}
    report.stage='revocation';await control.wait('revoke-request');const after=(await cdp.send('Network.getCookies',{urls:[origin]})).cookies.find(v=>v.name==='open_ai_canvas_session');check(after?.value===cookie.value,'same_cookie_after_revocation');const deniedAfter=await api(cdp,`return request('/api/agri/v1/parcels/'+${JSON.stringify(parcelID)}+'?workspace_id='+${JSON.stringify(config.workspace_id)});`);if(![401,403,404].includes(deniedAfter.status)||deniedAfter.data!=null)throw Error('revoked request not denied');await cdp.evaluate(`window.dispatchEvent(new Event('pageshow'))`);await cdp.poll(`!document.querySelector('[data-testid=parcel-detail]')&&!document.querySelector('[data-testid=parcel-editor]')&&!document.querySelector('[data-testid=parcel-preview-summary]')`,'revoked content cleared');check(true,'revoked_old_page_clears_content');await cdp.navigate('/agri/');await cdp.evaluate('history.back()');await cdp.poll(`location.pathname===${JSON.stringify(workspace)} && !!document.querySelector('main[data-session-status=error]') && !!document.querySelector('[role=alert]') && !document.querySelector('[data-testid=parcel-detail]')&&!document.querySelector('[data-testid=parcel-editor]')`,'revoked history cleared');check(true,'revoked_history_navigation_no_content');await control.wait('revoked-verified');check(control.complete(),'all_database_checkpoints_acknowledged');report.network=network.counts();check(report.network.observed>0&&report.network.violations===0,'no_internal_credentials_identity_spoofing_or_external_requests');report.stage='complete';report.result='pass';
  } catch(error) {report.result='fail';report.failure=safeFailure(error);process.exitCode=1;if(cdp){try{const flags=await cdp.evaluate(`(()=>{const has=s=>!!document.querySelector(s);return {login:has('#login-account'),session_ready:has('main[data-session-status=ready]'),context:has('[data-testid=parcel-context]'),create:has('[data-testid=parcel-create]'),detail:has('[data-testid=parcel-detail]'),editor:has('[data-testid=parcel-editor]'),preview:has('[data-testid=parcel-confirm]'),busy:has('[data-testid=parcel-busy]'),conflict:has('[data-testid=parcel-conflict]'),error_code:document.querySelector('[data-testid=parcel-error]')?.dataset.errorCode}})()`);report.ui_flags=safeUIFlags(flags);report.error_code=ERROR_CODES.includes(flags?.error_code)?flags.error_code:null;}catch{/* No raw DOM, response or exception text. */}}}
  finally {await cleanup();process.removeListener('SIGINT',interrupted);process.removeListener('SIGTERM',interrupted);if(config&&!existsSync(config.report_path))writeFileSync(config.report_path,JSON.stringify(report,null,2)+'\n',{flag:'wx',mode:0o600});}
  return report;
}
if(process.argv[1]&&import.meta.url===pathToFileURL(resolve(process.argv[1])).href)await main();
