#!/usr/bin/env node
// Real React/Vue -> production Go farm routes -> signed GIS; fixtures, not live acceptance.
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { constants, closeSync, cpSync, existsSync, fstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { createInterface } from 'node:readline';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { connect, freePort, ready, stop } from './session-e2e.mjs';
import { renderGateway, requestLeaksInternalCredential } from './gateway-browser-e2e.mjs';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const fileHash = path => hash(readFileSync(path));
const identifier = value => typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value);
const label = value => typeof value === 'string' && /^[A-Za-z0-9_.-]{1,128}$/.test(value);
const validPort = value => Number.isInteger(value) && value >= 1024 && value <= 65535;
export const CHECKPOINTS = ['created', 'updated', 'archived', 'revoke-request', 'revoked-verified'];
export const LOGIN_STAGES = ['react_login_navigation','react_login_account','react_login_password','react_login_submit','react_login_workspace'];
export const UI_BOOLEAN_KEYS = ['login','account_input','password_input','submit_button','session_ready','session_loading','session_error','workspace','workspace_context','create_button','editor','preview','busy','validation'];
export const ERROR_CODES = ['REVISION_CONFLICT','STATE_CONFLICT','INVALID_GEOMETRY','INVALID_ARGUMENT','INVALID_RESPONSE','DEPENDENCY_UNAVAILABLE','AUTHORITY_UNAVAILABLE','NETWORK_ERROR','TIMEOUT'];
export const OPERATION_STATES = ['preview','pending','completed','rejected','reconciliation_required','unknown'];
export const API_PATHS = ['/api/auth/settings','/api/auth/csrf','/api/auth/login','/api/auth/session','/api/auth/logout','/api/agriculture/workspaces','/api/agri/v1/context','/api/agri/v1/farms','/api/agri/v1/farm-previews','/api/agri/v1/farms/:farm_id','/api/agri/v1/farms/:farm_id/archive','/api/agri/v1/farm-operations/:operation_id'];
export function safeUIFlags(value) {
  if(!value||typeof value!=='object'||Array.isArray(value))return {};
  const out={};for(const key of UI_BOOLEAN_KEYS)if(typeof value[key]==='boolean')out[key]=value[key];
  out.error_code=ERROR_CODES.includes(value.error_code)?value.error_code:null;
  out.operation_state=OPERATION_STATES.includes(value.operation_state)?value.operation_state:null;
  return out;
}
export function safeAPIPath(url, origin) {
  let value;try{value=new URL(url);}catch{return null;}
  if(value.origin!==origin)return null;
  const path=value.pathname;
  if(API_PATHS.includes(path)&&!path.includes(':'))return path;
  if(/^\/api\/agri\/v1\/farms\/farm_[a-f0-9]{32}$/.test(path))return '/api/agri/v1/farms/:farm_id';
  if(/^\/api\/agri\/v1\/farms\/farm_[a-f0-9]{32}\/archive$/.test(path))return '/api/agri/v1/farms/:farm_id/archive';
  if(/^\/api\/agri\/v1\/farm-operations\/fop_[A-Za-z0-9_-]{1,120}$/.test(path))return '/api/agri/v1/farm-operations/:operation_id';
  return null;
}
export function safeAPIStatuses(values) {
  return Array.isArray(values)?values.slice(-80).filter(row=>row&&API_PATHS.includes(row.path)&&Number.isInteger(row.status)&&row.status>=100&&row.status<=599&&typeof row.completed==='boolean').map(row=>({path:row.path,status:row.status,completed:row.completed})):[];
}
export const REQUIRED_CHECKS = [
  'production_nginx_configuration_valid', 'real_react_login_shared_vue_session', 'http_only_platform_cookie',
  'north_created_with_explicit_grants', 'south_created_with_explicit_grants', 'duplicate_click_single_create',
  'created_farms_survive_reload', 'explicit_region_unit_crs_demo', 'openlayers_real_canvas',
  'invalid_geometry_no_write', 'missing_crs_no_preview', 'duplicate_external_code_rejected',
  'north_ui_update_revision_two', 'concurrent_real_api_update_revision_three', 'stale_revision_409_preserves_draft',
  'archive_ui_history_readonly', 'foreign_workspace_denied', 'same_tenant_other_workspace_denied',
  'same_cookie_after_revocation', 'revoked_old_page_clears_content', 'revoked_history_navigation_no_content',
  'all_database_checkpoints_acknowledged', 'no_internal_credentials_identity_spoofing_or_external_requests',
];
export function validateConfig(value, path) {
  const keys = ['platform_url','gateway_port','username','password','workspace_id','foreign_workspace_id','second_workspace_id','actor_id','report_path','dictionary_version','region_north','region_south','area_units'];
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).sort().join() !== keys.sort().join()) throw Error('invalid private farm configuration');
  if (!/^http:\/\/127\.0\.0\.1:[0-9]+$/.test(value.platform_url) || !validPort(Number(new URL(value.platform_url).port)) || !validPort(value.gateway_port) || Number(new URL(value.platform_url).port) === value.gateway_port) throw Error('literal loopback ports required');
  for (const key of ['username','workspace_id','foreign_workspace_id','second_workspace_id','actor_id']) if (!identifier(value[key])) throw Error('invalid private identifier');
  if (new Set([value.workspace_id,value.foreign_workspace_id,value.second_workspace_id]).size !== 3) throw Error('distinct scopes required');
  for (const key of ['dictionary_version','region_north','region_south']) if (!label(value[key])) throw Error('invalid dictionary fixture label');
  if (value.region_north === value.region_south || !Array.isArray(value.area_units) || !value.area_units.length || new Set(value.area_units).size !== value.area_units.length || value.area_units.some(x => !['m2','ha','mu'].includes(x))) throw Error('invalid region/unit fixture');
  if (typeof value.password !== 'string' || value.password.length < 12 || value.password.length > 256) throw Error('invalid fixture account');
  if (typeof value.report_path !== 'string' || resolve(value.report_path) !== value.report_path || dirname(value.report_path) !== dirname(resolve(path)) || value.report_path === resolve(path) || !/^[A-Za-z0-9_-]+\.json$/.test(value.report_path.split('/').at(-1))) throw Error('owned report path required');
  return value;
}
export function readPrivateConfig(path) {
  const directory = statSync(dirname(resolve(path)));
  if (!directory.isDirectory() || (directory.mode & 0o777) !== 0o700 || directory.uid !== process.getuid?.()) throw Error('owned private directory required');
  const descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const stat = fstatSync(descriptor);
    if (!stat.isFile() || (stat.mode & 0o777) !== 0o600 || stat.size > 16384 || stat.nlink !== 1 || stat.uid !== process.getuid?.()) throw Error('private config permissions invalid');
    return validateConfig(JSON.parse(readFileSync(descriptor, 'utf8')), path);
  } finally { closeSync(descriptor); }
}
export function validAck(line, checkpoint) {
  if (!CHECKPOINTS.includes(checkpoint) || typeof line !== 'string' || line.length > 128 || (line.match(/"event"\s*:/g) || []).length !== 1 || (line.match(/"checkpoint"\s*:/g) || []).length !== 1) return false;
  try { const v = JSON.parse(line); return !!v && Object.keys(v).sort().join() === 'checkpoint,event' && v.event === 'ack' && v.checkpoint === checkpoint; } catch { return false; }
}
export function checkpointControl(input, output, deadline = 60000) {
  const lines = createInterface({ input, crlfDelay: Infinity });
  let current, index = 0, broken = false;
  lines.on('line', line => {
    if (!current || !validAck(line, CHECKPOINTS[index])) { broken = true; current?.reject(Error('invalid checkpoint acknowledgement')); return; }
    const waiter = current; current = null; index++; waiter.resolve();
  });
  lines.on('close', () => { if (index !== CHECKPOINTS.length) { broken = true; current?.reject(Error('parent closed before checks')); } });
  return {
    wait(checkpoint) {
      if (broken || current || CHECKPOINTS[index] !== checkpoint) return Promise.reject(Error('checkpoint order invalid'));
      return new Promise((resolveWait, rejectWait) => {
        const timer = setTimeout(() => { broken = true; current = null; rejectWait(Error('database checkpoint deadline expired')); }, deadline);
        current = { resolve: () => { clearTimeout(timer); resolveWait(); }, reject: error => { clearTimeout(timer); rejectWait(error); } };
        output.write(JSON.stringify({ event: 'checkpoint', checkpoint }) + '\n');
      });
    },
    complete: () => !broken && index === CHECKPOINTS.length,
    close: () => lines.close(),
  };
}
export function safeFailure(error) { return /^assertion failed: [a-z0-9_]+$/.test(error?.message || '') ? error.message : 'farm browser fixture failed; private diagnostics withheld'; }
export function requestViolatesBoundary(request, origin) {
  if (requestLeaksInternalCredential(request, origin)) return true;
  const url = new URL(request.url);
  if (url.pathname.includes('/_test/') || ['tenant_id','user_id','role','operator_code'].some(key => url.searchParams.has(key))) return true;
  return /"(?:tenant_id|user_id|role|operator_code)"\s*:/.test(request.postData || '');
}
async function observer(port, origin) {
  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const socket = new WebSocket(targets.find(x => x.type === 'page').webSocketDebuggerUrl);
  await new Promise((ok,no) => { socket.addEventListener('open',ok,{once:true}); socket.addEventListener('error',no,{once:true}); });
  const counts = { observed: 0, violations: 0, previews: 0, creates: 0, updates: 0, archives: 0 };
  const statuses=[],pending=new Map();
  socket.addEventListener('message', event => {
    const value = JSON.parse(event.data);
    if(value.method==='Network.responseReceived'){
      const response=value.params.response,path=safeAPIPath(response.url,origin);
      if(path&&Number.isInteger(response.status)&&response.status>=100&&response.status<=599){const row={path,status:response.status,completed:false};statuses.push(row);pending.set(value.params.requestId,row);if(statuses.length>80){const removed=statuses.shift();for(const [id,item] of pending)if(item===removed)pending.delete(id);}}
    }else if(value.method==='Network.loadingFinished'){
      const row=pending.get(value.params.requestId);if(row){row.completed=true;pending.delete(value.params.requestId);}
    }
    if (value.method !== 'Network.requestWillBeSent') return;
    const req = value.params.request; counts.observed++;
    if (requestViolatesBoundary(req, origin)) counts.violations++;
    const path = new URL(req.url).pathname;
    if (req.method === 'POST' && path === '/api/agri/v1/farm-previews') counts.previews++;
    if (req.method === 'POST' && path === '/api/agri/v1/farms') counts.creates++;
    if (req.method === 'PATCH' && /^\/api\/agri\/v1\/farms\/farm_[a-f0-9]{32}$/.test(path)) counts.updates++;
    if (req.method === 'POST' && /^\/api\/agri\/v1\/farms\/farm_[a-f0-9]{32}\/archive$/.test(path)) counts.archives++;
  });
  await new Promise((ok,no) => {
    const timer = setTimeout(() => no(Error('network observer unavailable')),10000);
    const response = event => { const v=JSON.parse(event.data); if(v.id===1){clearTimeout(timer);socket.removeEventListener('message',response);v.error?no(Error('network observer rejected')):ok();} };
    socket.addEventListener('message',response);socket.send(JSON.stringify({id:1,method:'Network.enable'}));
  });
  return { counts: () => ({ ...counts }), statuses:()=>safeAPIStatuses(statuses), close: () => socket.close() };
}
async function setInput(cdp, selector, value) {
  await cdp.poll(`!!document.querySelector(${JSON.stringify(selector)}) && !document.querySelector(${JSON.stringify(selector)}).disabled`, 'enabled input');
  await cdp.evaluate(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.value='';e.dispatchEvent(new Event('input',{bubbles:true}));})()`);
  await cdp.fill(selector, value, 'farm input');
}
async function select(cdp, name, value) {
  return cdp.evaluate(`(()=>{const e=document.querySelector(${JSON.stringify(`[name="${name}"]`)});if(!e||e.disabled)return false;const option=${value === null ? '[...e.options].find(o=>o.value&&!o.disabled)?.value' : JSON.stringify(value)};if(![...e.options].some(o=>o.value===option))return false;e.value=option;e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));return true;})()`);
}
async function click(cdp, selector, twice = false) {
  await cdp.poll(`!!document.querySelector(${JSON.stringify(selector)}) && !document.querySelector(${JSON.stringify(selector)}).disabled`, 'enabled control');
  await cdp.evaluate(`(()=>{const button=document.querySelector(${JSON.stringify(selector)});button.click();${twice ? 'button.click();' : ''}})()`);
}
async function operationComplete(cdp) {
  const end = Date.now() + 60000;
  while (Date.now() < end) {
    const state = await cdp.evaluate(`document.querySelector('[data-testid=farm-operation]')?.dataset.operationState || ''`);
    if (state === 'completed') return;
    if (['rejected','reconciliation_required'].includes(state)) throw Error('farm operation not completed');
    if (await cdp.evaluate(`!!document.querySelector('[data-testid=farm-error]')`)) throw Error('farm operation request failed');
    if (['pending','preview','unknown'].includes(state)) await cdp.evaluate(`(()=>{const e=document.querySelector('[data-testid=farm-operation-poll]');if(e&&!e.disabled)e.click()})()`);
    await new Promise(done => setTimeout(done, 200));
  }
  throw Error('farm operation completion deadline expired');
}
async function openFarm(cdp, code) {
  await cdp.poll(`[...document.querySelectorAll('[data-testid=farm-list] li')].some(row=>row.textContent.includes(${JSON.stringify(code)}))`, 'persisted farm row');
  await cdp.evaluate(`[...document.querySelectorAll('[data-testid=farm-list] li')].find(row=>row.textContent.includes(${JSON.stringify(code)})).querySelector('button').click()`);
  await cdp.poll(`!!document.querySelector('[data-testid=farm-detail]') && !document.querySelector('[data-testid=farm-busy]')`, 'farm detail');
}
async function fillCreate(cdp, config, side, code = `browser-${side}`) {
  await click(cdp, '[data-testid=farm-create]');
  await setInput(cdp,'[name=external_code]',code);
  await setInput(cdp,'[name=name]',`Browser ${side} fixture`);
  await setInput(cdp,'[name=operator_name]','Browser fixture cooperative');
  const unit = config.area_units[side === 'south' ? config.area_units.length - 1 : 0];
  if (!await select(cdp,'region_code',config[`region_${side}`]) || !await select(cdp,'timezone',null) || !await select(cdp,'area_unit',unit) || !await select(cdp,'source_crs','EPSG:4326') || !await select(cdp,'data_class','demo')) throw Error('explicit configured option unavailable');
  const [x,y] = side === 'north' ? [121,31] : [113,23];
  await setInput(cdp,'[name=geometry]',JSON.stringify({type:'Polygon',coordinates:[[[x,y],[x+0.001,y],[x+0.001,y+0.001],[x,y+0.001],[x,y]]]}));
  await selectOwnGrants(cdp, config.actor_id);
}
export async function selectOwnGrants(cdp, actorID) {
  const boxes = `(()=>{const row=[...document.querySelectorAll('.farm-grant')].find(row=>row.querySelector('code')?.textContent===${JSON.stringify(actorID)});return row?[...row.querySelectorAll('input[type=checkbox]')]:[]})()`;
  if (!await cdp.evaluate(`(()=>{const boxes=${boxes};return boxes.length===3 && ['agri.farm.read','agri.farm.update','agri.farm.archive'].every(cap=>boxes.some(e=>e.value===cap&&!e.matches(':disabled')))})()`)) throw Error('explicit member grant controls unavailable');
  // Each browser command yields to Vue's render microtask. Synchronous clicks
  // share stale v-model array values and can silently retain only the last grant.
  for (let index=0;index<3;index++) {
    if (!await cdp.evaluate(`(()=>{const boxes=${boxes};const e=boxes[${index}];if(!e||e.matches(':disabled'))return false;if(!e.checked)e.click();return true})()`)) throw Error('explicit member grant controls unavailable');
    await cdp.poll(`(()=>{const boxes=${boxes};return boxes.length===3&&boxes.slice(0,${index+1}).every(e=>e.checked&&!e.matches(':disabled'))})()`, 'explicit grant retained after Vue update');
  }
  if (!await cdp.evaluate(`(()=>{const boxes=${boxes};return boxes.length===3&&boxes.every(e=>e.checked&&!e.matches(':disabled'))})()`)) throw Error('explicit member grants not retained');
}
async function sameCookieAPI(cdp, expression) {
  // Negative/race setup only; primary create/update/archive use rendered forms.
  return cdp.evaluate(`(async()=>{const request=async(path,method='GET',body)=>{const headers={Accept:'application/json'};if(method!=='GET'){const r=await fetch('/api/auth/csrf',{credentials:'same-origin',cache:'no-store'});const e=await r.json();if(!r.ok||e.code!==0)throw Error('csrf unavailable');headers['X-CSRF-Token']=e.data.token;headers['Content-Type']='application/json';headers['Idempotency-Key']=crypto.randomUUID();}const r=await fetch(path,{method,credentials:'same-origin',cache:'no-store',headers,...(body?{body:JSON.stringify(body)}:{})});const envelope=await r.json();return {status:r.status,data:envelope.data};};${expression}})()`, 'private negative fixture');
}
export async function main(configPath = process.argv[2]) {
  const report = { schema_version:'1.0',result:'not_run',stage:'preflight',results:[],data_class:'fixture',production_acceptance:false,limitations:['Real production HTTP routes, React/Vue/nginx/Chromium and parent-verified dual PostgreSQL fixtures, not live farm acceptance.','One same-session production API update creates the revision race only; primary mutations are rendered form interactions.','History navigation revalidates authority; actual BFCache eligibility is not assumed.'] };
  let config,temp,nginx,browser,cdp,network,control,cleanupPromise;
  const cleanup = () => cleanupPromise ||= (async()=>{control?.close();network?.close();cdp?.close();await Promise.all([stop(browser),stop(nginx)]);if(temp)rmSync(temp,{recursive:true,force:true});})();
  const interrupted = () => { report.result='fail';report.failure='owned farm fixture interrupted';void cleanup().finally(()=>process.exit(1)); };
  process.once('SIGTERM',interrupted);process.once('SIGINT',interrupted);
  const check = (ok,name) => { if(!REQUIRED_CHECKS.includes(name)||!ok||report.results.some(x=>x.name===name))throw Error(`assertion failed: ${REQUIRED_CHECKS.includes(name)?name:'invalid_check'}`);report.results.push({name,result:'pass'}); };
  try {
    if(process.getuid?.()===0||!configPath)throw Error('non-root private configuration required');
    config=readPrivateConfig(configPath);if(existsSync(config.report_path))throw Error('report already exists');
    control=checkpointControl(process.stdin,process.stdout);
    const chrome=process.env.CHROME_BIN||'/usr/bin/chromium',nginxBin=process.env.NGINX_BIN||'/usr/sbin/nginx',mimeTypes=resolve(process.env.NGINX_MIME_TYPES||'/etc/nginx/mime.types');
    const react=resolve(process.env.AGRI_E2E_REACT_DIST||join(ROOT,'web/dist-agriculture')),vue=resolve(process.env.AGRI_E2E_VUE_DIST||join(ROOT,'services/agri-gis/frontend/dist-agriculture')),template=join(ROOT,'deploy/agriculture/nginx.conf.template');
    for(const file of [chrome,nginxBin,mimeTypes,template,join(react,'index.html'),join(vue,'index.html')])if(!existsSync(file))throw Error('actual runtime or assets absent');
    report.artifact_hashes={nginx_template:fileHash(template),react_index:fileHash(join(react,'index.html')),vue_index:fileHash(join(vue,'index.html')),runner:fileHash(fileURLToPath(import.meta.url))};
    report.source_commit=process.env.AGRI_E2E_SOURCE_COMMIT||spawnSync('git',['rev-parse','HEAD'],{cwd:ROOT,encoding:'utf8'}).stdout.trim();if(!/^[a-f0-9]{40}$/.test(report.source_commit))throw Error('source commit required');
    const provenance=join(ROOT,'.local/evidence/session-ci-provenance.json');if(existsSync(provenance)){if(JSON.parse(readFileSync(provenance,'utf8')).source_commit!==report.source_commit)throw Error('provenance mismatch');report.ci_provenance_sha256=fileHash(provenance);}
    temp=mkdtempSync(join(tmpdir(),'agri-farm-browser-'));cpSync(react,join(temp,'html'),{recursive:true});cpSync(vue,join(temp,'html/agri'),{recursive:true});cpSync(join(ROOT,'deploy/agriculture/errors'),join(temp,'html/_agri_errors'),{recursive:true});
    for(const name of ['client','proxy','fastcgi','uwsgi','scgi'])mkdirSync(join(temp,name));
    const origin=`http://127.0.0.1:${config.gateway_port}`,nginxConfig=join(temp,'nginx.conf');
    writeFileSync(nginxConfig,renderGateway(readFileSync(template,'utf8'),{directory:temp,port:config.gateway_port,apiPort:Number(new URL(config.platform_url).port),mimeTypes}),{mode:0o600});
    check(spawnSync(nginxBin,['-t','-p',temp+'/','-c',nginxConfig],{stdio:'ignore'}).status===0,'production_nginx_configuration_valid');
    nginx=spawn(nginxBin,['-p',temp+'/','-c',nginxConfig,'-g','daemon off;'],{stdio:'ignore',env:{PATH:process.env.PATH}});await ready(origin+'/api/auth/settings',nginx,30000);
    const chromePort=await freePort();browser=spawn(chrome,['--headless=new','--disable-background-networking','--no-first-run',`--remote-debugging-port=${chromePort}`,`--user-data-dir=${join(temp,'chrome')}`,'about:blank'],{stdio:'ignore',env:{PATH:process.env.PATH,HOME:temp,XDG_CONFIG_HOME:join(temp,'config'),XDG_CACHE_HOME:join(temp,'cache')}});await ready(`http://127.0.0.1:${chromePort}/json/version`,browser,30000);
    cdp=await connect(chromePort,origin);network=await observer(chromePort,origin);
    const workspacePath='/agri/workspaces/'+config.workspace_id;
    const workspaceReady=()=>cdp.poll(`!!document.querySelector('main[data-session-status=ready]') && !!document.querySelector('[data-testid=farm-create]') && !document.querySelector('[data-testid=farm-busy]')`,'authorized farm workspace');
    const reload=async()=>{await cdp.navigate(workspacePath);await workspaceReady();};
    report.stage='react_login_navigation';await cdp.navigate('/login?next='+encodeURIComponent(workspacePath));
    report.stage='react_login_account';await cdp.fill('#login-account',config.username,'fixture account');
    report.stage='react_login_password';await cdp.fill('#login-password',config.password,'fixture password');
    report.stage='react_login_submit';await click(cdp,'form button[type=submit]');
    report.stage='react_login_workspace';await workspaceReady();
    check(await cdp.evaluate(`document.querySelector('[data-testid=agri-workspace-context]')?.dataset.workspaceId===${JSON.stringify(config.workspace_id)}`),'real_react_login_shared_vue_session');
    const cookie=(await cdp.send('Network.getCookies',{urls:[origin]})).cookies.find(c=>c.name==='open_ai_canvas_session');check(!!cookie?.httpOnly&&!!cookie.value,'http_only_platform_cookie');
    report.stage='create_two_regions';
    for(const side of ['north','south']){report.stage=`create_${side}_form`;await fillCreate(cdp,config,side);report.stage=`create_${side}_preview`;await click(cdp,'[data-testid=farm-preview]');await cdp.poll(`!!document.querySelector('[data-testid=farm-confirm]')`,'concrete preview');report.stage=`create_${side}_confirm`;await click(cdp,'[data-testid=farm-confirm]',true);await operationComplete(cdp);check(true,`${side}_created_with_explicit_grants`);report.stage=`create_${side}_reload`;await reload();}
    check(network.counts().creates===2,'duplicate_click_single_create');
    check(await cdp.evaluate(`document.querySelectorAll('[data-testid=farm-list] li').length===2`),'created_farms_survive_reload');
    await openFarm(cdp,'browser-north');await cdp.poll(`!!document.querySelector('[data-testid=farm-map] canvas')`,'actual OL canvas');check(true,'openlayers_real_canvas');
    const facts=await sameCookieAPI(cdp,`const result=await request('/api/agri/v1/farms?workspace_id='+${JSON.stringify(config.workspace_id)});if(result.status!==200||!Array.isArray(result.data))return false;for(const row of result.data){const detail=await request('/api/agri/v1/farms/'+row.id+'?workspace_id='+${JSON.stringify(config.workspace_id)});if(detail.status!==200||detail.data.data_class!=='demo'||detail.data.source_crs!=='EPSG:4326'||detail.data.dictionary_version!==${JSON.stringify(config.dictionary_version)}||!${JSON.stringify(config.area_units)}.includes(detail.data.area_unit))return false;}return result.data.some(x=>x.region_code===${JSON.stringify(config.region_north)})&&result.data.some(x=>x.region_code===${JSON.stringify(config.region_south)});`);check(facts,'explicit_region_unit_crs_demo');
    await control.wait('created');
    report.stage='invalid_input_negatives';await reload();await fillCreate(cdp,config,'north','browser-invalid');
    const before=network.counts();await setInput(cdp,'[name=geometry]','{"type":"Polygon","coordinates":[]}');await click(cdp,'[data-testid=farm-preview]');await cdp.poll(`!!document.querySelector('[data-testid=farm-validation]')`,'invalid geometry rejected');check(network.counts().previews===before.previews&&network.counts().creates===before.creates,'invalid_geometry_no_write');
    await reload();await fillCreate(cdp,config,'north','browser-missing-crs');await select(cdp,'source_crs','');const previews=network.counts().previews;await click(cdp,'[data-testid=farm-preview]');check(await cdp.evaluate(`document.querySelector('[name=source_crs]').validity.valueMissing`)&&network.counts().previews===previews,'missing_crs_no_preview');
    await reload();await fillCreate(cdp,config,'north','browser-north');await click(cdp,'[data-testid=farm-preview]');await cdp.poll(`!!document.querySelector('[data-testid=farm-error]') || !!document.querySelector('[data-testid=farm-confirm]')`,'duplicate code preview result');
    if(await cdp.evaluate(`!!document.querySelector('[data-testid=farm-confirm]')`)){await click(cdp,'[data-testid=farm-confirm]');await cdp.poll(`!!document.querySelector('[data-testid=farm-error]') || document.querySelector('[data-testid=farm-operation]')?.dataset.operationState==='rejected'`,'duplicate rejected');}
    check(await cdp.evaluate(`['REVISION_CONFLICT','STATE_CONFLICT'].includes(document.querySelector('[data-testid=farm-error]')?.dataset.errorCode) || document.querySelector('[data-testid=farm-operation]')?.dataset.operationState==='rejected'`),'duplicate_external_code_rejected');
    report.stage='ui_update_and_stale_revision';await reload();await openFarm(cdp,'browser-north');await click(cdp,'[data-testid=farm-edit]');await setInput(cdp,'[name=name]','Browser north updated');await click(cdp,'[data-testid=farm-preview]');await click(cdp,'[data-testid=farm-confirm]');await operationComplete(cdp);
    await reload();await openFarm(cdp,'browser-north');check(await cdp.evaluate(`document.querySelector('[data-testid=farm-detail]').textContent.includes('Browser north updated') && document.querySelector('.farm-facts dd').textContent.trim().endsWith('/ 2')`),'north_ui_update_revision_two');
    await click(cdp,'[data-testid=farm-edit]');await setInput(cdp,'[name=name]','Browser north stale draft');
    const raced=await sameCookieAPI(cdp,`const ws=${JSON.stringify(config.workspace_id)};const page=await request('/api/agri/v1/farms?workspace_id='+ws);const row=page.data.find(x=>x.external_code==='browser-north');const preview=await request('/api/agri/v1/farm-previews','POST',{workspace_id:ws,action:'update',farm_id:row.id,expected_revision:2,farm:{operator_name:'Concurrent fixture cooperative'}});if(preview.status!==200)return false;const result=await request('/api/agri/v1/farms/'+row.id,'PATCH',{workspace_id:ws,preview_id:preview.data.preview_id,expected_revision:2,farm:{operator_name:'Concurrent fixture cooperative'}});return result.status===200&&result.data.state==='completed'&&result.data.revision===3;`);check(raced,'concurrent_real_api_update_revision_three');
    await click(cdp,'[data-testid=farm-preview]');await cdp.poll(`!!document.querySelector('[data-testid=farm-conflict]')`,'stale revision conflict');check(await cdp.evaluate(`document.querySelector('[name=name]').value==='Browser north stale draft'`),'stale_revision_409_preserves_draft');await control.wait('updated');
    report.stage='archive_history';await reload();await openFarm(cdp,'browser-north');await setInput(cdp,'[name=archive_reason]','Browser fixture archive');await click(cdp,'[data-testid=farm-archive-preview]');await click(cdp,'[data-testid=farm-confirm]');await operationComplete(cdp);await reload();await select(cdp,'farm_status','archived');await click(cdp,'[data-testid=farm-search]');await openFarm(cdp,'browser-north');
    check(await cdp.evaluate(`document.querySelector('[data-testid=farm-detail]').textContent.includes('archived') && !document.querySelector('[data-testid=farm-edit]') && !document.querySelector('[name=archive_reason]')`),'archive_ui_history_readonly');await control.wait('archived');
    report.stage='cross_scope_negatives';
    for(const [scope,name] of [[config.foreign_workspace_id,'foreign_workspace_denied'],[config.second_workspace_id,'same_tenant_other_workspace_denied']]){const denied=await sameCookieAPI(cdp,`const result=await request('/api/agri/v1/farms?workspace_id='+${JSON.stringify(scope)});return result.status===404;`);check(denied,name);}
    await reload();await openFarm(cdp,'browser-south');report.stage='awaiting_revocation';await control.wait('revoke-request');
    const after=(await cdp.send('Network.getCookies',{urls:[origin]})).cookies.find(c=>c.name==='open_ai_canvas_session');check(after?.value===cookie.value,'same_cookie_after_revocation');
    await cdp.evaluate(`window.dispatchEvent(new Event('pageshow'))`);await cdp.poll(`!document.querySelector('[data-testid=farm-workspace]') && !document.body.textContent.includes('Browser south fixture')`,'revoked page clears');check(true,'revoked_old_page_clears_content');
    await cdp.navigate('/agri/');await cdp.evaluate('history.back()');await cdp.poll(`location.pathname===${JSON.stringify(workspacePath)} && !!document.querySelector('main[data-session-status=error]') && !document.querySelector('[data-testid=farm-workspace]') && !!document.querySelector('[role=alert]')`,'revoked history remains denied');check(true,'revoked_history_navigation_no_content');await control.wait('revoked-verified');check(control.complete(),'all_database_checkpoints_acknowledged');
    report.network=network.counts();check(report.network.observed>0&&report.network.violations===0&&cdp.exceptions.length===0&&cdp.outboundBlocked()===0,'no_internal_credentials_identity_spoofing_or_external_requests');
    if(report.results.length!==REQUIRED_CHECKS.length)throw Error('required coverage incomplete');report.stage='complete';report.result='pass';
  } catch(error){
    report.result='fail';report.failure=safeFailure(error);report.browser_runtime_exception_count=cdp?.exceptions.length??0;process.exitCode=1;
    report.api_statuses=network?.statuses()??[];
    if(cdp)try{report.ui_flags=safeUIFlags(await cdp.evaluate(`(()=>{const code=document.querySelector('[data-testid=farm-error]')?.dataset.errorCode;const state=document.querySelector('[data-testid=farm-operation]')?.dataset.operationState;return {login:!!document.querySelector('#login-account'),account_input:!!document.querySelector('#login-account'),password_input:!!document.querySelector('#login-password'),submit_button:!!document.querySelector('form button[type=submit]'),session_ready:!!document.querySelector('main[data-session-status=ready]'),session_loading:!!document.querySelector('main[data-session-status=loading]'),session_error:!!document.querySelector('main[data-session-status=error]'),workspace:!!document.querySelector('[data-testid=farm-workspace]'),workspace_context:!!document.querySelector('[data-testid=agri-workspace-context]'),create_button:!!document.querySelector('[data-testid=farm-create]'),editor:!!document.querySelector('[data-testid=farm-editor]'),preview:!!document.querySelector('[data-testid=farm-operation-preview]'),busy:!!document.querySelector('[data-testid=farm-busy]'),validation:!!document.querySelector('[data-testid=farm-validation]'),error_code:${JSON.stringify(ERROR_CODES)}.includes(code)?code:null,operation_state:${JSON.stringify(OPERATION_STATES)}.includes(state)?state:null};})()`,'sanitized component flags'));}catch{/* Never retain raw DOM or exception text. */}
  }
  finally{await cleanup();process.removeListener('SIGTERM',interrupted);process.removeListener('SIGINT',interrupted);if(config&&!existsSync(config.report_path))writeFileSync(config.report_path,JSON.stringify(report,null,2)+'\n',{flag:'wx',mode:0o600});}
  return report;
}
if(process.argv[1]&&import.meta.url===pathToFileURL(resolve(process.argv[1])).href)await main();
