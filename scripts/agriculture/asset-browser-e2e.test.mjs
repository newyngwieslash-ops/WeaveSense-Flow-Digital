import test from 'node:test';
import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import { chmodSync, linkSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { CHECKPOINTS, REQUIRED_CHECKS, STAGES, UI_FLAGS, checkpointControl, readPrivateConfig, safeFailure, safeUIFlags, validAck, validateConfig } from './asset-browser-e2e.mjs';

const config=path=>({platform_url:'http://127.0.0.1:18080',gateway_port:18081,username:'fixture_actor',password:'synthetic-only-not-real-password',workspace_id:'workspace_a',foreign_workspace_id:'workspace_b',second_workspace_id:'workspace_c',report_path:join(path,'report.json')});
test('strict private config separates scopes, ports and unknown authority fields',()=>{
 const path='/tmp/owned/config.json',value=config('/tmp/owned');assert.equal(validateConfig(value,path),value);
 for(const change of [{tenant_id:'forged'},{platform_url:'http://localhost:18080'},{gateway_port:18080},{second_workspace_id:'workspace_a'},{report_path:'/tmp/outside/report.json'},{report_path:path},{password:''}])assert.throws(()=>validateConfig({...value,...change},path));
});
test('private file denies public permissions, links, oversized configuration and nonprivate directory',()=>{
 const dir=mkdtempSync(join(tmpdir(),'asset-config-test-'));chmodSync(dir,0o700);const file=join(dir,'input.json');
 try{writeFileSync(file,JSON.stringify(config(dir)),{mode:0o600});assert.equal(readPrivateConfig(file).workspace_id,'workspace_a');chmodSync(file,0o644);assert.throws(()=>readPrivateConfig(file));chmodSync(file,0o600);symlinkSync(file,join(dir,'symlink.json'));assert.throws(()=>readPrivateConfig(join(dir,'symlink.json')));linkSync(file,join(dir,'hard.json'));assert.throws(()=>readPrivateConfig(file));rmSync(join(dir,'hard.json'));chmodSync(dir,0o755);assert.throws(()=>readPrivateConfig(file));chmodSync(dir,0o700);writeFileSync(file,' '.repeat(16385));assert.throws(()=>readPrivateConfig(file));}finally{chmodSync(dir,0o700);rmSync(dir,{recursive:true,force:true});}
});
test('checkpoint acknowledgments reject wrong, duplicate and additional authority fields',()=>{
 assert.equal(validAck('{"event":"ack","checkpoint":"created"}','created'),true);
 for(const line of ['null','[]','not-json','{"event":"ack","checkpoint":"versioned"}','{"event":"ack","checkpoint":"created","tenant":"x"}','{"event":"ack","event":"ack","checkpoint":"created"}','x'.repeat(129)])assert.equal(validAck(line,'created'),false);
});
test('checkpoint controller waits for every actual parent database barrier',async()=>{
 const input=new PassThrough(),output=new PassThrough();let raw='';output.on('data',v=>{raw+=v});const control=checkpointControl(input,output,1000);
 try{for(const checkpoint of CHECKPOINTS){let released=false;const wait=control.wait(checkpoint).then(()=>{released=true});await Promise.resolve();assert.equal(released,false);input.write(JSON.stringify({event:'ack',checkpoint})+'\n');await wait;}assert.equal(control.complete(),true);assert.deepEqual(raw.trim().split('\n').map(JSON.parse),CHECKPOINTS.map(checkpoint=>({event:'checkpoint',checkpoint})));}finally{control.close();input.destroy();output.destroy();}
});
test('missing or misordered database acknowledgments fail closed',async()=>{
 for(const mode of ['timeout','wrong','close']){const input=new PassThrough(),output=new PassThrough(),control=checkpointControl(input,output,10);try{const p=control.wait('created');if(mode==='wrong')input.write('{"event":"ack","checkpoint":"regranted"}\n');if(mode==='close')input.end();await assert.rejects(p);assert.equal(control.complete(),false);await assert.rejects(control.wait('versioned'));}finally{control.close();input.destroy();output.destroy();}}
});
test('diagnostics expose only closed stages, assertion names and boolean UI flags',()=>{
 assert.equal(safeFailure(Error('postgres://SECRET')), 'asset browser fixture failed; private diagnostics withheld');
 assert.equal(safeFailure(Error('assertion failed: SECRET')), 'asset browser fixture failed; private diagnostics withheld');
 assert.equal(safeFailure(Error('assertion failed: old_grant_denies_current_and_history')), 'assertion failed: old_grant_denies_current_and_history');
 assert.deepEqual(safeUIFlags({login:true,context:false,token:'SECRET',detail:'SECRET',busy:1}),{login:true,context:false});
 assert.equal(new Set(STAGES).size,STAGES.length);assert.equal(UI_FLAGS.length,9);
});
test('actual UI coverage matches exact Go checks and keeps sandbox and fieldset semantics',()=>{
 assert.equal(REQUIRED_CHECKS.length,23);assert.equal(new Set(REQUIRED_CHECKS).size,23);
 const source=readFileSync(new URL('./asset-browser-e2e.mjs',import.meta.url),'utf8');
 const go=readFileSync(new URL('../../backend/internal/agriculture/asset_browser_chain_test.go',import.meta.url),'utf8');
 const names=go.match(/const assetBrowserChecks = "([^"]+)"/)[1].split(' ');assert.deepEqual(new Set(names),new Set(REQUIRED_CHECKS));
 for(const literal of ['deploy/agriculture/nginx.conf.template',"cdp.fill('#login-password'","[data-testid=asset-confirm]","asset_history_version","[data-testid=asset-history-read]","history.back()","matches(':disabled')","control.wait('revoked-verified')"])assert.ok(source.includes(literal));
 for(const forbidden of ['Network.setCookie','--no-sandbox','--disable-setuid-sandbox','startProxy'])assert.ok(!source.includes(forbidden));
});
test('metadata acceptance cannot fake an object download or ready entity',()=>{
 const source=readFileSync(new URL('./asset-browser-e2e.mjs',import.meta.url),'utf8');
 const go=readFileSync(new URL('../../backend/internal/agriculture/asset_browser_chain_test.go',import.meta.url),'utf8');
 assert.ok(source.includes("second.data.entity===null"));assert.ok(source.includes("first.data.rights.status==='pending'"));assert.ok(!source.includes("+'/download?workspace_id='"));
 for(const literal of ['AuthorizeAssetDownload','errors.Is(e, ErrNotFound)','assetBrowserRegrant','GrantResourceBinding','historical asset overwritten','asset snapshot missing real business canary'])assert.ok(go.includes(literal));
});
