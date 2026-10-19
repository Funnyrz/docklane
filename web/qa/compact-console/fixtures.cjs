// Browser-only visual fixtures. Every fixture API request is intercepted; no Docker operation runs.
const { chromium } = require(process.env.DM_QA_PLAYWRIGHT || 'playwright');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const base = process.env.DM_QA_URL || 'http://127.0.0.1:18098';
const out = process.env.DM_QA_OUTPUT_DIR || __dirname;
fs.mkdirSync(out, { recursive: true });
const date = '2026-10-19T01:30:00Z';
const viewports = [{ width: 1440, height: 900 }, { width: 1366, height: 768 }, { width: 1280, height: 800 }, { width: 768, height: 1024 }, { width: 390, height: 844 }];
const longPath = '/srv/docker-manager/production-services/application-data/postgresql/primary-database';
const container = { Id: 'ui-visual-container-long-identifier-01', Names: ['production-api-gateway-with-a-long-resource-name'], Name: 'production-api-gateway-with-a-long-resource-name', Image: 'registry.example.com/platform/api-gateway:2026.10', State: 'running', Created: 1792152000, Ports: [{ IP: '0.0.0.0', PublicPort: 8080, PrivatePort: 80, Type: 'tcp' }], Mounts: [{ Type: 'volume', Name: 'production-data', Source: longPath, Destination: '/var/lib/app' }] };
const containers = [container, ...['paused', 'exited', 'created'].map((State, index) => ({ ...container, Id: `ui-visual-${State}`, Name: `worker-${State}`, Names: [`worker-${State}`], State, Ports: [] }))];
const images = [{ Id: 'sha256:visual-image', RepoTags: ['registry.example.com/platform/api-gateway:2026.10'], Size: 320 * 1024 ** 2, RepoDigests: ['registry.example.com/platform/api-gateway@sha256:0123456789abcdef0123456789abcdef'] }];
const volumes = [{ Name: 'production-data', Driver: 'local', Mountpoint: longPath, Scope: 'local' }];
const networks = [{ Id: 'ui-visual-network', Name: 'production-network', Driver: 'bridge', Scope: 'local', IPAM: { Config: [{ Subnet: '172.25.0.0/16', Gateway: '172.25.0.1' }] } }];
const tasks = ['Running', 'Pending', 'Succeeded', 'Failed', 'Canceled'].map((status, index) => ({ id: `ui-task-${index}`, type: ['backup.run', 'image.pull', 'compose.deploy', 'backup.verify', 'storage.scan'][index], target: index === 0 ? longPath : 'production-service', status, progress: index === 0 ? 46 : 100, stage: index === 0 ? '加密上传' : '完成', created_at: date, cancellable: index === 0, error_code: index === 3 ? 'READ_FAILED' : undefined, error_message: index === 3 ? '无法读取指定文件，请检查权限。' : undefined }));
const repo = { id: 'repo-visual', name: '生产环境加密备份', location: '/backups/restic/production-services/encrypted-repository' };
const policy = { id: 'policy-visual', name: '生产数据每日保护', repository_id: repo.id, targets: [{ type: 'directory', path: longPath }], consistency_mode: 'file', schedule: '0 3 * * *', timezone: 'Asia/Shanghai', keep_last: 7, retries: 1, retry_seconds: 30, excludes: [], enabled: true };
const project = { id: 'compose-visual', name: 'production-platform', workdir: '/srv/projects/production-platform', source_type: 'import', revision: 'visual-revision', config_paths: ['/srv/projects/production-platform/compose.yaml'] };
const snapshots = [{ id: 'verified-snapshot-0123456789abcdef', time: date, verification_status: 'verified' }, { id: 'unverified-snapshot-0123456789abcdef', time: date, verification_status: 'unverified', verification_warning: '此快照未完成完整校验，可能缺少文件。仅可救援到新目录。' }];
(async () => {
  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  try {
    const context = await browser.newContext({ locale: 'zh-CN', timezoneId: 'Asia/Shanghai', viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' });
    // Stream text is a visual fixture; actual SSE/TTY behavior is covered by historical release evidence.
    await context.addInitScript(() => {
      window.EventSource = class extends EventTarget {
        constructor(url) { super(); this.timer = setTimeout(() => { this.onopen?.(new Event('open')); if (String(url).includes('/logs')) this.dispatchEvent(new MessageEvent('log', { data: JSON.stringify({ text: '2026-10-16T12:30:00Z INFO application ready on :8080\n2026-10-16T12:30:02Z INFO request completed status=200 duration=12ms' }) })); }, 30); }
        close() { clearTimeout(this.timer); }
      };
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    let navigation = 0;
    let viewportHeight = 900;
    const errors = [], writes = [], checks = [];
    page.on('pageerror', error => errors.push(error.message));
    let offline = false, delayed = false, scenario = 'normal';
    await page.route('**/api/v1/**', async route => {
      const request = route.request(), url = new URL(request.url()), endpoint = url.pathname.replace('/api/v1', '');
      if (endpoint === '/auth/status') return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ initialized: true, authenticated: true, user: { username: 'visual-review' } }) });
      const json = (data, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) });
      if (request.method() !== 'GET') {
        writes.push({ endpoint, method: request.method(), intercepted: true });
        if (endpoint === '/backup/restore/preview') return json({ revision: 'visual-preview', target: longPath, warning: '仅恢复文件到新目录，不启动服务。' });
        if (endpoint.endsWith('/config/preview')) return json({ fingerprint: 'visual-fingerprint', changes: { image: 'nginx:1.27' }, requires_rebuild: true });
        if (endpoint.endsWith('/preview')) return json({ revision: 'visual-revision', sources: [longPath], stop_containers: [], warning: '文件级模式不保证数据库一致性。' });
        return json({ error: { code: 'VISUAL_ONLY', message: '视觉验收：模拟 API 错误；未执行任何 Docker 操作。' } }, 422);
      }
      if (delayed && endpoint.endsWith('/dashboard')) await new Promise(resolve => setTimeout(resolve, 700));
      if (endpoint.endsWith('/dashboard')) { const payload = { host: { hostname: 'production-docker-node-01', os: 'Ubuntu 24.04.4 LTS', architecture: 'aarch64', cpus: 16, load_1m: 1.28, memory_total: 32 * 1024 ** 3, memory_available: 20 * 1024 ** 3, uptime: 1908000, kernel: '6.8.0-117-generic', docker_root_dir: '/var/lib/docker', disk: { available: 190 * 1024 ** 3, total: 500 * 1024 ** 3 } }, docker: { connected: !offline, error: offline ? '视觉验收：Docker Engine 连接中断' : '', version: { Version: '29.5.2', ApiVersion: '1.54' } }, containers: { total: 18, running: 15 }, images: 24, volumes: 12, backup: { unprotected: 4, latest: { created_at: date }, coverage: { basis: 'enabled_policies', containers: { protected: 14, total: 18 }, volumes: { protected: 10, total: 12 } } } };
        if (scenario === 'unknown') { payload.host = { hostname: 'unknown-metrics' }; payload.containers = null; payload.images = null; payload.volumes = null; payload.backup = {}; }
        if (scenario === 'long') { payload.host.hostname = 'production-' + 'long-hostname-'.repeat(12); payload.host.docker_root_dir = '/srv/' + 'docker-platform-data/'.repeat(14); payload.host.kernel = 'kernel-' + 'release-'.repeat(12); payload.resource_error = 'Resource unavailable: ' + 'permission denied for requested path; '.repeat(12); }
        return json(payload);
      }
      for (const [kind, data] of Object.entries({ containers, images, networks, volumes })) {
        if (endpoint === `/hosts/local/${kind}`) return json({ items: data, total: data.length });
        if (endpoint.startsWith(`/hosts/local/${kind}/`)) {
          if (kind === 'containers') return json(container);
          return json({ [kind === 'images' ? 'image' : 'volume']: data[0], containers: [container] });
        }
      }
      if (endpoint === '/registry-credentials') return json([{ id: 'credential-visual', name: '私有镜像仓库', server: 'registry.example.com', username: 'service-account' }]);
      if (endpoint === '/hosts/local/mounts') return json([{ container_id: container.Id, names: container.Names, mount: container.Mounts[0] }]);
      if (endpoint === '/tasks') return json(scenario === 'unknown' ? [] : tasks);
      if (endpoint.endsWith('/logs')) return json([{ created_at: date, message: '视觉验收示例日志：任务正在处理数据。' }]);
      if (endpoint.startsWith('/tasks/')) return json(tasks[0]);
      if (endpoint === '/compose/projects') return json([project]);
      if (endpoint === '/compose/discover') return json([]);
      if (endpoint.startsWith('/compose/projects/')) return json({ project, files: [{ path: project.config_paths[0], content: 'services:\n  gateway:\n    image: nginx:1.27\n    ports:\n      - "8080:80"\n    volumes:\n      - application-data:/var/lib/app\nvolumes:\n  application-data:\n' }] });
      if (endpoint === '/backup/repositories') return json([repo]);
      if (endpoint === '/backup/policies') return json([policy]);
      if (endpoint === '/backup/schedule-instances') return json([]);
      if (endpoint.endsWith('/metadata')) return json({ verification_status: endpoint.includes('unverified') ? 'unverified' : 'verified', verification_warning: '快照未完成验证，可能缺少文件。', sources: [{ host_path: longPath, snapshot_path: '/data/production' }], containers: [{ id: container.Id, name: container.Name }], projects: [project] });
      if (endpoint.endsWith('/snapshots')) return json(snapshots);
      if (endpoint === '/settings') return json({ version: '0.3.0', mode: 'binary', listen: '127.0.0.1:8080', data_dir: '/var/lib/docker-manager', allowed_paths: ['/srv', '/backups'], path_mappings: [] });
      if (endpoint.endsWith('/capabilities')) return json({ capabilities: { 'docker.manage': { available: true, reason: 'Docker Engine 连接正常' }, 'backup.local': { available: true, reason: 'Restic 可用' }, 'host.daemon_config': { available: false, reason: '宿主机配置修改需要显式授权。' }, 'host.service_restart': { available: false, reason: '当前模式不允许自动重启 Docker。' } } });
      if (endpoint === '/audit') return json([{ actor_id: 'visual-review', action: 'backup.run', resource_type: 'policy', resource_id: policy.id, result: 'Succeeded', created_at: date }]);
      throw new Error(`Unhandled fixture endpoint: ${endpoint}`);
    });
    async function go(route, width = 1440) {
      await page.setViewportSize({ width, height: viewportHeight });
      await page.goto(`${base}/?visual-qa=${++navigation}#${route}`);
      await page.locator('.page h1').waitFor();
      await page.waitForTimeout(180);
    }
    async function shot(name) {
      await page.waitForTimeout(180);
      const width = page.viewportSize().width;
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Overflow: ${name}/${width}`);
      await page.screenshot({ path: path.join(out, `fixture-${name}-${await page.evaluate(() => document.documentElement.lang)}-${width}x${viewportHeight}.png`), fullPage: await page.locator('.n-drawer, .n-modal').count() === 0 });
      checks.push({ scenario: name, locale: await page.evaluate(() => document.documentElement.lang), width, height: viewportHeight, overflow: false });
      console.log(`checked ${name}/${width}`);
    }
    await page.goto(base);
    for (const language of ['zh-CN', 'en']) {
      await page.locator('.language-select select').selectOption(language);
      for (const viewport of viewports) {
        const { width, height } = viewport;
        viewportHeight = height;
      for (const route of ['dashboard', 'containers', 'compose', 'images', 'networks', 'volumes', 'backups', 'tasks', 'settings']) {
        await go(route, width);
        assert.equal(await page.locator('.sidebar nav button').count(), 9);
        assert.equal(await page.locator('.sidebar nav button small').count(), 0);
        if (width > 800) {
          const navFits = await page.locator('.sidebar nav button').evaluateAll(nodes => nodes.every(el => {
            const label = el.querySelector('.nav-label'), chevron = el.querySelector('.nav-chevron');
            return label.scrollWidth <= label.clientWidth + 1 && (!chevron || label.getBoundingClientRect().right <= chevron.getBoundingClientRect().left);
          }));
          assert(navFits, `Single-line navigation clipped: ${language}/${width}/${route}`);
        }
        if (route === 'dashboard') {
          assert.equal(await page.locator('.metric-card').count(), 4);
          assert.equal(await page.locator('.host-facts .facts > div').count(), 8);
          assert.equal(await page.locator('.activity-row').count(), 5);
          if (width === 1440 && height === 900) {
            assert(await page.evaluate(() => document.documentElement.scrollHeight <= innerHeight), `First screen overflow: ${language}`);
            const taskBounds = await page.locator('.activity-row').evaluateAll(nodes => nodes.map(el => el.getBoundingClientRect().bottom));
            assert(taskBounds.every(bottom => bottom <= height), `Task below first screen: ${language}`);
          }
        }
        if (width >= 1100 && ['containers','images','networks','volumes'].includes(route)) {
          const bounds = await page.locator('.resource-table td:last-child').first().boundingBox();
          assert(bounds.x + bounds.width <= width, `Desktop actions clipped: ${route}/${width}`);
          const buttonBounds = await page.locator('.resource-table .row-actions').first().locator('button').evaluateAll(nodes => nodes.map(el => ({ left: el.getBoundingClientRect().left, right: el.getBoundingClientRect().right })));
          assert(buttonBounds.every(button => button.left >= bounds.x && button.right <= bounds.x + bounds.width), `Action buttons clipped within cell: ${route}/${width}`);
          if (route === 'containers') {
            const ports = await page.locator('.resource-table td:nth-child(4)').first().boundingBox();
            assert(ports.x + ports.width <= bounds.x + 1, `Port/action overlap: ${width}`);
          }
        }
        await shot(route);
      }
    }
    }
    await page.locator('.language-select select').selectOption('zh-CN');
    viewportHeight = 900;
    await go('dashboard', 1440);
    const geometry = await page.evaluate(() => ({
      viewport: { width: innerWidth, height: innerHeight },
      documentHeight: document.documentElement.scrollHeight,
      panels: Object.fromEntries(['.host-summary', '.metrics', '.dashboard-details', '.recent-tasks', '.page-footer'].map(selector => { const r = document.querySelector(selector).getBoundingClientRect(); return [selector, { top: r.top, bottom: r.bottom, height: r.height }]; })),
      taskBottoms: [...document.querySelectorAll('.activity-row')].map(el => el.getBoundingClientRect().bottom),
    }));
    fs.writeFileSync(path.join(out, 'first-screen.json'), JSON.stringify(geometry, null, 2));
    await page.screenshot({ path: path.join(out, 'preview.png') });
    assert.equal(geometry.taskBottoms.length, 5);
    assert(geometry.documentHeight <= 900, 'Dashboard must fit within 1440x900');
    for (const width of [390, 1440]) {
      viewportHeight = width === 390 ? 844 : 900;
      await go('containers', width);
      await page.getByRole('button', { name: '详情', exact: true }).first().click();
      await page.getByRole('button', { name: '配置', exact: true }).click();
      await page.getByRole('button', { name: '高级 JSON', exact: true }).click();
      assert((await page.getByRole('textbox', { name: '高级容器配置 JSON' }).evaluate(el => getComputedStyle(el).fontFamily)).includes('SFMono'));
      await shot('container-config');
      await page.getByRole('button', { name: '日志', exact: true }).click();
      await page.locator('.terminal').filter({ hasText: 'application ready' }).waitFor();
      await shot('container-logs');
      await page.getByRole('button', { name: '终端', exact: true }).click();
      await shot('container-terminal');
      await go('containers', width);
      await page.getByRole('button', { name: '删除', exact: true }).first().click();
      const confirm = page.getByRole('button', { name: '确认执行', exact: true });
      await confirm.waitFor();
      assert(await confirm.isDisabled());
      assert.equal(await page.getByRole('checkbox').isChecked(), false);
      await page.waitForTimeout(150);
      assert.equal(await page.getByRole('button', { name: '取消', exact: true }).evaluate(el => el === document.activeElement), true);
      await shot('container-risk-confirm');
      await page.getByRole('button', { name: '取消', exact: true }).click();
      await go('compose', width);
      await page.getByRole('button', { name: '管理项目 ↗' }).click();
      await page.locator('.code-editor textarea').waitFor();
      assert((await page.locator('.code-editor textarea').evaluate(el => getComputedStyle(el).fontFamily)).includes('SFMono'));
      await shot('compose-editor');
      await go('images', width);
      await page.getByRole('button', { name: '拉取镜像', exact: true }).click();
      await page.getByRole('textbox', { name: '镜像引用' }).fill('invalid:visual');
      await page.getByRole('button', { name: '保存并执行' }).click();
      await page.getByRole('alert').waitFor();
      await shot('form-error');
      await go('tasks', width);
      await page.locator('.status-select').click();
      await page.locator('.n-base-select-menu').waitFor();
      await shot('task-filter-menu');
      await page.keyboard.press('Escape');
      await page.getByRole('button', { name: '查看日志 ↗' }).first().click();
      await page.locator('.terminal').waitFor();
      await shot('task-detail');
      await go('backups', width);
      await page.getByRole('button', { name: '创建策略', exact: true }).click();
      await page.getByRole('textbox', { name: '策略名称' }).waitFor();
      await shot('backup-policy');
      await go('backups', width);
      await page.getByRole('button', { name: '浏览快照 / 恢复 ↗' }).click();
      await page.getByRole('button', { name: '恢复此快照 →' }).waitFor();
      await shot('snapshots');
      await page.getByRole('button', { name: '检查未验证快照 →' }).click();
      await page.getByRole('checkbox', { name: '我了解快照未完成验证，仅救援到新目录' }).waitFor();
      assert(await page.getByRole('button', { name: '检查并预览覆盖范围' }).isDisabled());
      await shot('unverified-restore');
      await page.getByRole('checkbox', { name: '我了解快照未完成验证，仅救援到新目录' }).check();
      await page.getByRole('textbox', { name: '新目录绝对路径' }).fill('/restore/production-services/application-data/rescue-copy');
      await page.getByRole('button', { name: '检查并预览覆盖范围' }).click();
      await page.getByRole('button', { name: '继续确认' }).click();
      await confirm.waitFor();
      assert(await confirm.isDisabled());
      await shot('restore-risk-confirm');
      await page.getByRole('button', { name: '取消', exact: true }).click();
    }
    viewportHeight = 844;
    offline = true;
    await go('dashboard', 390);
    await page.getByText('视觉验收：Docker Engine 连接中断', { exact: true }).waitFor();
    assert.equal(await page.locator('.metrics').count(), 0);
    await shot('offline');
    offline = false;
    delayed = true;
    await page.reload();
    await page.getByText('正在读取实际状态…').waitFor();
    await shot('loading');
    delayed = false;
    await page.locator('.metrics').waitFor();
    for (const width of [390, 1440]) {
      viewportHeight = width === 390 ? 844 : 900;
      scenario = 'unknown';
      await go('dashboard', width);
      assert.deepEqual(await page.locator('.metric-value').allTextContents(), ['—', '—', '—', '—']);
      assert.equal(await page.locator('.activity-row').count(), 0);
      await shot('unknown-empty');
      scenario = 'long';
      await go('dashboard', width);
      await page.getByRole('alert').waitFor();
      assert(await page.locator('.notice.error').evaluate(el => el.scrollHeight <= el.clientHeight + 1));
      await shot('long-values-error');
    }
    scenario = 'normal';
    viewportHeight = 844;
    await go('dashboard', 390);
    const mobileRefresh = await page.locator('.host-summary button').boundingBox();
    assert(mobileRefresh.height >= 40, 'Mobile refresh touch target must be >=40px');
    // Focus containment and focus return of the mobile navigation.
    await page.getByRole('button', { name: '打开导航' }).click();
    const nav = page.getByRole('dialog', { name: '主导航' });
    await nav.waitFor();
    assert.equal(await page.locator('.workspace').getAttribute('inert'), '');
    await page.waitForTimeout(50);
    assert.equal(await page.locator('button[aria-current="page"]').evaluate(el => el === document.activeElement), true);
    await page.locator('.sidebar-user').focus();
    await page.keyboard.press('Tab');
    assert.equal(await page.locator('.sidebar-brand').evaluate(el => el === document.activeElement), true);
    await page.keyboard.press('Shift+Tab');
    assert.equal(await page.locator('.sidebar-user').evaluate(el => el === document.activeElement), true);
    await page.keyboard.press('Escape');
    await page.waitForTimeout(30);
    assert.equal(await page.getByRole('button', { name: '打开导航' }).evaluate(el => el === document.activeElement), true);
    assert.equal(await page.locator('.sidebar').getAttribute('inert'), '');
    await page.setViewportSize({ width: 1280, height: 600 });
    await page.waitForTimeout(50);
    assert.equal(await page.locator('.sidebar').getAttribute('inert'), null);
    assert.equal(await page.locator('.workspace').getAttribute('inert'), null);
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(out, 'fixture-results.json'), JSON.stringify({ kind: 'browser route fixtures only; no real resource actions', checks, writes, mobileFocus: 'pass', confirmationSafety: 'pass', editorFont: 'pass', browserErrors: errors }, null, 2));
    console.log(JSON.stringify({ screenshots: checks.length, writesIntercepted: writes.length, mobileFocus: 'pass', confirmationSafety: 'pass', browserErrors: errors }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
