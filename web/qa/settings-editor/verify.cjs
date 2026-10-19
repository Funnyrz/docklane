// Mock API only. Saves never touch a host, install tools, grant OS rights or restart services.
const {chromium}=require(process.env.DM_QA_PLAYWRIGHT||'playwright');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const base=process.env.DM_QA_URL||'http://127.0.0.1:18098';
const empty={allowed_paths:[],path_mappings:[],enable_daemon_write:false};
const capabilities={
 'docker.manage':{available:true},
 'backup.bind_mount':{available:false,reason_code:'ALLOWED_PATHS_MISSING'},
 'host.daemon_config':{available:false,reason_code:'DAEMON_WRITE_NOT_AUTHORIZED'},
 'host.service_restart':{available:false,reason_code:'DAEMON_WRITE_NOT_AUTHORIZED'},
};
(async()=>{
 const browser=await chromium.launch({channel:'chrome',headless:true});
 try{
  const context=await browser.newContext({locale:'zh-CN',reducedMotion:'reduce'});
  const page=await context.newPage(),errors=[],checks=[],writes=[];
  page.on('pageerror',e=>errors.push(e.message));
  let saved=structuredClone(empty),supported=true,failSave=false;
  const settings=()=>({version:'0.3.0',mode:supported?'binary':'docker',listen:':8080',data_dir:'/data',...empty,capability_settings:{effective:empty,saved,pending_restart:JSON.stringify(saved)!==JSON.stringify(empty),daemon_toggle_supported:supported}});
  await page.route('**/api/v1/**',async route=>{
   const req=route.request(),ep=new URL(req.url()).pathname.replace('/api/v1','');
   const send=(data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
   if(req.method()!=='GET'){
    assert.equal(ep,'/settings/capabilities');
    const body=req.postDataJSON();assert.equal(body.confirm,true);assert.equal(typeof body.password,'string');
    writes.push({endpoint:ep,method:req.method(),passwordProvided:Boolean(body.password)});
    if(failSave)return send({error:{code:'PASSWORD_CONFIRMATION_FAILED'}},403);
    saved={allowed_paths:body.allowed_paths,path_mappings:body.path_mappings,enable_daemon_write:body.enable_daemon_write};
    return send(settings());
   }
   if(ep==='/auth/status')return send({initialized:true,authenticated:true,user:{username:'admin'}});
   if(ep==='/settings')return send(settings());
   if(ep.endsWith('/capabilities'))return send({capabilities});
   if(ep==='/audit')return send([]);
   throw Error('Unexpected request '+ep);
  });
  const stable=()=>page.evaluate(()=>document.fonts.ready);
  async function noOverflow(){assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));assert(await page.locator('.capability-editor').evaluate(e=>e.scrollWidth<=e.clientWidth+1))}
  await page.goto(base+'/#settings');
  await page.getByRole('button',{name:'配置功能',exact:true}).first().waitFor();
  for(const language of ['zh-CN','en']){
   await page.locator('.language-select select').selectOption(language);
   const zh=language==='zh-CN';
   for(const viewport of [{width:1440,height:900},{width:1366,height:768},{width:1280,height:800},{width:768,height:1024},{width:390,height:844}]){
    await page.setViewportSize(viewport);
    const opener=page.getByRole('button',{name:zh?'配置功能':'Configure features',exact:true}).first();
    await opener.click();await page.locator('#allowed-directories').waitFor();await stable();
    await page.getByRole('button',{name:zh?'添加映射':'Add mapping',exact:true}).click();
    await page.locator('#mapping-host-0').fill('/srv/data/long-container-project-name');
    await page.locator('#mapping-local-0').fill('/host-data/long-container-project-name');
    await page.locator('#allowed-directories').fill('/srv/data\n/srv/backups');
    await noOverflow();
    await page.screenshot({path:path.join(__dirname,`editor-${language}-${viewport.width}.png`),animations:'disabled'});
    assert(await page.getByRole('button',{name:zh?'保存配置':'Save configuration',exact:true}).isDisabled());
    // Verify keyboard focus is trapped by the modal and cancellation has no writes.
    await page.getByRole('button',{name:zh?'取消':'Cancel',exact:true}).focus();await page.keyboard.press('Tab');
    assert(await page.evaluate(()=>Boolean(document.activeElement.closest('.capability-editor'))));
    await page.keyboard.press('Escape');await page.locator('#allowed-directories').waitFor({state:'hidden'});
    await opener.focus(); // Explicitly restore before the next independent viewport scenario.
    checks.push({language,viewport,overflow:false,saveRequiresConfirmation:true});
   }
  }
  assert.equal(writes.length,0);
  await page.locator('.language-select select').selectOption('zh-CN');await page.setViewportSize({width:1440,height:900});
  await page.getByRole('button',{name:'配置功能',exact:true}).first().click();
  await page.locator('#allowed-directories').fill('/srv/data\n/srv/backups');
  await page.getByRole('button',{name:'添加映射',exact:true}).click();
  await page.locator('#mapping-host-0').fill('/srv/data');await page.locator('#mapping-local-0').fill('/data');
  await page.getByRole('checkbox',{name:'允许修改 Docker 配置和重启服务',exact:true}).click();
  await page.locator('#settings-admin-password').fill('fixture-password-only');
  const save=page.getByRole('button',{name:'保存配置',exact:true});assert(await save.isDisabled());
  await page.getByRole('checkbox',{name:/我已检查目录范围/}).click();
  failSave=true;await save.click();await page.getByRole('alert').filter({hasText:'当前管理员密码不正确'}).waitFor();
  assert.equal(await page.locator('#settings-admin-password').inputValue(),'');
  assert.equal(await page.locator('#allowed-directories').inputValue(),'/srv/data\n/srv/backups');
  failSave=false;await page.locator('#settings-admin-password').fill('fixture-password-only');await save.click();
  await page.getByRole('status').filter({hasText:'请在合适时间重启 Docklane'}).waitFor();
  // Effective capability stays unconfigured, not cosmetically marked available.
  assert.equal(await page.locator('[id="capability-backup.bind_mount"] .tag').innerText(),'未配置');
  await page.getByRole('button',{name:'配置功能',exact:true}).first().click();
  assert.equal(await page.locator('#allowed-directories').inputValue(),'/srv/data\n/srv/backups');
  assert.equal(await page.locator('#settings-admin-password').inputValue(),'');
  assert.equal(await page.getByRole('checkbox',{name:/我已检查目录范围/}).getAttribute('aria-checked'),'false');
  await page.screenshot({path:path.join(__dirname,'pending-desktop.png'),animations:'disabled'});
  await page.getByRole('button',{name:'使用当前生效值',exact:true}).click();
  assert.equal(await page.locator('#allowed-directories').inputValue(),'');
  assert.equal(await page.locator('#mapping-host-0').count(),0);
  await page.getByRole('button',{name:'取消',exact:true}).click();
  checks.push({scenario:'password error, draft preservation, saved pending state and reset',pass:true});
  supported=false;saved=structuredClone(empty);
  await page.getByRole('button',{name:'重新检测',exact:true}).click();
  await page.getByRole('button',{name:'配置功能',exact:true}).first().click();
  assert(await page.getByRole('checkbox',{name:'允许修改 Docker 配置和重启服务',exact:true}).isDisabled());
  await page.getByText('当前系统或容器部署不支持宿主机操作授权。目录配置仍可保存。',{exact:true}).waitFor();
  await page.setViewportSize({width:390,height:844});await noOverflow();
  await page.screenshot({path:path.join(__dirname,'unsupported-mobile.png'),animations:'disabled'});
  checks.push({scenario:'container deployment cannot enable host authorization',pass:true});
  assert.deepEqual(errors,[]);assert.equal(writes.length,2);
  fs.writeFileSync(path.join(__dirname,'browser-results.json'),JSON.stringify({kind:'browser fixtures only',checks,writes,browserErrors:errors},null,2));
  console.log(JSON.stringify({checks:checks.length,writesIntercepted:writes.length,browserErrors:errors}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
