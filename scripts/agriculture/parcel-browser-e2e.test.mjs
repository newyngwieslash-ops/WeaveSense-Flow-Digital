import test from 'node:test';
import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import { chmodSync, linkSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { CHECKPOINTS, REQUIRED_CHECKS, STAGES, UI_FLAGS, checkpointControl, readPrivateConfig, rectangle, safeFailure, safeUIFlags, validAck, validateConfig } from './parcel-browser-e2e.mjs';

const config=path=>({platform_url:'http://127.0.0.1:18080',gateway_port:18081,username:'fixture_actor',password:'synthetic-only-not-real-password',workspace_id:'workspace_a',foreign_workspace_id:'workspace_b',second_workspace_id:'workspace_c',farm_id:'farm_'+'a'.repeat(32),report_path:join(path,'report.json')});
test('strict private config separates scopes, ports and real farm identifiers',()=>{
 const path='/tmp/owned/config.json',value=config('/tmp/owned');assert.equal(validateConfig(value,path),value);
 for(const change of [{tenant_id:'forged'},{platform_url:'http://localhost:18080'},{gateway_port:18080},{second_workspace_id:'workspace_a'},{farm_id:'farm_wrong'},{report_path:'/tmp/outside/report.json'},{report_path:path},{password:''}])assert.throws(()=>validateConfig({...value,...change},path));
});
test('private file denies public permissions, links, oversized configuration and nonprivate directory',()=>{
 const dir=mkdtempSync(join(tmpdir(),'parcel-config-test-'));chmodSync(dir,0o700);const file=join(dir,'input.json');
 try{writeFileSync(file,JSON.stringify(config(dir)),{mode:0o600});assert.equal(readPrivateConfig(file).workspace_id,'workspace_a');chmodSync(file,0o644);assert.throws(()=>readPrivateConfig(file));chmodSync(file,0o600);symlinkSync(file,join(dir,'symlink.json'));assert.throws(()=>readPrivateConfig(join(dir,'symlink.json')));linkSync(file,join(dir,'hard.json'));assert.throws(()=>readPrivateConfig(file));rmSync(join(dir,'hard.json'));chmodSync(dir,0o755);assert.throws(()=>readPrivateConfig(file));chmodSync(dir,0o700);writeFileSync(file,' '.repeat(16385));assert.throws(()=>readPrivateConfig(file));}finally{chmodSync(dir,0o700);rmSync(dir,{recursive:true,force:true});}
});
test('checkpoint acknowledgments reject wrong, duplicate and additional authority fields',()=>{
 assert.equal(validAck('{"event":"ack","checkpoint":"created"}','created'),true);
 for(const line of ['null','[]','not-json','{"event":"ack","checkpoint":"revised"}','{"event":"ack","checkpoint":"created","tenant":"x"}','{"event":"ack","event":"ack","checkpoint":"created"}','x'.repeat(129)])assert.equal(validAck(line,'created'),false);
});
test('checkpoint controller waits for every actual parent database barrier',async()=>{
 const input=new PassThrough(),output=new PassThrough();let raw='';output.on('data',v=>{raw+=v});const control=checkpointControl(input,output,1000);
 try{for(const checkpoint of CHECKPOINTS){let released=false;const wait=control.wait(checkpoint).then(()=>{released=true});await Promise.resolve();assert.equal(released,false);input.write(JSON.stringify({event:'ack',checkpoint})+'\n');await wait;}assert.equal(control.complete(),true);assert.deepEqual(raw.trim().split('\n').map(JSON.parse),CHECKPOINTS.map(checkpoint=>({event:'checkpoint',checkpoint})));}finally{control.close();input.destroy();output.destroy();}
});
test('missing or misordered database acknowledgments fail closed',async()=>{
 for(const mode of ['timeout','wrong','close']){const input=new PassThrough(),output=new PassThrough(),control=checkpointControl(input,output,10);try{const p=control.wait('created');if(mode==='wrong')input.write('{"event":"ack","checkpoint":"raced"}\n');if(mode==='close')input.end();await assert.rejects(p);assert.equal(control.complete(),false);await assert.rejects(control.wait('revised'));}finally{control.close();input.destroy();output.destroy();}}
});
test('diagnostics expose only closed stages, assertion names and boolean UI flags',()=>{
 assert.equal(safeFailure(Error('postgres://SECRET')), 'parcel browser fixture failed; private diagnostics withheld');
 assert.equal(safeFailure(Error('assertion failed: SECRET')), 'parcel browser fixture failed; private diagnostics withheld');
 assert.equal(safeFailure(Error('assertion failed: stale_revision_409_preserves_draft')), 'assertion failed: stale_revision_409_preserves_draft');
 assert.deepEqual(safeUIFlags({login:true,context:false,token:'SECRET',detail:'SECRET',busy:1}),{login:true,context:false});
 assert.equal(new Set(STAGES).size,STAGES.length);assert.equal(UI_FLAGS.length,9);
});
test('actual UI coverage matches exact Go checks and keeps sandbox and fieldset semantics',()=>{
 assert.equal(REQUIRED_CHECKS.length,21);assert.equal(new Set(REQUIRED_CHECKS).size,21);
 const source=readFileSync(new URL('./parcel-browser-e2e.mjs',import.meta.url),'utf8');
 const go=readFileSync(new URL('../../backend/internal/agriculture/parcel_browser_chain_test.go',import.meta.url),'utf8');
 const names=go.match(/const parcelBrowserChecks = "([^"]+)"/)[1].split(' ');assert.deepEqual(new Set(names),new Set(REQUIRED_CHECKS));
 for(const literal of ['deploy/agriculture/nginx.conf.template',"cdp.fill('#login-password'","[data-testid=parcel-confirm]","[name=parcel_geometry]","[data-testid=parcel-at-submit]","history.back()","matches(':disabled')","control.wait('revoked-verified')"])assert.ok(source.includes(literal));
 for(const forbidden of ['Network.setCookie','--no-sandbox','--disable-setuid-sandbox','startProxy'])assert.ok(!source.includes(forbidden));
});
test('synthetic parcel geometry is explicit, closed and distinct by revision',()=>{
 const a=rectangle(),b=rectangle(0.001);assert.equal(a.type,'Polygon');assert.deepEqual(a.coordinates[0][0],a.coordinates[0].at(-1));assert.notDeepEqual(a,b);assert.ok(a.coordinates.flat(2).every(Number.isFinite));
});
