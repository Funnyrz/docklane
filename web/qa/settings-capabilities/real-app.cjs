// Run only against an owned disposable application/database and unavailable Engine socket.
const {chromium}=require(process.env.DM_QA_PLAYWRIGHT||'playwright');
const crypto=require('node:crypto');
const fs=require('node:fs');
const path=require('node:path');
const assert=require('node:assert/strict');
const base=process.env.DM_QA_URL||'http://127.0.0.1:18099';
(async()=>{
 const browser=await chromium.launch({channel:'chrome',headless:true});
 try{
  const context=await browser.newContext({locale:'zh-CN',timezoneId:'Asia/Shanghai',viewport:{width:1440,height:900},reducedMotion:'reduce'});
  const page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto(base);
  await page.getByRole('heading',{name:'初始化管理平台',exact:true}).waitFor();
  await page.locator('#username').fill('capability-review');
  await page.locator('#password').fill(crypto.randomBytes(24).toString('base64url'));
  await page.getByRole('button',{name:'创建管理员并进入',exact:true}).click();
  await page.locator('.shell').waitFor();
  const response=await page.request.get(base+'/api/v1/hosts/local/capabilities');
  assert(response.ok());
  const body=await response.json();
  assert.equal(Object.keys(body.capabilities).length,6);
  for(const item of Object.values(body.capabilities)){
   assert.equal(typeof item.available,'boolean');assert.equal(typeof item.reason,'string');
   if(!item.available)assert.equal(typeof item.reason_code,'string');
  }
  assert.equal(body.capabilities['docker.manage'].reason_code,'DOCKER_UNAVAILABLE');
  assert.equal(body.capabilities['backup.bind_mount'].reason_code,'ALLOWED_PATHS_MISSING');
  await page.goto(base+'/#settings');
  await page.locator('[id="capability-docker.manage"] .tag').filter({hasText:'连接异常'}).waitFor();
  assert(await page.getByRole('button',{name:'预览配置修改',exact:true}).isDisabled());
  assert(await page.getByRole('button',{name:'应用并重启 Docker',exact:true}).isDisabled());
  await page.getByRole('button',{name:'读取配置 / 操作指引',exact:true}).click();
  await page.getByText(/^手工操作：/).waitFor();
  await page.screenshot({path:path.join(__dirname,'real-settings.png'),fullPage:true,animations:'disabled'});
  assert.deepEqual(errors,[]);
  fs.writeFileSync(path.join(__dirname,'real-app-results.json'),JSON.stringify({kind:'real embedded Go app, disposable initialized database, deliberately unavailable Docker socket',mode:body.mode,capabilities:body.capabilities,localizedSettings:'pass',hostWriteControls:'disabled',manualGuide:'localized',browserErrors:errors,assets:await page.locator('script[src]').evaluateAll(nodes=>nodes.map(n=>n.getAttribute('src')))},null,2));
  console.log('Real capability API and localized settings passed');
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
