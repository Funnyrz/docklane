// Uses an existing Playwright installation; no project dependency is required.
// All API and stream traffic is fixture-controlled. No Docker operations run.
const { chromium } = require(process.env.DM_QA_PLAYWRIGHT || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const base = process.env.DM_QA_URL || 'http://127.0.0.1:18108';
const out = __dirname;
const routes = ['dashboard','containers','compose','images','networks','volumes','backups','tasks','settings'];
const headings = {
  'zh-CN': ['工作台','容器','Compose 项目','镜像与仓库','网络','存储','备份与恢复','任务中心','系统设置'],
  en: ['Overview','Containers','Compose projects','Images & registries','Networks','Storage','Backup & recovery','Task center','Settings'],
};
const date = '2026-10-16T12:30:00Z';
const container = {Id:'container-1',Name:'production-gateway',Names:['production-gateway'],Image:'nginx:1.27',State:'running',Created:1792152000,Ports:[{IP:'0.0.0.0',PublicPort:8080,PrivatePort:80,Type:'tcp'}]};
const repo = {id:'repo-1',name:'production-backups',location:'/backups/restic'};
const policy = {id:'policy-1',name:'daily-backup',repository_id:repo.id,targets:[{type:'directory',path:'/srv/data'}],consistency_mode:'file',schedule:'0 3 * * *',timezone:'Asia/Shanghai',keep_last:7,retries:0,retry_seconds:30,excludes:[],enabled:true};
const task = {id:'task-1',type:'backup',target:'/srv/data',status:'Running',progress:46,stage:'preflight',created_at:date,cancellable:true};
const project = {id:'compose-1',name:'production-stack',workdir:'/srv/projects/production',source_type:'local',revision:'rev-1',config_paths:['/srv/projects/production/compose.yaml']};
const snapshots = [{id:'verified-snapshot-123456789',time:date,verification_status:'verified'},{id:'unverified-snapshot-12345',time:date,verification_status:'unverified',verification_warning:'Raw verification warning'}];
const checks = [], errors = [], warnings = [], writes = [];
async function installFixtures(context) {
  let initialized = false, authenticated = false;
  await context.addInitScript(() => {
    window.EventSource = class extends EventTarget {
      constructor(url) { super(); this.timer=setTimeout(()=>this.onopen?.(new Event('open')),10); }
      close() {clearTimeout(this.timer);}
    };
  });
  await context.route('**/api/v1/**', async route => {
    const request=route.request(), endpoint=new URL(request.url()).pathname.replace('/api/v1','');
    const json=(data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
    if (endpoint==='/auth/status') return json({initialized,authenticated,user:authenticated?{username:'i18n-review'}:undefined,csrf_token:'fixture-token'});
    if (endpoint==='/auth/setup'||endpoint==='/auth/login') {initialized=true;authenticated=true;return json({});}
    if (endpoint==='/auth/logout') {authenticated=false;return json({});}
    if(request.method()!=='GET') {
      writes.push({endpoint,body:request.postDataJSON()});
      if(endpoint.endsWith('/preview')) return json({revision:'rev-preview',fingerprint:'fingerprint',warning:'Raw diagnostic warning',sources:['/srv/data'],stop_containers:[]});
      if(endpoint.endsWith('/diagnostics')) return json({registry:'Raw registry response'});
      return json({id:'task-new'});
    }
    if(endpoint.endsWith('/dashboard')) return json({host:{hostname:'production-host',os:'Ubuntu 24.04',architecture:'aarch64',cpus:16,load_1m:1.28,memory_total:32*1024**3,memory_available:20*1024**3,uptime:1908000,kernel:'6.8.0',docker_root_dir:'/var/lib/docker',disk:{available:190*1024**3,total:500*1024**3}},docker:{connected:true,version:{Version:'29.5.2',ApiVersion:'1.54'}},containers:{total:18,running:15},images:24,volumes:12,backup:{unprotected:4,latest:{created_at:date},coverage:{basis:'enabled_policies',containers:{protected:14,total:18},volumes:{protected:10,total:12}}}});
    if(endpoint==='/hosts/local/capabilities') return json({capabilities:{'host.daemon_config':{available:false,reason:'Raw capability detail'},'host.service_restart':{available:false,reason:'Raw capability detail'}}});
    if(endpoint==='/hosts/local/containers') return json({items:[container],total:1});
    if(endpoint==='/hosts/local/images') return json({items:[{Id:'image-1',RepoTags:['nginx:1.27'],Size:320*1024**2,RepoDigests:['nginx@sha256:1234567890']}],total:1});
    if(endpoint==='/hosts/local/networks') return json({items:[{Id:'network-1',Name:'production-network',Driver:'bridge',Scope:'local',IPAM:{Config:[{Subnet:'172.25.0.0/16',Gateway:'172.25.0.1'}]}}],total:1});
    if(endpoint==='/hosts/local/volumes') return json({items:[{Name:'production-data',Driver:'local',Mountpoint:'/var/lib/docker/volumes/production-data',Scope:'local'}],total:1});
    if(endpoint==='/hosts/local/containers/container-1') return json({...container,Mounts:[]});
    if(endpoint==='/hosts/local/mounts'||endpoint==='/registry-credentials') return json([]);
    if(endpoint==='/compose/projects') return json([project]);
    if(endpoint==='/compose/discover') return json([]);
    if(endpoint==='/compose/projects/compose-1') return json({project,files:[{path:project.config_paths[0],content:'services:\n  app:\n    image: nginx:1.27'}]});
    if(endpoint==='/backup/repositories') return json([repo]);
    if(endpoint==='/backup/policies') return json([policy]);
    if(endpoint==='/backup/schedule-instances') return json([{policy_id:policy.id,scheduled_at_utc:date,task_id:task.id}]);
    if(endpoint==='/backup/repositories/repo-1/snapshots') return json(snapshots);
    if(endpoint.endsWith('/metadata')) return json({verification_status:endpoint.includes('unverified')?'unverified':'verified',verification_warning:'Raw verification warning',sources:[{host_path:'/srv/data',snapshot_path:'data/source'}],containers:[],projects:[]});
    if(endpoint==='/tasks') return json([task]);
    if(endpoint==='/tasks/task-1') return json(task);
    if(endpoint==='/tasks/task-1/logs') return json([{created_at:date,message:'原始日志 / raw log'}]);
    if(endpoint==='/settings') return json({version:'v0.3.0',mode:'docker',listen:'127.0.0.1:8080',data_dir:'/data',allowed_paths:['/srv'],path_mappings:[]});
    if(endpoint==='/audit') return json([{actor_id:'i18n-review',action:'backup.run',resource_type:'policy',resource_id:policy.id,result:'Succeeded',created_at:date}]);
    throw Error(`Unhandled fixture: ${endpoint}`);
  });
}
async function shot(page,name) {
  await page.waitForTimeout(80);
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`Document overflow: ${name}`);
  const clipped = await page.locator('.n-button__content').evaluateAll(nodes=>nodes.filter(n=>n.scrollWidth>n.clientWidth+1).map(n=>n.textContent));
  assert.deepEqual(clipped,[],`Clipped buttons: ${name}`);
  await page.screenshot({path:path.join(out,`${name}.png`),fullPage:true});
  checks.push({scenario:name,overflow:false,clippedButtons:false});
  console.log(`checked ${name}`);
}
(async()=>{
  const browser=await chromium.launch({channel:'chrome',headless:true});
  try {
    for(const language of ['zh-CN','en']) {
      const context=await browser.newContext({locale:language==='en'?'en-GB':'zh-TW',viewport:{width:1440,height:1000},reducedMotion:'reduce'});
      await installFixtures(context);
      const page=await context.newPage();page.setDefaultTimeout(10000);
      page.on('pageerror',e=>errors.push(e.message));
      page.on('console',msg=>{if(msg.type()==='warning'&&msg.text().includes('intlify'))warnings.push(msg.text());});
      await page.goto(base);
      await page.locator('#username').waitFor();
      assert.equal(await page.locator('html').getAttribute('lang'),language);
      assert.equal(await page.locator('.language-select select').inputValue(),language);
      const setupHeading=language==='en'?'Initialize management platform':'初始化管理平台';
      assert.equal(await page.locator('h2').textContent(),setupHeading);
      for(const width of [1440,390]) {await page.setViewportSize({width,height:1000});await shot(page,`${language}-initialization-${width}`);}
      await page.locator('#username').fill('i18n-review');
      await page.locator('#password').fill('fixture-password-123');
      // Switching during initialization preserves credentials and makes no API call.
      await page.locator('.language-select select').selectOption(language==='en'?'zh-CN':'en');
      assert.equal(await page.locator('#username').inputValue(),'i18n-review');
      assert.equal(await page.locator('#password').inputValue(),'fixture-password-123');
      await page.locator('.language-select select').selectOption(language);
      await page.locator('.auth-submit').click();
      await page.locator('.shell').waitFor();
      await page.setViewportSize({width:1440,height:1000});
      await page.locator('.sidebar-user').click();
      await page.locator('#username').waitFor();
      for(const width of [1440,390]) {await page.setViewportSize({width,height:1000});await shot(page,`${language}-login-${width}`);}
      await page.locator('#username').fill('i18n-review');await page.locator('#password').fill('fixture-password-123');await page.locator('.auth-submit').click();await page.locator('.shell').waitFor();
      for(const width of [1440,390]) {
        await page.setViewportSize({width,height:1000});
        for(let i=0;i<routes.length;i++) {
          await page.goto(`${base}/#${routes[i]}`);await page.locator('.page h1').waitFor();
          await page.waitForTimeout(100);
          assert.equal(await page.locator('.page h1').textContent(),headings[language][i]);
          // No Chinese product copy in English (the fixed language name is intentionally Chinese).
          if(language==='en') {
            const text=await page.locator('.workspace').innerText();
            assert(!/[\p{Script=Han}]/u.test(text.replaceAll('简体中文','')),`Untranslated English UI: ${routes[i]}`);
          }
          await shot(page,`${language}-${routes[i]}-${width}`);
        }
      }
      // The current selection survives refresh and overrides the browser locale.
      const other=language==='en'?'zh-CN':'en';
      await page.locator('.language-select select').selectOption(other);
      await page.reload();await page.locator('.shell').waitFor();
      assert.equal(await page.locator('html').getAttribute('lang'),other);
      assert.equal(await page.evaluate(()=>localStorage.getItem('docker-manager:locale')),other);
      await page.locator('.language-select select').selectOption(language);
      // Dynamic fields and library empty states switch without losing form values.
      await page.goto(`${base}/#images`);await page.getByRole('button',{name:language==='en'?'Pull image':'拉取镜像',exact:true}).click();
      await page.locator('#field-image').fill('nginx:1.28');
      await page.locator('#field-credential_id').fill('missing-fixture-credential');
      await page.locator('.n-empty').waitFor();
      assert((await page.locator('.n-empty').innerText()).includes(language==='en'?'No Data':'无数据'),'Naive UI locale mismatch');
      await page.locator('#field-credential_id').fill('');
      await page.locator('.n-base-select-option').first().waitFor();
      const optionText=await page.locator('.n-base-select-option').first().textContent();
      assert(optionText.includes(language==='en'?'Anonymous':'匿名'));
      await page.keyboard.press('Escape');
      await shot(page,`${language}-image-form-390`);
      // Open modal intercepts clicks; switch through keyboard control on the persisted selector.
      await page.locator('.n-modal .n-button').filter({hasText:language==='en'?'Cancel':'取消'}).click();
      for(const width of [390,1440]) {
        await page.setViewportSize({width,height:1000});
        await page.goto(`${base}/#containers`);
        await page.getByRole('button',{name:language==='en'?'Delete':'删除',exact:true}).first().click();
        const confirm=page.getByRole('button',{name:language==='en'?'Confirm action':'确认执行',exact:true});
        assert(await confirm.isDisabled());
        await shot(page,`${language}-container-risk-${width}`);
        await page.getByRole('button',{name:language==='en'?'Cancel':'取消',exact:true}).click();
        await page.goto(`${base}/#compose`);
        await page.getByRole('button',{name:language==='en'?'Manage project ↗':'管理项目 ↗',exact:true}).click();
        await page.locator('.code-editor textarea').waitFor();
        await shot(page,`${language}-compose-editor-${width}`);
        await page.goto(`${base}/#tasks`);
        await page.locator('.status-select').click();
        await page.locator('.n-base-select-menu').waitFor();
        await shot(page,`${language}-task-filter-${width}`);
        await page.keyboard.press('Escape');
        await page.getByRole('button',{name:language==='en'?'View logs ↗':'查看日志 ↗',exact:true}).click();
        await page.locator('.terminal').filter({hasText:'原始日志 / raw log'}).waitFor();
        await shot(page,`${language}-task-detail-${width}`);
        await page.goto(`${base}/#backups`);
        await page.getByRole('button',{name:language==='en'?'Create policy':'创建策略',exact:true}).click();
        await page.locator('#policy-name').waitFor();
        await shot(page,`${language}-backup-policy-${width}`);
        await page.reload();
        await page.getByRole('button',{name:language==='en'?'Browse snapshots / restore ↗':'浏览快照 / 恢复 ↗',exact:true}).click();
        await page.getByRole('button',{name:language==='en'?'Inspect unverified snapshot →':'检查未验证快照 →',exact:true}).click();
        await page.locator('#restore-target').waitFor();
        assert(await page.getByRole('button',{name:language==='en'?'Check and preview affected data':'检查并预览覆盖范围',exact:true}).isDisabled());
        await shot(page,`${language}-unverified-restore-${width}`);
      }
      await context.close();
    }
    assert.deepEqual(errors,[]);assert.deepEqual(warnings,[]);
    fs.writeFileSync(path.join(out,'browser-results.json'),JSON.stringify({kind:'Vue application in Chrome; API/streams intercepted fixtures; no Docker operations',checks,browserErrors:errors,i18nWarnings:warnings,writes},null,2));
    console.log(JSON.stringify({checks:checks.length,browserErrors:errors,i18nWarnings:warnings}));
  } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
