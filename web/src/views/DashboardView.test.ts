import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Row } from '../api'
import { formatDateTime, setLocale } from '../i18n'

vi.mock('naive-ui', async () => {
  const { defineComponent } = await import('vue')
  return { NButton: defineComponent({ template: '<button><slot/></button>' }) }
})
import DashboardView from './DashboardView.vue'

enableAutoUnmount(afterEach)
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })

function dashboard(): Row {
  return {
    host: {
      hostname: 'production-docker-01', os: 'linux', architecture: 'amd64', cpus: 8, load_1m: 0.42,
      memory_total: 16 * 1024 ** 3, memory_available: 4 * 1024 ** 3, uptime: 180000,
      disk: { available: 64 * 1024 ** 3, total: 128 * 1024 ** 3 },
      kernel: '6.8.0', docker_root_dir: '/var/lib/docker',
    },
    docker: { connected: true, version: { Version: '27.5.1', ApiVersion: '1.47' } },
    containers: { total: 12, running: 9 }, images: 23, volumes: 7,
    backup: {
      unprotected: 3, latest: { created_at: '2026-10-19T01:30:00Z' },
      coverage: { basis: 'enabled policies', containers: { protected: 10, total: 12 }, volumes: { protected: 6, total: 7 } },
    },
  }
}

function mockRequests(data: Row = dashboard(), tasks: Row[] = []) {
  const request = vi.fn(async (path: string) => new Response(JSON.stringify(path.endsWith('/tasks') ? tasks : data), { status: 200 }))
  vi.stubGlobal('fetch', request)
  return request
}

describe('Dashboard summary', () => {
  it.each([
    ['zh-CN', '工作台', ['全部容器', '运行中', '本地镜像', '数据卷']],
    ['en', 'Overview', ['All containers', 'Running', 'Local images', 'Volumes']],
  ] as const)('shows the host, four resource counts and their destinations in %s', async (language, title, labels) => {
    setLocale(language, false)
    mockRequests()
    const wrapper = mount(DashboardView)
    await flushPromises()

    expect(wrapper.find('.host-summary h1').text()).toBe(title)
    expect(wrapper.find('.host-summary').text()).toContain('production-docker-01')
    const metrics = wrapper.findAll('.metric-card')
    expect(metrics).toHaveLength(4)
    expect(metrics.map(metric => metric.find('.metric-value').text())).toEqual(['12', '9', '23', '7'])
    for (const [index, metric] of metrics.entries()) {
      expect(metric.text()).toContain(labels[index])
      await metric.trigger('click')
    }
    expect(wrapper.emitted('navigate')).toEqual([['containers'], ['containers'], ['images'], ['volumes']])
  })

  it('retains all eight host facts, backup coverage, verification time and consistency warning', async () => {
    mockRequests()
    const wrapper = mount(DashboardView)
    await flushPromises()

    expect(wrapper.findAll('.facts dt').map(fact => fact.text())).toEqual([
      '操作系统 / 架构', 'Docker / API 版本', 'CPU / 1 分钟负载', '主机内存',
      '主机运行时间', '磁盘（可用 / 总量）', '内核', 'Docker 根目录',
    ])
    expect(wrapper.findAll('.facts dd').map(fact => fact.text())).toEqual([
      'linux / amd64', '27.5.1 / 1.47', '8 核 / 0.42', '4.0 GiB 可用 / 16.0 GiB',
      '2 天 2 小时', '64.0 GiB / 128.0 GiB', '6.8.0', '/var/lib/docker',
    ])
    expect(wrapper.find('.protection').text()).toContain('容器保护 10 / 12 · 数据卷 6 / 7')
    expect(wrapper.find('.protection').text()).toContain(formatDateTime('2026-10-19T01:30:00Z'))
    expect(wrapper.find('.protection').text()).toContain('文件级备份不等于数据库一致性备份')
    await wrapper.find('.protection button').trigger('click')
    await wrapper.find('.recent-tasks button').trigger('click')
    expect(wrapper.emitted('navigate')).toEqual([['backups'], ['tasks']])
  })

  it('shows at most five recent tasks and exposes the full target path', async () => {
    const tasks = Array.from({ length: 6 }, (_, index) => ({
      id: `task-${index}`, type: 'container.start', target: `/srv/production/a-long-project-name/container-${index}`, state: 'succeeded',
    }))
    mockRequests(dashboard(), tasks)
    const wrapper = mount(DashboardView)
    await flushPromises()

    const entries = wrapper.findAll('.activity-row')
    expect(entries).toHaveLength(5)
    expect(entries.map(entry => entry.find('.activity-target').text())).toEqual(tasks.slice(0, 5).map(task => task.target))
    expect(entries[0]!.find('.activity-target').attributes('title')).toBe(tasks[0]!.target)
    expect(wrapper.text()).not.toContain('container-5')
    expect(entries[0]!.text()).toContain('启动容器')
    expect(entries[0]!.text()).toContain('已成功')
  })

  it('uses unknown markers instead of inventing zero resource counts', async () => {
    mockRequests({ host: {}, docker: { connected: true } })
    const wrapper = mount(DashboardView)
    await flushPromises()

    expect(wrapper.findAll('.metric-value').map(value => value.text())).toEqual(['—', '—', '—', '—'])
    expect(wrapper.findAll('.facts dd').every(value => value.text().includes('—'))).toBe(true)
    expect(wrapper.find('.protection').text()).toContain('暂无记录')
    expect(wrapper.find('.quiet-empty').text()).toContain('暂无可显示的任务记录')
  })

  it('shows the Engine error and hides resource counts when Docker is disconnected', async () => {
    mockRequests({ host: { hostname: 'test' }, docker: { connected: false, error: 'Docker socket unavailable' }, containers: null, images: null, volumes: null })
    const wrapper = mount(DashboardView)
    await flushPromises()

    expect(wrapper.find('[role="alert"]').text()).toContain('Docker socket unavailable')
    expect(wrapper.find('.metrics').exists()).toBe(false)
    expect(wrapper.text()).toContain('等待 Docker Engine 恢复连接')
  })

  it('reports failed requests while keeping task history available', async () => {
    vi.stubGlobal('fetch', vi.fn(async (path: string) => path.endsWith('/tasks')
      ? new Response(JSON.stringify([{ id: 'queued-task', type: 'backup.verify', target: 'daily', state: 'queued' }]))
      : new Response(JSON.stringify({ error: { message: 'Host unavailable' } }), { status: 503 })))
    const wrapper = mount(DashboardView)
    await flushPromises()

    expect(wrapper.find('[role="alert"]').text()).toContain('Host unavailable')
    expect(wrapper.find('.metrics').exists()).toBe(false)
    expect(wrapper.find('.activity-row').text()).toContain('daily')
  })

  it('refreshes manually and every 15 seconds, then stops polling after unmount', async () => {
    vi.useFakeTimers()
    const request = mockRequests()
    const wrapper = mount(DashboardView)
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(2)

    await wrapper.find('.host-summary button').trigger('click')
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(4)
    await vi.advanceTimersByTimeAsync(15000)
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(6)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(30000)
    expect(request).toHaveBeenCalledTimes(6)
  })
})
