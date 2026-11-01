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
export const CHECKPOINTS = ['created', 'versioned', 'regranted', 'revoke-request', 'revoked-verified'];
export const REQUIRED_CHECKS = ['production_nginx_configuration_valid', 'real_react_login_shared_vue_session', 'http_only_platform_cookie', 'explicit_asset_registration_capability', 'explicit_initial_grant_plan', 'pending_rights_metadata_only_preview', 'duplicate_click_single_create', 'asset_survives_reload', 'metadata_not_ready_without_entity', 'unknown_source_rights_pending', 'version_two_committed', 'old_grant_denies_current_and_history', 'explicit_regrant_restores_version_two', 'historical_version_one_readonly', 'history_uses_current_authority', 'not_ready_download_denied', 'foreign_workspace_denied', 'same_tenant_other_workspace_denied', 'same_cookie_after_revocation', 'revoked_old_page_clears_content', 'revoked_history_navigation_no_content', 'all_database_checkpoints_acknowledged', 'no_internal_credentials_identity_spoofing_or_external_requests'];
export const STAGES = ['preflight', 'login_navigation', 'login_account', 'login_password', 'login_submit', 'workspace', 'create_form', 'initial_grants', 'create_preview', 'create_confirm', 'create_reload', 'version_form', 'version_confirm', 'old_grant', 'regrant', 'history', 'download', 'scope_negatives', 'revocation', 'complete'];
export const UI_FLAGS = ['login','session_ready','context','create','detail','editor','preview','busy','conflict'];
export const ERROR_CODES = ['REVISION_CONFLICT','STATE_CONFLICT','INVALID_GEOMETRY','INVALID_ARGUMENT','INVALID_RESPONSE','DEPENDENCY_UNAVAILABLE','AUTHORITY_UNAVAILABLE','NETWORK_ERROR','TIMEOUT','RESOURCE_NOT_FOUND','FORBIDDEN'];
export function safeUIFlags(value) { if(!value||typeof value!=='object'||Array.isArray(value))return {};return Object.fromEntries(UI_FLAGS.filter(k=>typeof value[k]==='boolean').map(k=>[k,value[k]])); }
const id = value => typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value);
const port = value => Number.isInteger(value) && value >= 1024 && value <= 65535;
export function validateConfig(v, path) {
  const keys = ['platform_url','gateway_port','username','password','workspace_id','foreign_workspace_id','second_workspace_id','report_path'];
  if (!v || typeof v !== 'object' || Array.isArray(v) || Object.keys(v).sort().join() !== keys.sort().join()) throw Error('invalid private asset configuration');
  if (!/^http:\/\/127\.0\.0\.1:[0-9]+$/.test(v.platform_url) || !port(Number(new URL(v.platform_url).port)) || !port(v.gateway_port) || Number(new URL(v.platform_url).port) === v.gateway_port) throw Error('literal loopback ports required');
  for (const key of ['username','workspace_id','foreign_workspace_id','second_workspace_id']) if (!id(v[key])) throw Error('invalid fixture identifier');
  if (new Set([v.workspace_id,v.foreign_workspace_id,v.second_workspace_id]).size !== 3) throw Error('distinct scopes required');
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
  return REQUIRED_CHECKS.includes(name) ? `assertion failed: ${name}` : 'asset browser fixture failed; private diagnostics withheld';
}
async function observer(debugPort, origin) {
  const targets=await (await fetch(`http://127.0.0.1:${debugPort}/json/list`)).json();
  const socket=new WebSocket(targets.find(v=>v.type==='page').webSocketDebuggerUrl);
  await new Promise((ok,no)=>{socket.addEventListener('open',ok,{once:true});socket.addEventListener('error',no,{once:true});});
  const counts={observed:0,violations:0,creates:0,revisions:0};
  socket.addEventListener('message',event=>{const v=JSON.parse(event.data);if(v.method!=='Network.requestWillBeSent')return;const r=v.params.request;counts.observed++;if(requestViolatesBoundary(r,origin))counts.violations++;let url;try{url=new URL(r.url);}catch{return;}if(r.method==='POST'&&/^\/api\/agri\/v1\/assets$/.test(url.pathname))counts.creates++;if(r.method==='POST'&&/^\/api\/agri\/v1\/assets\/asset_[a-f0-9]{32}\/versions$/.test(url.pathname))counts.revisions++;});
  await new Promise((ok,no)=>{const timer=setTimeout(()=>no(Error('network observer unavailable')),10000);const reply=e=>{const v=JSON.parse(e.data);if(v.id===1){clearTimeout(timer);socket.removeEventListener('message',reply);v.error?no(Error('network observer rejected')):ok();}};socket.addEventListener('message',reply);socket.send(JSON.stringify({id:1,method:'Network.enable'}));});
  return {counts:()=>({...counts}),close:()=>socket.close()};
}
async function click(cdp, selector, twice=false) {
  await cdp.poll(`!!document.querySelector(${JSON.stringify(selector)}) && !document.querySelector(${JSON.stringify(selector)}).matches(':disabled')`, 'enabled asset action');
  await cdp.evaluate(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.click();${twice?'e.click();':''}})()`);
}
async function input(cdp, name, value) {
  const selector=`[name=${name}]`;await cdp.poll(`!!document.querySelector(${JSON.stringify(selector)}) && !document.querySelector(${JSON.stringify(selector)}).matches(':disabled')`, 'enabled asset field');
  await cdp.evaluate(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.value=${JSON.stringify(value)};e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));})()`);
}
async function api(cdp, expression) {
  return cdp.evaluate(`(async()=>{const request=async(path,method='GET',body)=>{const headers={Accept:'application/json'};if(method!=='GET'){const c=await fetch('/api/auth/csrf',{credentials:'same-origin',cache:'no-store'});const e=await c.json();if(!c.ok||e.code!==0)throw Error('csrf unavailable');headers['X-CSRF-Token']=e.data.token;headers['Content-Type']='application/json';headers['Idempotency-Key']=crypto.randomUUID();}const r=await fetch(path,{method,credentials:'same-origin',cache:'no-store',headers,...(body?{body:JSON.stringify(body)}:{})});const e=await r.json();return {status:r.status,data:e.data};};${expression}})()`,'private asset fixture');
}
async function completed(cdp) {
  for(let i=0;i<80;i++){const state=await cdp.evaluate(`document.querySelector('[data-testid=asset-operation]')?.dataset.operationState`);if(state==='completed')return;if(['rejected','reconciliation_required'].includes(state))throw Error('asset mutation rejected');if(await cdp.evaluate(`!!document.querySelector('[data-testid=asset-error]')`))throw Error('asset API failed');if(await cdp.evaluate(`!!document.querySelector('[data-testid=asset-operation-poll]') && !document.querySelector('[data-testid=asset-operation-poll]').matches(':disabled')`))await click(cdp,'[data-testid=asset-operation-poll]');await new Promise(r=>setTimeout(r,100));}throw Error('asset operation deadline');
}
export async function main(configPath=process.argv[2]) {
  const report={schema_version:'1.0',result:'not_run',stage:'preflight',results:[],data_class:'fixture',production_acceptance:false};
  let config,temp,nginx,browser,cdp,network,control,cleanupPromise;
  const cleanup=()=>cleanupPromise||=(async()=>{control?.close();network?.close();cdp?.close();await Promise.all([stop(browser),stop(nginx)]);if(temp)rmSync(temp,{recursive:true,force:true});})();
  const interrupted=()=>{report.result='fail';report.failure='owned asset fixture interrupted';void cleanup().finally(()=>process.exit(1));};
  process.once('SIGINT',interrupted);process.once('SIGTERM',interrupted);
  const check=(value,name)=>{if(!REQUIRED_CHECKS.includes(name)||!value||report.results.some(v=>v.name===name))throw Error(`assertion failed: ${name}`);report.results.push({name,result:'pass'});};
  try {
    if(process.getuid?.()===0||!configPath)throw Error('private non-root fixture required');config=readPrivateConfig(configPath);if(existsSync(config.report_path))throw Error('report exists');control=checkpointControl(process.stdin,process.stdout);
    const chrome=process.env.CHROME_BIN||'/usr/bin/chromium',nginxBin=process.env.NGINX_BIN||'/usr/sbin/nginx',mimeTypes=resolve(process.env.NGINX_MIME_TYPES||'/etc/nginx/mime.types');
    const react=resolve(process.env.AGRI_E2E_REACT_DIST||join(ROOT,'web/dist-agriculture')),vue=resolve(process.env.AGRI_E2E_VUE_DIST||join(ROOT,'services/agri-gis/frontend/dist-agriculture')),template=join(ROOT,'deploy/agriculture/nginx.conf.template');
    for(const p of [chrome,nginxBin,mimeTypes,template,join(react,'index.html'),join(vue,'index.html')])if(!existsSync(p))throw Error('actual runtime missing');
    const hash=p=>createHash('sha256').update(readFileSync(p)).digest('hex');report.artifact_hashes={nginx_template:hash(template),react_index:hash(join(react,'index.html')),vue_index:hash(join(vue,'index.html')),runner:hash(fileURLToPath(import.meta.url))};report.source_commit=process.env.AGRI_E2E_SOURCE_COMMIT||spawnSync('git',['rev-parse','HEAD'],{cwd:ROOT,encoding:'utf8'}).stdout.trim();if(!/^[a-f0-9]{40}$/.test(report.source_commit))throw Error('invalid source commit');
    const provenance=join(ROOT,'.local/evidence/session-ci-provenance.json');if(existsSync(provenance)){if(JSON.parse(readFileSync(provenance,'utf8')).source_commit!==report.source_commit)throw Error('provenance mismatch');report.ci_provenance_sha256=hash(provenance);}
    temp=mkdtempSync(join(tmpdir(),'agri-asset-browser-'));cpSync(react,join(temp,'html'),{recursive:true});cpSync(vue,join(temp,'html/agri'),{recursive:true});cpSync(join(ROOT,'deploy/agriculture/errors'),join(temp,'html/_agri_errors'),{recursive:true});for(const n of ['client','proxy','fastcgi','uwsgi','scgi'])mkdirSync(join(temp,n));
    const origin=`http://127.0.0.1:${config.gateway_port}`,nginxConfig=join(temp,'nginx.conf');writeFileSync(nginxConfig,renderGateway(readFileSync(template,'utf8'),{directory:temp,port:config.gateway_port,apiPort:Number(new URL(config.platform_url).port),mimeTypes}),{mode:0o600});check(spawnSync(nginxBin,['-t','-p',temp+'/','-c',nginxConfig],{stdio:'ignore'}).status===0,'production_nginx_configuration_valid');
    nginx=spawn(nginxBin,['-p',temp+'/','-c',nginxConfig,'-g','daemon off;'],{stdio:'ignore',env:{PATH:process.env.PATH}});await ready(origin+'/api/auth/settings',nginx,30000);
    const chromePort=await freePort();browser=spawn(chrome,['--headless=new','--disable-background-networking','--no-first-run',`--remote-debugging-port=${chromePort}`,`--user-data-dir=${join(temp,'chrome')}`,'about:blank'],{stdio:'ignore',env:{PATH:process.env.PATH,HOME:temp,XDG_CONFIG_HOME:join(temp,'config'),XDG_CACHE_HOME:join(temp,'cache')}});await ready(`http://127.0.0.1:${chromePort}/json/version`,browser,30000);cdp=await connect(chromePort,origin);network=await observer(chromePort,origin);
    const workspace='/agri/workspaces/'+config.workspace_id;
    const workspaceReady=()=>cdp.poll(`!!document.querySelector('main[data-session-status=ready]') && !!document.querySelector('[data-testid=asset-catalog]') && !!document.querySelector('[data-testid=asset-create]') && !document.querySelector('[data-testid=asset-busy]')`,'asset workspace ready');
    const reload=async()=>{await cdp.navigate(workspace);await workspaceReady();};
    const open=async()=>{await cdp.poll(`document.querySelectorAll('[data-testid=asset-list] li').length===1`,'persisted asset');await click(cdp,'[data-testid=asset-list] li button');await cdp.poll(`!!document.querySelector('[data-testid=asset-detail]')&&!document.querySelector('[data-testid=asset-busy]')`,'asset detail ready');};
    report.stage='login_navigation';await cdp.navigate('/login?next='+encodeURIComponent(workspace));report.stage='login_account';await cdp.fill('#login-account',config.username,'fixture account');report.stage='login_password';await cdp.fill('#login-password',config.password,'fixture password');report.stage='login_submit';await click(cdp,'form button[type=submit]');report.stage='workspace';await workspaceReady();check(await cdp.evaluate(`document.querySelector('[data-testid=agri-workspace-context]')?.dataset.workspaceId===${JSON.stringify(config.workspace_id)}`),'real_react_login_shared_vue_session');
    const cookie=(await cdp.send('Network.getCookies',{urls:[origin]})).cookies.find(v=>v.name==='open_ai_canvas_session');check(!!cookie?.value&&cookie.httpOnly,'http_only_platform_cookie');check(await cdp.evaluate(`!!document.querySelector('[data-testid=asset-create]')`),'explicit_asset_registration_capability');
    report.stage='create_form';await click(cdp,'[data-testid=asset-create]');
    for(const [name,value] of Object.entries({asset_name:'Synthetic browser asset one',asset_kind:'raster',asset_data_class:'demo',asset_farm:'__unscoped__',asset_source_kind:'customer_data',asset_source_reference:'fixture-record',asset_sensitivity:'internal'}))await input(cdp,name,value);
    report.stage='initial_grants';
    for(const cap of ['agri.asset.read','agri.asset.create','agri.asset_download.read']){
      const expression=`[...document.querySelectorAll('[data-testid=asset-initial-grants] .farm-grant')].find(e=>e.querySelector('code')?.textContent===${JSON.stringify(config.username)})?.querySelector('input[value="${cap}"]')`;
      await cdp.poll(`!!(${expression})&&!(${expression}).matches(':disabled')`,'explicit grant available');await cdp.evaluate(`(${expression}).click()`);await cdp.poll(`(${expression}).checked`,'individual Vue grant committed');
    }
    check(await cdp.evaluate(`[...document.querySelectorAll('[data-testid=asset-initial-grants] .farm-grant')].find(e=>e.querySelector('code')?.textContent===${JSON.stringify(config.username)}).querySelectorAll('input:checked').length===3`),'explicit_initial_grant_plan');
    report.stage='create_preview';await click(cdp,'[data-testid=asset-preview]');await cdp.poll(`!!document.querySelector('[data-testid=asset-confirm]')`,'real asset preview');
    const preview=await cdp.evaluate(`JSON.parse(document.querySelector('[data-testid=asset-preview-summary]').textContent)`);check(preview.entity===null&&preview.status==='validating'&&preview.rights.status==='pending'&&preview.metadata.farm_id===null&&preview.metadata.source.source_id===null&&preview.initial_grants.length===1&&preview.initial_grants[0].subject_user_id===config.username&&preview.initial_grants[0].capabilities.length===3,'pending_rights_metadata_only_preview');
    report.stage='create_confirm';await click(cdp,'[data-testid=asset-confirm]',true);await completed(cdp);check(network.counts().creates===1,'duplicate_click_single_create');await control.wait('created');
    report.stage='create_reload';await reload();await open();check(await cdp.evaluate(`document.querySelector('[data-testid=asset-detail]').dataset.version==='1'`),'asset_survives_reload');
    const assetID=await cdp.evaluate(`document.querySelector('[data-testid=asset-list] li').dataset.assetId`);if(!/^asset_[a-f0-9]{32}$/.test(assetID))throw Error('invalid persisted asset id');
    const path=version=>'/api/agri/v1/assets/'+assetID+'/versions/'+version+'?workspace_id='+config.workspace_id;
    const read=async(version)=>api(cdp,`return request(${JSON.stringify(path(version))});`);
    const first=await read(1);check(first.status===200&&first.data.version===1&&first.data.revision===1&&first.data.entity===null&&first.data.status==='validating'&&first.data.data_class==='demo','metadata_not_ready_without_entity');check(first.data.rights.status==='pending'&&first.data.rights.review_ref===null&&/^[a-f0-9]{64}$/.test(first.data.rights.snapshot_hash)&&first.data.metadata.source.source_id===null,'unknown_source_rights_pending');
    report.stage='version_form';await click(cdp,'[data-testid=asset-version-edit]');await input(cdp,'asset_name','Synthetic browser asset two');await input(cdp,'asset_source_reference','fixture-record-two');await click(cdp,'[data-testid=asset-preview]');await cdp.poll(`!!document.querySelector('[data-testid=asset-confirm]')`,'version preview');report.stage='version_confirm';await click(cdp,'[data-testid=asset-confirm]');await completed(cdp);await control.wait('versioned');check(network.counts().revisions===1,'version_two_committed');
    report.stage='old_grant';const stale=[await read(1),await read(2)];
    const operationID=await cdp.evaluate(`document.querySelector('[data-testid=asset-operation] code')?.textContent`);if(!/^aop_[A-Za-z0-9_-]{1,120}$/.test(operationID))throw Error('actual operation receipt missing');
    await click(cdp,'[data-testid=asset-history-read]');await cdp.poll(`!!document.querySelector('[data-testid=asset-safe-receipt]')&&!document.querySelector('[data-testid=asset-busy]')`,'fresh owner-verified receipt');
    check(stale.every(r=>[403,404].includes(r.status)&&r.data==null)&&await cdp.evaluate(`(()=>{const receipt=document.querySelector('[data-testid=asset-safe-receipt]');const codes=[...receipt.querySelectorAll('code')].map(e=>e.textContent);return codes.length===2&&codes[0]===${JSON.stringify(operationID)}&&codes[1]===${JSON.stringify('/api/agri/v1/asset-operations/'+operationID+'?workspace_id='+config.workspace_id)}&&!receipt.textContent.includes(${JSON.stringify(assetID)})&&!document.querySelector('[data-testid=asset-detail]')&&!document.querySelector('[data-testid=asset-metadata-editor]')&&!document.querySelector('[data-testid=asset-preview-summary]')&&!document.querySelector('[data-testid=asset-list]');})()`),'old_grant_denies_current_and_history');
    report.stage='regrant';await control.wait('regranted');await reload();await open();const second=await read(2);check(second.status===200&&second.data.asset_id===assetID&&second.data.version===2&&second.data.revision===2&&second.data.metadata.name==='Synthetic browser asset two'&&second.data.asset_version_id!==first.data.asset_version_id,'explicit_regrant_restores_version_two');
    report.stage='history';await input(cdp,'asset_history_version','1');await click(cdp,'[data-testid=asset-history-read]');await cdp.poll(`!!document.querySelector('[data-testid=asset-historical]')`,'immutable historical asset');check(await cdp.evaluate(`document.querySelector('[data-testid=asset-detail]').dataset.version==='1'&&document.querySelector('[data-testid=asset-detail]').dataset.revision==='2'&&!document.querySelector('[data-testid=asset-version-edit]')`),'historical_version_one_readonly');
    const historical=await read(1);check(historical.status===200&&historical.data.revision===2&&historical.data.asset_version_id===first.data.asset_version_id&&historical.data.metadata_hash===first.data.metadata_hash&&historical.data.rights.snapshot_hash===first.data.rights.snapshot_hash&&historical.data.rights.status==='pending'&&historical.data.status==='validating'&&historical.data.entity===null&&!historical.data.allowed_actions.includes('agri.asset.create'),'history_uses_current_authority');
    // There is no object ID: a fabricated download URL returning 404 is not an authorization test.
    // The parent separately calls the production typed download authority at the regrant barrier.
    report.stage='download';check(!second.data.allowed_actions.includes('agri.asset_download.read')&&second.data.entity===null&&await cdp.evaluate(`!document.querySelector('[data-testid=asset-catalog] a[download]')&&!document.querySelector('[data-testid=asset-catalog] a[href*="/objects/"]')`),'not_ready_download_denied');
    report.stage='scope_negatives';for(const [scope,name] of [[config.foreign_workspace_id,'foreign_workspace_denied'],[config.second_workspace_id,'same_tenant_other_workspace_denied']]){const denied=await api(cdp,`const r=await request('/api/agri/v1/assets/'+${JSON.stringify(assetID)}+'/versions/1?workspace_id='+${JSON.stringify(scope)});return [403,404].includes(r.status)&&r.data==null;`);check(denied===true,name);}
    report.stage='revocation';await control.wait('revoke-request');const after=(await cdp.send('Network.getCookies',{urls:[origin]})).cookies.find(v=>v.name==='open_ai_canvas_session');check(after?.value===cookie.value,'same_cookie_after_revocation');const deniedAfter=await api(cdp,`return request('/api/agri/v1/assets/'+${JSON.stringify(assetID)}+'/versions/1?workspace_id='+${JSON.stringify(config.workspace_id)});`);if(![401,403,404].includes(deniedAfter.status)||deniedAfter.data!=null)throw Error('revoked request not denied');await cdp.evaluate(`window.dispatchEvent(new Event('pageshow'))`);await cdp.poll(`!document.querySelector('[data-testid=asset-detail]')&&!document.querySelector('[data-testid=asset-metadata-editor]')&&!document.querySelector('[data-testid=asset-preview-summary]')`,'revoked content cleared');check(true,'revoked_old_page_clears_content');await cdp.navigate('/agri/');await cdp.evaluate('history.back()');await cdp.poll(`location.pathname===${JSON.stringify(workspace)} && !!document.querySelector('main[data-session-status=error]') && !!document.querySelector('[role=alert]') && !document.querySelector('[data-testid=asset-detail]')&&!document.querySelector('[data-testid=asset-metadata-editor]')`,'revoked history cleared');check(true,'revoked_history_navigation_no_content');await control.wait('revoked-verified');check(control.complete(),'all_database_checkpoints_acknowledged');report.network=network.counts();check(report.network.observed>0&&report.network.violations===0,'no_internal_credentials_identity_spoofing_or_external_requests');report.stage='complete';report.result='pass';
  } catch(error) {report.result='fail';report.failure=safeFailure(error);process.exitCode=1;if(cdp){try{const flags=await cdp.evaluate(`(()=>{const has=s=>!!document.querySelector(s);return {login:has('#login-account'),session_ready:has('main[data-session-status=ready]'),context:has('[data-testid=asset-catalog]'),create:has('[data-testid=asset-create]'),detail:has('[data-testid=asset-detail]'),editor:has('[data-testid=asset-metadata-editor]'),preview:has('[data-testid=asset-confirm]'),busy:has('[data-testid=asset-busy]'),conflict:has('[data-testid=asset-conflict]'),error_code:document.querySelector('[data-testid=asset-error]')?.dataset.errorCode}})()`);report.ui_flags=safeUIFlags(flags);report.error_code=ERROR_CODES.includes(flags?.error_code)?flags.error_code:null;}catch{/* No raw DOM, response or exception text. */}}}
  finally {await cleanup();process.removeListener('SIGINT',interrupted);process.removeListener('SIGTERM',interrupted);if(config&&!existsSync(config.report_path))writeFileSync(config.report_path,JSON.stringify(report,null,2)+'\n',{flag:'wx',mode:0o600});}
  return report;
}
if(process.argv[1]&&import.meta.url===pathToFileURL(resolve(process.argv[1])).href)await main();
