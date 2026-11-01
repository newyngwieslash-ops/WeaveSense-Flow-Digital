import test from 'node:test';
import { runInNewContext } from 'node:vm';
import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import { chmodSync, linkSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { API_PATHS, CHECKPOINTS, ERROR_CODES, LOGIN_STAGES, OPERATION_STATES, REQUIRED_CHECKS, UI_BOOLEAN_KEYS, checkpointControl, readPrivateConfig, requestViolatesBoundary, safeAPIPath, safeAPIStatuses, safeFailure, safeUIFlags, selectOwnGrants, validAck, validateConfig } from './farm-browser-e2e.mjs';
const fixture = directory => ({ platform_url:'http://127.0.0.1:18080',gateway_port:18081,username:'fixture_user',password:'not-a-real-fixture-password',workspace_id:'ws_main',foreign_workspace_id:'ws_foreign',second_workspace_id:'ws_second',actor_id:'actor_main',report_path:join(directory,'report.json'),dictionary_version:'fixture-v1',region_north:'fixture-north',region_south:'fixture-south',area_units:['m2','ha','mu'] });

test('private configuration is exact literal-loopback schema, not extensible authority',()=>{
 const config=fixture('/tmp/farm-test');assert.equal(validateConfig(config,'/tmp/farm-test/config.json'),config);
 for(const platform_url of ['https://example.invalid','http://localhost:18080','http://user@127.0.0.1:18080','http://127.0.0.1:18080/path','http://127.0.0.1:1','http://127.0.0.1:65536'])assert.throws(()=>validateConfig({...config,platform_url},'/tmp/farm-test/config.json'));
 for(const gateway_port of [18080,1,65536,18081.5,'18081'])assert.throws(()=>validateConfig({...config,gateway_port},'/tmp/farm-test/config.json'));
 for(const extra of [{tenant_id:'forged'},{role:'admin'},{operator_code:'forged'},{service_key:'secret'}])assert.throws(()=>validateConfig({...config,...extra},'/tmp/farm-test/config.json'));
});
test('dictionary, actor, distinct scope and owned report constraints fail closed',()=>{
 const good=fixture('/tmp/farm-test');
 for(const changes of [{actor_id:'../secret'},{region_north:''},{region_south:good.region_north},{dictionary_version:'a\nb'},{area_units:[]},{area_units:['acre']},{area_units:['mu','mu']},{second_workspace_id:good.workspace_id},{report_path:'/tmp/elsewhere/report.json'},{report_path:'/tmp/farm-test/config.json'},{report_path:'/tmp/farm-test/../report.json'},{password:'short'}])assert.throws(()=>validateConfig({...good,...changes},'/tmp/farm-test/config.json'));
});
test('private config requires 0700 owned directory and 0600 regular single-link non-symlink file',()=>{
 const directory=mkdtempSync(join(tmpdir(),'farm-config-test-')),file=join(directory,'config.json');
 try{
  writeFileSync(file,JSON.stringify(fixture(directory)),{mode:0o600});assert.equal(readPrivateConfig(file).dictionary_version,'fixture-v1');
  chmodSync(file,0o644);assert.throws(()=>readPrivateConfig(file));chmodSync(file,0o600);
  const alias=join(directory,'alias.json');symlinkSync(file,alias);assert.throws(()=>readPrivateConfig(alias));
  const hard=join(directory,'hard.json');linkSync(file,hard);assert.throws(()=>readPrivateConfig(file));rmSync(hard);
  chmodSync(directory,0o755);assert.throws(()=>readPrivateConfig(file));chmodSync(directory,0o700);
  writeFileSync(file,' '.repeat(16385));assert.throws(()=>readPrivateConfig(file));
 }finally{chmodSync(directory,0o700);rmSync(directory,{recursive:true,force:true});}
});
test('ack parser rejects wrong stages, extra authority, duplicate keys, oversized and malformed messages',()=>{
 assert.equal(validAck('{"event":"ack","checkpoint":"created"}','created'),true);
 for(const line of ['null','[]','not json','{"event":"ack","checkpoint":"updated"}','{"event":"ack","checkpoint":"created","role":"admin"}','{"event":"ack","event":"ack","checkpoint":"created"}','x'.repeat(129)])assert.equal(validAck(line,'created'),false);
});
test('checkpoint controller blocks until matching ack and covers every database barrier in order',async()=>{
 const input=new PassThrough(),output=new PassThrough();let text='';output.on('data',data=>{text+=data});const controller=checkpointControl(input,output,1000);
 try{for(const checkpoint of CHECKPOINTS){let released=false;const waiting=controller.wait(checkpoint).then(()=>{released=true});await Promise.resolve();assert.equal(released,false);input.write(JSON.stringify({event:'ack',checkpoint})+'\n');await waiting;}assert.equal(controller.complete(),true);assert.deepEqual(text.trim().split('\n').map(JSON.parse),CHECKPOINTS.map(checkpoint=>({event:'checkpoint',checkpoint})));}finally{controller.close();input.destroy();output.destroy();}
});
test('checkpoint missing ack, early close and wrong ack fail rather than continuing',async()=>{
 for(const mode of ['timeout','close','wrong']){
  const input=new PassThrough(),output=new PassThrough(),controller=checkpointControl(input,output,20);const waiting=controller.wait('created');
  if(mode==='close')input.end();if(mode==='wrong')input.write('{"event":"ack","checkpoint":"updated"}\n');
  await assert.rejects(waiting);assert.equal(controller.complete(),false);await assert.rejects(controller.wait('updated'));controller.close();input.destroy();output.destroy();
 }
});
test('request observer allows explicit grantee/resource selectors but forbids internal credentials and client identity spoofing',()=>{
 const origin='http://127.0.0.1:18081',request={url:origin+'/api/agri/v1/farms',headers:{'Content-Type':'application/json'},postData:JSON.stringify({workspace_id:'ws',initial_grants:[{subject_user_id:'actor',capabilities:['agri.farm.read']}]})};
 assert.equal(requestViolatesBoundary(request,origin),false);
 for(const bad of [{...request,url:'https://external.invalid/tile'},{...request,url:origin+'/internal/agri/v1/farms'},{...request,url:origin+'/api/agriculture/_test/anything'},{...request,url:request.url+'?tenant_id=x'},{...request,headers:{'X-Agri-Delegation':'secret'}},{...request,postData:'{"user_id":"forged"}'},{...request,postData:'{"operator_code":"forged"}'}])assert.equal(requestViolatesBoundary(bad,origin),true);
});
test('failure output never reflects raw credentials, request bodies or database messages',()=>{
 assert.equal(safeFailure(Error('assertion failed: invalid_geometry_no_write')),'assertion failed: invalid_geometry_no_write');
 for(const message of ['password=secret','Cookie: secret','postgres://role:secret@host/db','{"contact":{"email":"private@example.com"}}'])assert.equal(safeFailure(Error(message)),'farm browser fixture failed; private diagnostics withheld');
});
test('login diagnostics have five fixed stages and only typed UI booleans or closed enums',()=>{
 assert.deepEqual(LOGIN_STAGES,['react_login_navigation','react_login_account','react_login_password','react_login_submit','react_login_workspace']);
 const values=Object.fromEntries(UI_BOOLEAN_KEYS.map(key=>[key,true]));
 assert.deepEqual(safeUIFlags({...values,error_code:'INVALID_RESPONSE',operation_state:'pending',password:'secret',body:{contact:'secret'}}),{...values,error_code:'INVALID_RESPONSE',operation_state:'pending'});
 assert.deepEqual(safeUIFlags({login:'secret',workspace:1,error_code:'PASSWORD_SECRET',operation_state:'secret'}),{error_code:null,operation_state:null});
 assert.deepEqual(safeUIFlags(null),{});assert.equal(UI_BOOLEAN_KEYS.length,14);assert.equal(ERROR_CODES.length,9);assert.equal(OPERATION_STATES.length,6);
});
test('API diagnostics retain only fixed paths and normalize known opaque IDs without query values',()=>{
 const origin='http://127.0.0.1:18081',id='farm_'+'a'.repeat(32);
 assert.equal(safeAPIPath(origin+'/api/agri/v1/context?workspace_id=private&token=secret',origin),'/api/agri/v1/context');
 assert.equal(safeAPIPath(origin+'/api/agri/v1/farms/'+id+'?workspace_id=private',origin),'/api/agri/v1/farms/:farm_id');
 assert.equal(safeAPIPath(origin+'/api/agri/v1/farms/'+id+'/archive',origin),'/api/agri/v1/farms/:farm_id/archive');
 assert.equal(safeAPIPath(origin+'/api/agri/v1/farm-operations/fop_private?token=secret',origin),'/api/agri/v1/farm-operations/:operation_id');
 for(const url of ['not a URL','https://external.invalid/api/auth/login',origin+'/api/agri/v1/farms/private',origin+'/api/agri/v1/farms/:farm_id',origin+'/internal/secret'])assert.equal(safeAPIPath(url,origin),null);
 assert.equal(API_PATHS.length,12);
});
test('API status output is bounded and strips reflected fields, bodies and dynamic paths',()=>{
 const good={path:'/api/agri/v1/context',status:503,completed:true,token:'secret',body:{contact:'secret'}};
 assert.deepEqual(safeAPIStatuses([good,{...good,path:good.path+'?workspace_id=private'},{...good,status:'503'},{...good,status:0},{...good,completed:1}]),[{path:good.path,status:503,completed:true}]);
 assert.equal(safeAPIStatuses(Array(100).fill(good)).length,80);assert.deepEqual(safeAPIStatuses(null),[]);
});
test('runner has fixed complete coverage, real form happy flows and sandboxed production nginx with no cookie injection',()=>{
 const source=readFileSync(new URL('./farm-browser-e2e.mjs',import.meta.url),'utf8');
 assert.equal(REQUIRED_CHECKS.length,23);assert.equal(new Set(REQUIRED_CHECKS).size,23);
 for(const required of ['real_react_login_shared_vue_session','stale_revision_409_preserves_draft','archive_ui_history_readonly','revoked_history_navigation_no_content'])assert.ok(REQUIRED_CHECKS.includes(required));
 for(const literal of ['deploy/agriculture/nginx.conf.template',"cdp.fill('#login-password'",'await fillCreate(cdp,config,side)',"await click(cdp,'[data-testid=farm-confirm]'",'history.back()',"await control.wait('revoked-verified')"])assert.ok(source.includes(literal));
 for(const forbidden of ['Network.setCookie','--no-sandbox','--disable-setuid-sandbox','startProxy','workbench-attributes'])assert.ok(!source.includes(forbidden));
 assert.ok(source.includes("process.getuid?.()===0"));assert.ok(source.includes('if(process.argv[1]&&import.meta.url==='));
});

test('explicit grant selection yields between checkbox clicks and verifies all retained choices',async()=>{
 let model=[],pending=false,clicks=0,polls=0;
 const boxes=['agri.farm.read','agri.farm.update','agri.farm.archive'].map(value=>({value,checked:false,model:[],matches:()=>false,click(){clicks++;this.checked=true;model=this.model.concat(this.value);pending=true;}}));
 const row={querySelector:()=>({textContent:'actor_main'}),querySelectorAll:()=>boxes};
 const document={querySelectorAll:()=>[row]};
 const evaluate=async expression=>{const result=runInNewContext(expression,{document});await Promise.resolve();if(pending){for(const box of boxes){box.model=model;box.checked=model.includes(box.value)}pending=false;}return result;};
 const cdp={evaluate,poll:async expression=>{polls++;assert.equal(await evaluate(expression),true)}};
 await selectOwnGrants(cdp,'actor_main');assert.deepEqual(model,boxes.map(e=>e.value));assert.equal(clicks,3);assert.equal(polls,3);
 boxes[0].matches=()=>true;await assert.rejects(selectOwnGrants(cdp,'actor_main'));assert.equal(clicks,3);
 await assert.rejects(selectOwnGrants(cdp,'missing_actor'));
});
