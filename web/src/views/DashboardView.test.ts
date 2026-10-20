import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Row } from '../api'
import { formatDateTime, setLocale } from '../i18n'

vi.mock('naive-ui', async () => {
  const { defineComponent } = await import('vue')
  return { NButton: defineComponent({ template: '<button><slot/></button>' }) }
})
import DashboardView from './DashboardView.vue'

enableAutoUnmount(afterEach)
beforeEach(() => setLocale('zh-CN', false))
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
    containers: { total: 12, running: 9, stopped: 3 }, images: 23, volumes: 7,
    backup: {
      unprotected: 3, latest: { created_at: '2026-10-19T01:30:00Z' },
      coverage: { basis: 'enabled policies', containers: { protected: 10, total: 12 }, volumes: { protected: 6, total: 7 } },
    },
  }
}

function mockRequests(data: Row = dashboard(), tasks: Row[] = []) {
  const request = vi.fn(async (path: string) => new Response(JSON.stringify(path.includes('/containers?') ? { items: [], total: 0 } : path.endsWith('/tasks') ? tasks : data), { status: 200 }))
  vi.stubGlobal('fetch', request)
  return request
}

describe('Dashboard summary', () => {
  it.each([
    ['zh-CN', '工作台', ['运行中', '已停止', '本地镜像', '数据卷']],
    ['en', 'Overview', ['Running', 'Stopped', 'Local images', 'Volumes']],
  ] as const)('shows the host, four resource counts and their destinations in %s', async (language, title, labels) => {
    setLocale(language, false)
    mockRequests()
    const wrapper = mount(DashboardView)
    await flushPromises()

    expect(wrapper.find('.host-summary h1').text()).toBe(title)
    expect(wrapper.find('.host-summary').text()).toContain('production-docker-01')
    const metrics = wrapper.findAll('.metric-card')
    expect(metrics).toHaveLength(4)
    expect(metrics.map(metric => metric.find('.metric-value').text())).toEqual(['9', '3', '23', '7'])
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
    expect(wrapper.find('.recent-tasks .quiet-empty').text()).toContain('暂无可显示的任务记录')
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
    expect(request).toHaveBeenCalledTimes(3)

    await wrapper.find('.host-summary button').trigger('click')
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(6)
    await vi.advanceTimersByTimeAsync(15000)
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(9)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(30000)
    expect(request).toHaveBeenCalledTimes(9)
  })
})

