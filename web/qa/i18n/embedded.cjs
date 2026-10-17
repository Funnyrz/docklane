// Requires a disposable Go app/data directory with an unavailable Docker endpoint.
// Creates only a temporary administrator; never performs a Docker operation.
const {chromium}=require(process.env.DM_QA_PLAYWRIGHT||'playwright');
const assert=require('node:assert/strict');
const crypto=require('node:crypto');
const fs=require('node:fs');
const path=require('node:path');
const base=process.env.DM_QA_URL||'http://127.0.0.1:18109';
(async()=>{
  const browser=await chromium.launch({channel:'chrome',headless:true});
  const password=crypto.randomBytes(24).toString('base64url');
  const errors=[],checks=[];
  try{
    for(const language of ['zh-CN','en']){
      const context=await browser.newContext({locale:language==='en'?'en-GB':'zh-HK',viewport:{width:language==='en'?1440:390,height:1000}});
      const page=await context.newPage();page.setDefaultTimeout(10000);
      page.on('pageerror',e=>errors.push(e.message));
      await page.goto(base);await page.locator('#username').waitFor();
      assert.equal(await page.locator('html').getAttribute('lang'),language);
      assert.equal(await page.locator('h2').textContent(),language==='en'?'Welcome back':'初始化管理平台');
      await page.locator('#username').fill('i18n-temporary-review');
      if(language==='en'){
        await page.locator('#password').fill('invalid-fixture-password-123');await page.locator('.auth-submit').click();
        await page.getByRole('alert').waitFor();
        assert((await page.getByRole('alert').innerText()).includes('Incorrect username or password'));
        checks.push('English authentication error localization');
      }
      await page.locator('#password').fill(password);await page.locator('.auth-submit').click();await page.locator('.shell').waitFor();
      await page.locator('.host-copy .danger').waitFor();
      assert.equal(await page.locator('.metric-card').count(),0);
      assert.equal(await page.locator('.page h1').textContent(),language==='en'?'Overview':'工作台');
      await page.locator('.language-select select').selectOption(language==='en'?'zh-CN':'en');
      await page.reload();await page.locator('.shell').waitFor();
      assert.equal(await page.locator('html').getAttribute('lang'),language==='en'?'zh-CN':'en');
      await page.locator('.language-select select').selectOption(language);
      await page.screenshot({path:path.join(__dirname,`${language}-embedded-dashboard.png`),fullPage:true});
      checks.push(`${language}: real authentication, embedded assets, unavailable Engine, preference persistence`);
      await page.setViewportSize({width:1440,height:1000});await page.locator('.sidebar-user').click();await page.locator('#username').waitFor();
      await context.close();
    }
    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(__dirname,'embedded-results.json'),JSON.stringify({kind:'Real Go application embedding production build; isolated temporary database; missing Docker socket',checks,browserErrors:errors},null,2));
    console.log(JSON.stringify({checks:checks.length,browserErrors:errors}));
  }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
