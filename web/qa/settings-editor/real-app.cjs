// Own disposable instance only: initialize, save to its DB, restart, verify activation.
const {chromium}=require(process.env.DM_QA_PLAYWRIGHT||'playwright');
const fs=require('node:fs'),path=require('node:path'),os=require('node:os'),crypto=require('node:crypto'),assert=require('node:assert/strict');
const {spawn}=require('node:child_process');
const binary=process.env.DM_QA_BINARY||'/tmp/docklane-editor-native',base='http://127.0.0.1:18099';
(async()=>{
 const data=fs.mkdtempSync(path.join(os.tmpdir(),'docklane-editor-real-'));
 const source=path.join(data,'source'),repository=path.join(data,'repository');fs.mkdirSync(source);fs.mkdirSync(repository);
 const password=crypto.randomBytes(24).toString('base64url');
 const env={...process.env};for(const key of Object.keys(env))if(key.startsWith('DM_'))delete env[key];
 Object.assign(env,{DM_LISTEN:'127.0.0.1:18099',DM_DATA_DIR:data,DM_MODE:'binary',DM_DOCKER_ENDPOINT:'unix:///tmp/docklane-editor-no-engine.sock',DM_COMPOSE_PATH:'/tmp/docklane-editor-no-docker',DM_RESTIC_PATH:'/tmp/docklane-editor-no-restic'});
 let server,browser;
 const wait=ms=>new Promise(r=>setTimeout(r,ms));
 async function start(){
  server=spawn(binary,[],{env,stdio:'ignore'});
  for(let i=0;i<100;i++){
   if(server.exitCode!==null)throw Error('Disposable app exited '+server.exitCode);
   try{const r=await fetch(base+'/healthz');if(r.ok)return}catch{}
   await wait(100);
  }throw Error('App did not become healthy');
 }
 async function stop(){if(!server)return;const p=server;server=undefined;if(p.exitCode!==null)return;await new Promise((resolve,reject)=>{const timer=setTimeout(()=>{p.kill('SIGKILL');reject(Error('Graceful stop timed out'))},10000);p.once('exit',()=>{clearTimeout(timer);resolve()});p.kill('SIGTERM')})}
 try{
  await start();browser=await chromium.launch({channel:'chrome',headless:true});
  const context=await browser.newContext({locale:'zh-CN',viewport:{width:1440,height:900},reducedMotion:'reduce'});
  const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto(base);await page.locator('#username').fill('settings-review');await page.locator('#password').fill(password);
  await page.getByRole('button',{name:'创建管理员并进入',exact:true}).click();await page.locator('.shell').waitFor();
  await page.goto(base+'/#settings');await page.getByRole('button',{name:'配置功能',exact:true}).first().click();
  await page.locator('#allowed-directories').fill(source+'\n'+repository);
  await page.locator('#settings-admin-password').fill(password);
  await page.getByRole('checkbox',{name:/我已检查目录范围/}).click();
  await page.getByRole('button',{name:'保存配置',exact:true}).click();
  await page.getByRole('status').filter({hasText:'请在合适时间重启 Docklane'}).waitFor();
  const pending=await (await page.request.get(base+'/api/v1/settings')).json();
  assert.equal(pending.capability_settings.pending_restart,true);
  assert.deepEqual(pending.capability_settings.effective.allowed_paths,[]);
  assert.deepEqual(pending.capability_settings.saved.allowed_paths,[source,repository]);
  assert.equal(await page.locator('[id="capability-backup.bind_mount"] .tag').innerText(),'未配置');
  await page.screenshot({path:path.join(__dirname,'real-pending.png'),fullPage:true});
  // Restart only this owned Docklane process. No Engine or host services are running here.
  await stop();await start();
  const activated=await (await page.request.get(base+'/api/v1/settings')).json();
  assert.equal(activated.capability_settings.pending_restart,false);
  assert.deepEqual(activated.capability_settings.effective.allowed_paths,[source,repository]);
  assert.deepEqual(activated.allowed_paths,[source,repository]);
  assert.equal(activated.capabilities['backup.bind_mount'].available,true);
  await page.reload();await page.locator('[id="capability-backup.bind_mount"] .tag').filter({hasText:'可用'}).waitFor();
  await page.screenshot({path:path.join(__dirname,'real-active.png'),fullPage:true});
  // Revoke with an empty list through the same real UI and activate it by restart.
  await page.getByRole('button',{name:'配置功能',exact:true}).first().click();await page.locator('#allowed-directories').fill('');
  await page.locator('#settings-admin-password').fill(password);await page.getByRole('checkbox',{name:/我已检查目录范围/}).click();await page.getByRole('button',{name:'保存配置',exact:true}).click();
  await page.getByRole('status').filter({hasText:'请在合适时间重启 Docklane'}).waitFor();
  await stop();await start();
  const revoked=await (await page.request.get(base+'/api/v1/settings')).json();
  assert.equal(revoked.capability_settings.pending_restart,false);assert.deepEqual(revoked.allowed_paths,[]);
  assert.equal(revoked.capabilities['backup.bind_mount'].reason_code,'ALLOWED_PATHS_MISSING');
  const audit=await (await page.request.get(base+'/api/v1/audit')).json();
  assert.equal(audit.filter(e=>e.action==='settings.capabilities.update').length,2);
  assert(!JSON.stringify(audit).includes(password));assert(!JSON.stringify(activated).includes(password));assert.deepEqual(errors,[]);
  fs.writeFileSync(path.join(__dirname,'real-app-results.json'),JSON.stringify({kind:'actual embedded Go app, disposable DB, unavailable Engine socket',savedPending:'pass',activeAfterDocklaneRestart:'pass',revokedAfterRestart:'pass',auditEntries:2,passwordAbsentFromSettingsAndAudit:true,operatingSystem:process.platform,hostAuthorizationToggle:activated.capability_settings.daemon_toggle_supported,browserErrors:errors,assets:await page.locator('script[src]').evaluateAll(ns=>ns.map(n=>n.getAttribute('src')))},null,2));
  console.log('Real UI save, persistence across restart, activation, revocation and audit passed');
 }finally{
  if(browser)await browser.close();await stop();fs.rmSync(data,{recursive:true,force:true});
 }
})().catch(e=>{console.error(e);process.exitCode=1});
