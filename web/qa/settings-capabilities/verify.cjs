// Browser fixtures only: no tools installed and no host authorization or restart performed.
const { chromium } = require(process.env.DM_QA_PLAYWRIGHT || 'playwright');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const base = process.env.DM_QA_URL || 'http://127.0.0.1:18098';
const mixed = {
  'docker.manage': {available:true,reason:''},
  'compose.manage': {available:false,reason:'Docker Compose v2 plugin is unavailable',reason_code:'COMPOSE_V2_UNAVAILABLE'},
  'backup.local': {available:false,reason:'Restic is not installed',reason_code:'RESTIC_MISSING'},
  'backup.bind_mount': {available:false,reason:'Configure explicit allowed paths and container path mappings',reason_code:'ALLOWED_PATHS_MISSING'},
  'host.daemon_config': {available:false,reason:'daemon changes require binary mode and explicit enable_daemon_write authorization',reason_code:'DAEMON_WRITE_NOT_AUTHORIZED'},
  'host.service_restart': {available:false,reason:'daemon changes require binary mode and explicit enable_daemon_write authorization',reason_code:'DAEMON_WRITE_NOT_AUTHORIZED'},
};
(async()=>{
  const browser=await chromium.launch({channel:'chrome',headless:true});
  try {
    const context=await browser.newContext({locale:'zh-CN',timezoneId:'Asia/Shanghai',reducedMotion:'reduce',viewport:{width:1440,height:900}});
    const page=await context.newPage();
    const errors=[],checks=[],writes=[];
    page.on('pageerror',e=>errors.push(e.message));
    let caps=structuredClone(mixed),mode='binary';
    await page.route('**/api/v1/**',r=>{
      const request=r.request(), endpoint=new URL(request.url()).pathname.replace('/api/v1','');
      const json=data=>r.fulfill({contentType:'application/json',body:JSON.stringify(data)});
      if(request.method()!=='GET'){
        writes.push({endpoint,method:request.method()});
        assert.equal(endpoint,'/host/daemon/preview','No apply/install/authorization requests are allowed');
        return json({revision:'fixture',configuration:{'registry-mirrors':['https://registry.example.com']}});
      }
      if(endpoint==='/auth/status')return json({initialized:true,authenticated:true,user:{username:'admin'}});
      if(endpoint==='/settings')return json({version:'0.3.0',mode,listen:':8080',data_dir:'/home/ubuntu/docklane/data',allowed_paths:null,path_mappings:null});
      if(endpoint.endsWith('/capabilities'))return json({mode,capabilities:caps});
      if(endpoint==='/audit')return json([]);
      if(endpoint==='/host/daemon')return json({available:false,reason:'raw backend diagnostic',manual_guide:'Raw English manual guide'});
      throw Error('Unexpected endpoint '+endpoint);
    });
    const card=key=>page.locator(`[id="capability-${key}"]`);
    async function stable(){await page.evaluate(()=>document.fonts.ready);await page.waitForTimeout(150)}
    async function noOverflow(){assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))}
    await page.goto(base+'/#settings');
    await card('backup.local').waitFor();
    for(const language of ['zh-CN','en']){
      await page.locator('.language-select select').selectOption(language);
      for(const viewport of [{width:1440,height:900},{width:1280,height:800},{width:768,height:1024},{width:390,height:844}]){
        await page.setViewportSize(viewport);await stable();await noOverflow();
        assert.equal(await page.locator('.capability').count(),6);
        assert.equal(await card('backup.local').locator('.tag').innerText(),language==='zh-CN'?'缺少工具':'Missing tool');
        assert.equal(await card('backup.bind_mount').locator('.tag').innerText(),language==='zh-CN'?'未配置':'Not configured');
        assert.equal(await card('host.daemon_config').locator('.tag').innerText(),language==='zh-CN'?'未授权':'Not authorized');
        const preview=page.getByRole('button',{name:language==='zh-CN'?'预览配置修改':'Preview configuration changes',exact:true});
        assert(await preview.isDisabled());
        assert(!await page.getByText('Restic is not installed',{exact:true}).isVisible());
        if(viewport.width===1440||viewport.width===390)await page.screenshot({path:path.join(__dirname,`settings-${language}-${viewport.width}.png`),fullPage:true,animations:'disabled'});
        checks.push({scenario:'mixed',language,viewport});
      }
    }
    await page.setViewportSize({width:1440,height:900});
    await page.locator('.language-select select').selectOption('zh-CN');
    const help=card('backup.local').locator('.capability-help');
    await help.locator(':scope > summary').click();
    assert.equal(await help.getAttribute('open'),'');
    assert(await help.getByText(/DM_RESTIC_PATH/).isVisible());
    assert.equal(writes.length,0);
    await page.locator('.capabilities-panel').screenshot({path:path.join(__dirname,'resolution-desktop.png'),animations:'disabled'});
    await help.locator('.capability-technical > summary').click();
    assert(await page.getByText('Restic is not installed',{exact:true}).isVisible());
    await page.setViewportSize({width:390,height:844});await noOverflow();
    await page.locator('.capabilities-panel').screenshot({path:path.join(__dirname,'resolution-mobile.png'),animations:'disabled'});
    const codes=['DOCKER_UNAVAILABLE','DOCKER_CLI_MISSING','COMPOSE_V2_UNAVAILABLE','RESTIC_MISSING','ALLOWED_PATHS_MISSING','PATH_MAPPINGS_MISSING','HOST_OS_UNSUPPORTED','HOST_MODE_UNSUPPORTED','DAEMON_WRITE_NOT_AUTHORIZED','LOCAL_DOCKER_ENDPOINT_REQUIRED','DAEMON_CONFIG_PATH_INVALID','DAEMON_CONFIG_NOT_REGULAR','DAEMON_CONFIG_INSPECTION_FAILED','DAEMON_CONFIG_DIRECTORY_MISSING','HOST_TOOL_MISSING','HOST_PERMISSION_DENIED','DAEMON_CONFIG_NOT_WRITABLE'];
    for(const reason_code of codes){
      caps={'host.daemon_config':{available:false,reason_code,reason:'Raw diagnostic payload',reason_params:{tool:'systemctl',path:'/etc/docker/daemon.json'}}};
      await page.getByRole('button',{name:'重新检测',exact:true}).click();
      await card('host.daemon_config').locator('.capability-reason').waitFor();
      await stable();await noOverflow();
      const item=card('host.daemon_config');
      assert(!(await item.locator('.capability-reason').innerText()).includes('Raw diagnostic payload'));
      assert((await item.locator('.tag').innerText())!=='受限');
      const disclosure=item.locator('.capability-help');
      if(await disclosure.getAttribute('open')===null)await disclosure.locator(':scope > summary').click();
      assert((await disclosure.locator(':scope > p').innerText()).length>10);
      checks.push({scenario:reason_code,viewport:{width:390,height:844}});
    }
    mode='docker';
    caps={'backup.bind_mount':{available:false,reason_code:'PATH_MAPPINGS_MISSING'},'host.daemon_config':{available:false,reason_code:'HOST_MODE_UNSUPPORTED'}};
    await page.getByRole('button',{name:'重新检测',exact:true}).click();await stable();
    assert.equal(await card('host.daemon_config').locator('.tag').innerText(),'环境不支持');
    assert((await page.locator('.settings-paths').innerText()).includes('路径映射'));
    await page.getByRole('button',{name:'读取配置 / 操作指引',exact:true}).click();await stable();
    assert(await page.getByText(/^手工操作：/).isVisible());
    assert(!await page.getByText('Raw English manual guide',{exact:true}).isVisible());
    checks.push({scenario:'container mode and localized manual guide'});
    caps={'host.daemon_config':{available:false,reason_code:'FUTURE_CODE',reason:'<img src=x onerror="alert(1)">'}};
    await page.getByRole('button',{name:'重新检测',exact:true}).click();await stable();
    assert.equal(await card('host.daemon_config').locator('.tag').innerText(),'需要排查');
    const tech=card('host.daemon_config').locator('.capability-technical');
    if(await card('host.daemon_config').locator('.capability-help').getAttribute('open')===null)await card('host.daemon_config').locator('.capability-help > summary').click();
    if(await tech.getAttribute('open')===null)await tech.locator('summary').click();
    assert.equal(await tech.locator('img').count(),0);
    assert((await tech.innerText()).includes('<img src=x'));
    checks.push({scenario:'unknown code safe raw diagnostic'});
    caps={'host.daemon_config':{available:true},'host.service_restart':{available:false,reason_code:'HOST_TOOL_MISSING',reason_params:{tool:'systemctl'}}};
    await page.getByRole('button',{name:'重新检测',exact:true}).click();await stable();
    assert(await page.getByRole('button',{name:'预览配置修改',exact:true}).isDisabled());
    checks.push({scenario:'both backend permissions required'});
    caps={'host.daemon_config':{available:true},'host.service_restart':{available:true}};
    await page.getByRole('button',{name:'重新检测',exact:true}).click();await stable();
    assert(await page.getByRole('button',{name:'应用并重启 Docker',exact:true}).isDisabled());
    await page.getByRole('button',{name:'预览配置修改',exact:true}).click();await stable();
    await page.getByRole('button',{name:'应用并重启 Docker',exact:true}).click();
    const confirm=page.getByRole('button',{name:'确认执行',exact:true});
    await confirm.waitFor();assert(await confirm.isDisabled());
    assert.equal(await page.getByRole('checkbox').isChecked(),false);
    await page.getByRole('button',{name:'取消',exact:true}).click();
    checks.push({scenario:'preview and explicit confirmation preserved'});
    assert.deepEqual(errors,[]);
    assert.equal(writes.length,1);
    fs.writeFileSync(path.join(__dirname,'browser-results.json'),JSON.stringify({checks,writes,confirmationGate:'pass',browserErrors:errors},null,2));
    console.log(JSON.stringify({checks:checks.length,writesIntercepted:writes.length,browserErrors:errors}));
  }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
