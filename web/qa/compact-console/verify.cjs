// Run against a disposable app/database with an unavailable Docker endpoint.
// DM_QA_PLAYWRIGHT points to an existing Playwright installation; no project dependency is added.
const { chromium } = require(process.env.DM_QA_PLAYWRIGHT || 'playwright');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');
const base = process.env.DM_QA_URL || 'http://127.0.0.1:18099';
const out = __dirname;
const reuse = process.env.DM_QA_REUSE_SESSION === '1';
const widths = [390, 768, 1280, 1440];
const routes = ['dashboard', 'containers', 'compose', 'images', 'networks', 'volumes', 'backups', 'tasks', 'settings'];
(async () => {
  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  try {
    const context = await browser.newContext({ ...(reuse ? { storageState: '/tmp/docklane-compact-browser-state.json' } : {}), locale: 'zh-CN', timezoneId: 'Asia/Shanghai', viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    if (!reuse) {
    await page.goto(base);
    const setup = await page.getByRole('heading', { name: '初始化管理平台' }).isVisible();
    assert(setup, 'Use a fresh disposable database for initialization coverage');
    for (const width of widths) {
      await page.setViewportSize({ width, height: 900 });
      await page.waitForTimeout(200);
      await page.screenshot({ path: path.join(out, `initialization-${width}.png`), fullPage: true });
    }
    const username = 'visual-review';
    const password = crypto.randomBytes(24).toString('base64url');
    await page.locator('#username').fill(username);
    await page.locator('#password').fill(password);
    await page.getByRole('button', { name: '创建管理员并进入' }).click();
    await page.locator('.shell').waitFor();
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.locator('.sidebar-user').click();
    await page.getByRole('heading', { name: '欢迎回来' }).waitFor();
    for (const width of widths) {
      await page.setViewportSize({ width, height: 900 });
      await page.waitForTimeout(200);
      await page.screenshot({ path: path.join(out, `login-${width}.png`), fullPage: true });
    }
    await page.locator('#username').fill(username);
    await page.locator('#password').fill(password);
    await page.getByRole('button', { name: '登录控制台' }).click();
    await page.locator('.shell').waitFor();
    }
    const results = [];
    for (const width of widths) {
      await page.setViewportSize({ width, height: 900 });
      await page.waitForTimeout(200);
      for (const route of routes) {
        await page.goto(`${base}/#${route}`);
        await page.locator('.page h1').waitFor();
        await page.waitForTimeout(180);
        const sizes = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth }));
        assert(sizes.document <= width, `Document overflow at ${route}/${width}: ${sizes.document}`);
        if (route === 'dashboard') {
          await page.getByText('连接异常', { exact: true }).waitFor();
          assert.equal(await page.locator('.metrics').count(), 0, 'Disconnected metrics must be hidden');
        }
        await page.screenshot({ path: path.join(out, `${route}-${width}.png`), fullPage: true });
        results.push({ route, width, overflow: false });
      }
    }
    // Mobile navigation must open, close after routing, and support Escape.
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole('button', { name: '打开导航' }).click();
    assert.equal(await page.getByRole('button', { name: '关闭导航' }).getAttribute('aria-expanded'), 'true');
    await page.keyboard.press('Escape');
    assert.equal(await page.getByRole('button', { name: '打开导航' }).getAttribute('aria-expanded'), 'false');
    await page.getByRole('button', { name: '打开导航' }).click();
    await page.getByRole('navigation').getByRole('button', { name: '容器', exact: true }).click();
    assert.equal(await page.locator('.sidebar.open').count(), 0);
    // Short desktop screen: exit stays visible while only navigation scrolls.
    await page.setViewportSize({ width: 1280, height: 400 });
    const exit = await page.locator('.sidebar-user').boundingBox();
    assert(exit.y >= 0 && exit.y + exit.height <= 400);
    const scrolls = await page.locator('.sidebar nav').evaluate(el => el.scrollHeight > el.clientHeight);
    assert(scrolls, 'Short-screen navigation should scroll independently');
    await page.screenshot({ path: path.join(out, 'short-screen-navigation.png') });
    fs.writeFileSync('/tmp/docklane-compact-browser-state.json', JSON.stringify(await context.storageState()), { mode: 0o600 });
    fs.writeFileSync(path.join(out, 'browser-results.json'), JSON.stringify({ kind: 'real embedded Go application, disposable database, deliberately unavailable Engine', results, mobileNavigation: 'pass', shortScreenNavigation: 'pass', browserErrors: errors, assets: await page.locator('script[src]').evaluateAll(nodes => nodes.map(n => n.getAttribute('src'))) }, null, 2));
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ screenshots: results.length + (reuse ? 1 : 9), overflowChecks: results.length, mobileNavigation: 'pass', shortScreenNavigation: 'pass', browserErrors: errors }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