describe('Dashboard live resources and container overview', () => {
  it('starts with real values, only charts scheduled samples and breaks lines after a failed sample', async () => {
    vi.useFakeTimers(); vi.setSystemTime('2026-10-19T10:00:00Z')
    let failed = false
    vi.stubGlobal('fetch', vi.fn(async (path: string) => {
      if (path.includes('/containers?')) return new Response(JSON.stringify({ items: [] }))
      if (path.endsWith('/tasks')) return new Response('[]')
      if (failed) return new Response(JSON.stringify({ error: { message: 'offline' } }), { status: 503 })
      const value = dashboard(); Object.assign(value.host as Row, { cpu_percent: 12.5, cpu_status: 'ready', cpu_sampled_at: new Date().toISOString() })
      return new Response(JSON.stringify(value))
    }))
    const wrapper = mount(DashboardView); await flushPromises()
    expect(wrapper.findAll('.resource-chart-value')[0]!.text()).toBe('12.5%')
    expect(wrapper.findAll('.resource-chart-value')[1]!.text()).toContain('12.0 GiB')
    expect(wrapper.findAll('.trend-line')).toHaveLength(0)
    await wrapper.find('.host-summary button').trigger('click'); await flushPromises()
    expect(wrapper.findAll('.trend-line')).toHaveLength(0)
    await vi.advanceTimersByTimeAsync(15000); await flushPromises()
    expect(wrapper.findAll('.trend-line')).toHaveLength(3)
    failed = true; await vi.advanceTimersByTimeAsync(15000); await flushPromises()
    expect(wrapper.findAll('.resource-chart-value')[0]!.text()).toBe('—')
    failed = false; await vi.advanceTimersByTimeAsync(15000); await flushPromises()
    expect(wrapper.findAll('.trend-line')).toHaveLength(3)
    await vi.advanceTimersByTimeAsync(15000); await flushPromises()
    expect(wrapper.findAll('.trend-line')).toHaveLength(6)
  })

  it('keeps warming, unsupported and invalid metrics honest', async () => {
    const value = dashboard()
    Object.assign(value.host as Row, { cpu_status: 'unsupported', cpu_percent: 70, memory_available: -1, disk: { total: 100, available: 200 } })
    mockRequests(value)
    const wrapper = mount(DashboardView); await flushPromises()
    expect(wrapper.findAll('.resource-chart-value').map(item => item.text())).toEqual(['—', '—', '—'])
    expect(wrapper.find('.resource-chart').text()).toContain('Linux 二进制部署中通过本地 Unix 套接字连接 Docker')
    expect(wrapper.findAll('.trend-line')).toHaveLength(0)
  })

  it('shows five containers with creation times and opens the existing detail flow', async () => {
    const containers = Array.from({ length: 6 }, (_, i) => ({ Id: `id-${i}`, Names: [`/nginx-${i}`], State: 'running', Image: 'nginx:latest', Created: 1729324800, Ports: [{ PublicPort: 8080, PrivatePort: 80, Type: 'tcp' }] }))
    const request = vi.fn(async (path: string) => new Response(JSON.stringify(path.includes('/containers?') ? { items: containers, total: 12 } : path.endsWith('/tasks') ? [] : dashboard())))
    vi.stubGlobal('fetch', request)
    const wrapper = mount(DashboardView); await flushPromises()
    expect(request.mock.calls.some(([path]) => path.endsWith('/containers?page=1&page_size=5&q='))).toBe(true)
    expect(wrapper.findAll('.dashboard-container-table tbody tr')).toHaveLength(5)
    expect(wrapper.find('.container-count').text()).toBe('12')
    expect(wrapper.find('.container-ports').text()).toBe('8080 → 80/tcp')
    expect(wrapper.find('time').attributes('datetime')).toBe(new Date(1729324800000).toISOString())
    await wrapper.find('.container-name').trigger('click')
    expect(JSON.parse(sessionStorage.getItem('docker-manager:inspect')!)).toEqual({ kind: 'containers', id: 'id-0' })
    expect(wrapper.emitted('navigate')).toEqual([['containers']])
  })

  it('debounces server-side searches and ignores stale responses', async () => {
    vi.useFakeTimers()
    let resolveOld: ((response: Response) => void) | undefined
    const request = vi.fn(async (path: string) => {
      if (path.includes('&q=old')) return await new Promise<Response>(resolve => { resolveOld = resolve })
      return new Response(JSON.stringify(path.includes('/containers?') ? { items: path.includes('&q=new') ? [{ Id: 'new', Names: ['/new'], State: 'running' }] : [], total: 1 } : path.endsWith('/tasks') ? [] : dashboard()))
    })
    vi.stubGlobal('fetch', request)
    const wrapper = mount(DashboardView); await flushPromises()
    await wrapper.find('input[type="search"]').setValue('old')
    await vi.advanceTimersByTimeAsync(299); expect(resolveOld).toBeUndefined()
    await vi.advanceTimersByTimeAsync(1); expect(resolveOld).toBeTypeOf('function')
    await wrapper.find('input[type="search"]').setValue('new')
    await vi.advanceTimersByTimeAsync(300); await flushPromises()
    expect(wrapper.find('.container-name').text()).toBe('new')
    resolveOld!(new Response(JSON.stringify({ items: [{ Id: 'old', Names: ['/old'] }] }))); await flushPromises()
    expect(wrapper.find('.container-name').text()).toBe('new')
    await wrapper.find('input[type="search"]').setValue('never-requested')
    wrapper.unmount(); const calls = request.mock.calls.length
    await vi.advanceTimersByTimeAsync(30000); expect(request.mock.calls).toHaveLength(calls)
  })

  it('shows independent container and task errors while host data stays available', async () => {
    vi.stubGlobal('fetch', vi.fn(async (path: string) => path.endsWith('/dashboard') ? new Response(JSON.stringify(dashboard())) : new Response(JSON.stringify({ error: { message: path.endsWith('/tasks') ? 'Task service unavailable' : 'Container list unavailable' } }), { status: 503 })))
    const wrapper = mount(DashboardView); await flushPromises()
    expect(wrapper.findAll('.metric-card')).toHaveLength(4)
    expect(wrapper.find('.dashboard-containers [role="alert"]').text()).toContain('Container list unavailable')
    expect(wrapper.find('.recent-tasks [role="alert"]').text()).toContain('Task service unavailable')
    expect(wrapper.find('.recent-tasks .quiet-empty').exists()).toBe(false)
  })

  it('avoids overlapping summary polling and aborts pending work when unmounted', async () => {
    vi.useFakeTimers()
    let summarySignal: AbortSignal | null | undefined
    const request = vi.fn(async (path: string, options: RequestInit) => {
      if (path.endsWith('/dashboard')) { summarySignal = options.signal; return await new Promise<Response>(() => {}) }
      return new Response('[]')
    })
    vi.stubGlobal('fetch', request)
    const wrapper = mount(DashboardView); await flushPromises()
    await vi.advanceTimersByTimeAsync(45000)
    expect(request.mock.calls.filter(([path]) => path.endsWith('/dashboard'))).toHaveLength(1)
    wrapper.unmount(); expect(summarySignal?.aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(30000)
    expect(request.mock.calls).toHaveLength(3)
  })

  it.each(['tasks', 'containers'])('keeps summary sampling and manual refresh independent of pending %s requests', async (pending) => {
    vi.useFakeTimers()
    const signals: AbortSignal[] = []
    const request = vi.fn(async (path: string, options: RequestInit) => {
      if (path.includes(pending === 'tasks' ? '/tasks' : '/containers?')) {
        signals.push(options.signal as AbortSignal)
        return await new Promise<Response>(() => {})
      }
      return new Response(JSON.stringify(path.endsWith('/dashboard') ? dashboard() : []))
    })
    vi.stubGlobal('fetch', request)
    const wrapper = mount(DashboardView); await flushPromises()
    await vi.advanceTimersByTimeAsync(45000); await flushPromises()
    expect(request.mock.calls.filter(([path]) => path.endsWith('/dashboard'))).toHaveLength(4)
    expect(signals.slice(0, -1).every(signal => signal.aborted)).toBe(true)
    expect(signals.at(-1)!.aborted).toBe(false)
    await wrapper.find('.host-summary button').trigger('click'); await flushPromises()
    expect(request.mock.calls.filter(([path]) => path.endsWith('/dashboard'))).toHaveLength(5)
    wrapper.unmount()
    expect(signals.every(signal => signal.aborted)).toBe(true)
    const calls = request.mock.calls.length
    await vi.advanceTimersByTimeAsync(30000); expect(request.mock.calls).toHaveLength(calls)
  })

  it('does not let an aborted task request overwrite newer history', async () => {
    vi.useFakeTimers()
    let resolveOld: ((response: Response) => void) | undefined, requests = 0
    vi.stubGlobal('fetch', vi.fn(async (path: string) => {
      if (path.endsWith('/tasks')) {
        requests++
        if (requests === 1) return await new Promise<Response>(resolve => { resolveOld = resolve })
        return new Response(JSON.stringify([{ id: 'new', type: 'backup.verify', target: 'latest', state: 'queued' }]))
      }
      return new Response(JSON.stringify(path.endsWith('/dashboard') ? dashboard() : []))
    }))
    const wrapper = mount(DashboardView); await flushPromises()
    await vi.advanceTimersByTimeAsync(15000); await flushPromises()
    expect(wrapper.find('.activity-target').text()).toBe('latest')
    resolveOld!(new Response(JSON.stringify([{ id: 'old', target: 'stale' }]))); await flushPromises()
    expect(wrapper.find('.activity-target').text()).toBe('latest')
  })

  it('breaks history and suspends requests while the page is hidden', async () => {
    vi.useFakeTimers(); vi.setSystemTime('2026-10-19T10:00:00Z')
    let visibility: DocumentVisibilityState = 'visible'
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
    const request = mockRequests()
    const wrapper = mount(DashboardView); await flushPromises()
    await vi.advanceTimersByTimeAsync(15000); await flushPromises()
    expect(wrapper.findAll('.trend-line')).toHaveLength(2)
    visibility = 'hidden'; document.dispatchEvent(new Event('visibilitychange'))
    const calls = request.mock.calls.length
    await vi.advanceTimersByTimeAsync(45000); expect(request.mock.calls).toHaveLength(calls)
    visibility = 'visible'; document.dispatchEvent(new Event('visibilitychange')); await flushPromises()
    expect(wrapper.findAll('.trend-line')).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(15000); await flushPromises()
    expect(wrapper.findAll('.trend-line')).toHaveLength(4)
  })
})
