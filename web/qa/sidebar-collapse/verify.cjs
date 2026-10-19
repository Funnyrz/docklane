// Browser-only fixtures: exercise desktop collapse and independent mobile drawer without resource actions.
const { chromium } = require(process.env.DM_QA_PLAYWRIGHT || 'playwright');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const base = process.env.DM_QA_URL || 'http://127.0.0.1:18098';
const GiB = 1024 ** 3;
const dashboard = {
  host: { hostname: 'pve-ubuntu', os: 'Ubuntu 24.04.1 LTS', architecture: 'x86_64', cpus: 4, load_1m: 1.55, memory_available: 3.6 * GiB, memory_total: 5.8 * GiB, uptime: 46 * 86400 + 12 * 3600, disk: { available: 8.5 * GiB, total: 30.3 * GiB }, kernel: '6.8.0-138-generic', docker_root_dir: '/var/lib/docker' },
  docker: { connected: true, version: { Version: '29.1.3', ApiVersion: '1.52' } },
  containers: { total: 4, running: 3 }, images: 8, volumes: 0,
  backup: { unprotected: 4, latest: null, coverage: { basis: 'enabled_policies', containers: { protected: 0, total: 4 }, volumes: { protected: 0, total: 0 } } },
};
const tasks = [{ id: 'fixture-task', type: 'mirror.diagnostic_pull', target: 'alpine', status: 'Succeeded' }];
(async () => {
  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  try {
    const context = await browser.newContext({ locale: 'zh-CN', timezoneId: 'Asia/Shanghai', reducedMotion: 'reduce', viewport: { width: 1440, height: 900 } });
    await context.addInitScript(() => { window.EventSource = class extends EventTarget { close() {} }; });
    const page = await context.newPage();
    const errors = [], checks = [];
    page.on('pageerror', e => errors.push(e.message));
    await page.route('**/api/v1/**', route => {
      assert.equal(route.request().method(), 'GET', 'No writes are needed for sidebar verification');
      const endpoint = new URL(route.request().url()).pathname;
      const data = endpoint.endsWith('/auth/status') ? { initialized: true, authenticated: true, user: { username: 'admin' } } : endpoint.endsWith('/dashboard') ? dashboard : endpoint.endsWith('/tasks') ? tasks : [];
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(data) });
    });
    const preference = 'docker-manager:sidebar-collapsed';
    const routes = ['dashboard','containers','compose','images','networks','volumes','backups','tasks','settings'];
    async function stable() { await page.evaluate(() => document.fonts.ready); await page.waitForTimeout(180); }
    async function geometry(collapsed) {
      const sizes = await page.evaluate(() => {
        const sidebar = document.querySelector('.sidebar').getBoundingClientRect();
        const workspace = document.querySelector('.workspace').getBoundingClientRect();
        return { sidebar: sidebar.width, workspaceLeft: workspace.left, documentWidth: document.documentElement.scrollWidth, viewport: innerWidth };
      });
      assert.equal(sizes.sidebar, collapsed ? 64 : 208);
      assert.equal(sizes.workspaceLeft, sizes.sidebar);
      assert(sizes.documentWidth <= sizes.viewport);
      return sizes;
    }
    await page.goto(base + '/#dashboard');
    await page.locator('.metric-card').first().waitFor();
    for (const language of ['zh-CN','en']) {
      await page.locator('.language-select select').selectOption(language);
      const toggle = page.locator('.sidebar-toggle');
      assert.equal(await toggle.getAttribute('aria-expanded'), 'true');
      await toggle.focus();
      await page.keyboard.press('Enter');
      assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
      assert.equal(await toggle.evaluate(el => el === document.activeElement), true);
      assert.equal(await page.evaluate(key => localStorage.getItem(key), preference), 'true');
      for (const viewport of [{width:1440,height:900},{width:1366,height:768},{width:1280,height:800},{width:801,height:900}]) {
        await page.setViewportSize(viewport);
        await stable();
        const sizes = await geometry(true);
        for (const [i, route] of routes.entries()) {
          const item = page.locator('.sidebar nav button').nth(i);
          assert.equal(await item.getAttribute('title'), await item.getAttribute('aria-label'));
          assert(await item.getAttribute('aria-label'));
          await item.click();
          assert.equal(new URL(page.url()).hash, '#' + route);
          assert.equal(await item.getAttribute('aria-current'), 'page');
          assert.equal(await page.locator('.sidebar nav [aria-current="page"]').count(), 1);
          assert.equal(await item.locator('.nav-label').evaluate(el => getComputedStyle(el).display), 'none');
          const bounds = await item.boundingBox();
          assert(bounds.x >= 0 && bounds.x + bounds.width <= sizes.sidebar);
          assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
          checks.push({ language, viewport, route, collapsed: true });
        }
      }
      await page.setViewportSize({width:1440,height:900});
      await page.locator('.sidebar nav button').first().click();
      await stable();
      await page.screenshot({ path: path.join(__dirname, `collapsed-${language}.png`), animations: 'disabled' });
      await page.reload();
      await page.locator('.sidebar-collapsed').waitFor();
      await stable();
      await geometry(true);
      assert.equal(await page.locator('.sidebar-user').getAttribute('title'), language === 'en' ? 'Sign out · admin' : '退出登录 · admin');
      await page.locator('.sidebar-toggle').focus();
      await page.keyboard.press('Space');
      await stable();
      await geometry(false);
      assert.equal(await page.evaluate(key => localStorage.getItem(key), preference), 'false');
    }
    await page.locator('.language-select select').selectOption('zh-CN');
    await page.locator('.sidebar-toggle').click();
    await page.locator('.sidebar-toggle').focus();
    await page.setViewportSize({ width:800, height:900 });
    await stable();
    assert.equal(await page.locator('.sidebar-toggle').count(), 0);
    assert.equal(await page.locator('.mobile-menu').evaluate(el => el === document.activeElement), true);
    assert.equal(await page.locator('.sidebar').evaluate(el => el.inert), true);
    await page.getByRole('button', { name:'打开导航' }).click();
    assert.equal(await page.locator('.workspace').evaluate(el => el.inert), true);
    assert.equal(await page.locator('.sidebar').evaluate(el => el.inert), false);
    assert.equal(await page.locator('.sidebar').evaluate(el => el.getBoundingClientRect().width), 248);
    assert.equal(await page.locator('.sidebar nav button').first().locator('.nav-label').evaluate(el => getComputedStyle(el).display), 'block');
    await page.locator('.sidebar-user').focus();
    await page.keyboard.press('Tab');
    assert.equal(await page.locator('.sidebar-brand').evaluate(el => el === document.activeElement), true);
    await page.keyboard.press('Shift+Tab');
    assert.equal(await page.locator('.sidebar-user').evaluate(el => el === document.activeElement), true);
    await page.keyboard.press('Escape');
    await stable();
    assert.equal(await page.locator('.mobile-menu').evaluate(el => el === document.activeElement), true);
    await page.setViewportSize({width:390,height:844});
    await page.getByRole('button', { name:'打开导航' }).click();
    await stable();
    await page.screenshot({ path:path.join(__dirname,'mobile-drawer.png'), animations:'disabled' });
    await page.locator('.sidebar nav button').nth(1).click();
    assert.equal(await page.locator('.sidebar.open').count(), 0);
    assert.equal(await page.evaluate(key => localStorage.getItem(key), preference), 'true');
    await page.setViewportSize({width:1440,height:900});
    await stable();
    await geometry(true);
    assert.equal(await page.locator('.sidebar-toggle').evaluate(el => el === document.activeElement), true);
    await page.setViewportSize({width:1280,height:400});
    const logout = await page.locator('.sidebar-user').boundingBox();
    assert(logout.y >= 0 && logout.y + logout.height <= 400);
    await page.setViewportSize({width:1440,height:900});
    await page.locator('.sidebar-toggle').click();
    await page.reload();
    await stable();
    await geometry(false);
    // Browser storage failures must not prevent toggling within the current session.
    await context.addInitScript(() => {
      const get = Storage.prototype.getItem, set = Storage.prototype.setItem;
      Storage.prototype.getItem = function (key) {
        if (key === 'docker-manager:sidebar-collapsed') throw new DOMException('denied','SecurityError');
        return get.call(this, key);
      };
      Storage.prototype.setItem = function (key, value) {
        if (key === 'docker-manager:sidebar-collapsed') throw new DOMException('full','QuotaExceededError');
        return set.call(this, key, value);
      };
    });
    await page.reload();
    await page.locator('.shell').waitFor();
    await page.locator('.sidebar-toggle').click();
    await stable();
    await geometry(true);
    await page.locator('.sidebar-toggle').click();
    await stable();
    await geometry(false);
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(__dirname,'collapse-results.json'),JSON.stringify({checks,keyboard:'Enter/Space/focus retained passed',persistence:'reload/navigation and breakpoint return passed',mobile:'independent full labels, inert, focus loop, Escape and route close passed',storageDenied:'pass',shortScreen:'logout visible',browserErrors:errors},null,2));
    console.log(JSON.stringify({collapsedRouteChecks:checks.length,keyboard:'pass',persistence:'pass',mobile:'pass',storageDenied:'pass',browserErrors:errors}));
  } finally { await browser.close(); }
})().catch(e=>{console.error(e);process.exitCode=1});
