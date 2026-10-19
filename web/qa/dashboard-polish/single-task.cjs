// Reproduce the user's sparse dashboard state without connecting to a real host.
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
    const context = await browser.newContext({ locale: 'zh-CN', timezoneId: 'Asia/Shanghai', reducedMotion: 'reduce' });
    const page = await context.newPage();
    const errors = [], checks = [];
    page.on('pageerror', e => errors.push(e.message));
    await page.route('**/api/v1/**', route => {
      const endpoint = new URL(route.request().url()).pathname;
      const data = endpoint.endsWith('/auth/status') ? { initialized: true, authenticated: true, user: { username: 'admin' } } : endpoint.endsWith('/dashboard') ? dashboard : endpoint.endsWith('/tasks') ? tasks : null;
      assert(data !== null, `Unexpected request ${endpoint}`);
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(data) });
    });
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      await page.setViewportSize(viewport);
      await page.goto(base + '/#dashboard');
      await page.locator('.metric-value').first().waitFor();
      assert.deepEqual(await page.locator('.metric-value').allTextContents(), ['4', '3', '8', '0']);
      assert.equal(await page.locator('.activity-row').count(), 1);
      assert.equal(await page.locator('.host-facts .facts > div').count(), 8);
      assert(await page.getByText('暂无记录', { exact: true }).isVisible());
      await page.evaluate(() => document.fonts.ready);
      await page.waitForTimeout(250);
      assert.equal(await page.locator('.shell').count(), 1);
      const geometry = await page.evaluate(() => ({
        documentWidth: document.documentElement.scrollWidth,
        documentHeight: document.documentElement.scrollHeight,
        taskBottom: document.querySelector('.recent-tasks').getBoundingClientRect().bottom,
        fields: [...document.querySelectorAll('.host-facts .facts > div')].map(el => {
          const label = el.querySelector('dt').getBoundingClientRect(), value = el.querySelector('dd').getBoundingClientRect();
          return { labelTop: label.top, labelBottom: label.bottom, valueTop: value.top };
        }),
      }));
      assert(geometry.documentWidth <= viewport.width);
      if (viewport.width === 1440) {
        assert(geometry.documentHeight <= viewport.height);
        assert(geometry.fields.every(f => f.valueTop - f.labelBottom >= 2), 'Labels need breathing space above values');
      }
      await page.screenshot({ path: path.join(__dirname, `single-task-${viewport.width}.png`), fullPage: true, animations: 'disabled' });
      checks.push({ viewport, geometry });
    }
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(__dirname, 'single-task-results.json'), JSON.stringify({ kind: 'browser fixtures matching user screenshot; no resource actions', checks, browserErrors: errors }, null, 2));
    console.log('Single-task/zero-volume/no-verified-backup: 2 viewports passed');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
