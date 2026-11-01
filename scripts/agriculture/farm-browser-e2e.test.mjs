import test from 'node:test';
import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import { chmodSync, linkSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { CHECKPOINTS, REQUIRED_CHECKS, checkpointControl, readPrivateConfig, requestViolatesBoundary, safeFailure, validAck, validateConfig } from './farm-browser-e2e.mjs';
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
test('runner has fixed complete coverage, real form happy flows and sandboxed production nginx with no cookie injection',()=>{
 const source=readFileSync(new URL('./farm-browser-e2e.mjs',import.meta.url),'utf8');
 assert.equal(REQUIRED_CHECKS.length,23);assert.equal(new Set(REQUIRED_CHECKS).size,23);
 for(const required of ['real_react_login_shared_vue_session','stale_revision_409_preserves_draft','archive_ui_history_readonly','revoked_history_navigation_no_content'])assert.ok(REQUIRED_CHECKS.includes(required));
 for(const literal of ['deploy/agriculture/nginx.conf.template',"cdp.fill('#login-password'",'await fillCreate(cdp,config,side)',"await click(cdp,'[data-testid=farm-confirm]'",'history.back()',"await control.wait('revoked-verified')"])assert.ok(source.includes(literal));
 for(const forbidden of ['Network.setCookie','--no-sandbox','--disable-setuid-sandbox','startProxy','workbench-attributes'])assert.ok(!source.includes(forbidden));
 assert.ok(source.includes("process.getuid?.()===0"));assert.ok(source.includes('if(process.argv[1]&&import.meta.url==='));
});
