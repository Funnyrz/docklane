import { enableAutoUnmount, shallowMount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'
import { version as appVersion } from '../package.json'
import { setLocale } from './i18n'
import { useAuth } from './stores/auth'

enableAutoUnmount(afterEach)
beforeEach(() => {
  localStorage.clear()
  location.hash = ''
  setActivePinia(createPinia())
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({
    matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn(),
  }))
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); localStorage.clear() })

describe('Docklane branding', () => {
  it.each([
    ['zh-CN', 'Docklane · 运维控制台'],
    ['en', 'Docklane · Operations Console'],
  ] as const)('shows the product name on login and in the authenticated shell in %s', async (language, title) => {
    setLocale(language, false)
    const auth = useAuth()
    auth.ready = true
    auth.initialized = true
    vi.spyOn(auth, 'refresh').mockResolvedValue(undefined)
    const wrapper = shallowMount(App, {
      global: { renderStubDefaultSlot: true },
    })

    expect(document.title).toBe(title)
    expect(wrapper.find('.auth-story .brand').text()).toBe('DOCKLANE')
    expect(wrapper.find('.auth-story-footer').text()).toContain(`v${appVersion}`)

    auth.authenticated = true
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.sidebar-brand .brand-copy').text()).toBe('DOCKLANE')
    expect(wrapper.find('.page-footer').text()).toContain(`Docklane / v${appVersion}`)
    expect(wrapper.find('.version').text()).toContain(`v${appVersion}`)
    expect(wrapper.text()).not.toMatch(/docker\s*manager/i)
    wrapper.unmount()
  })
})

describe('Console navigation', () => {
  it.each([
    ['zh-CN', ['工作台', '容器', 'Compose 项目', '镜像与仓库', '网络', '存储', '备份与恢复', '任务中心', '系统设置']],
    ['en', ['Overview', 'Containers', 'Compose projects', 'Images & registries', 'Networks', 'Storage', 'Backup & recovery', 'Task center', 'Settings']],
  ] as const)('keeps all nine routes accessible with one active page and single navigation labels in %s', async (language, titles) => {
    setLocale(language, false)
    const auth = useAuth()
    auth.ready = true
    auth.authenticated = true
    vi.spyOn(auth, 'refresh').mockResolvedValue(undefined)
    const wrapper = shallowMount(App, { global: { renderStubDefaultSlot: true } })
    const routes = ['dashboard', 'containers', 'compose', 'images', 'networks', 'volumes', 'backups', 'tasks', 'settings']
    const buttons = wrapper.findAll('.sidebar nav button')

    expect(buttons).toHaveLength(9)
    expect(buttons.map(button => button.find('.nav-label').text())).toEqual(titles)
    expect(wrapper.find('.page-heading').exists()).toBe(false)
    expect(wrapper.find('dashboard-view-stub').exists()).toBe(true)

    for (const [index, route] of routes.entries()) {
      await buttons[index]!.trigger('click')
      expect(location.hash).toBe(`#${route}`)
      expect(wrapper.findAll('.sidebar nav [aria-current="page"]')).toHaveLength(1)
      expect(buttons[index]!.attributes('aria-current')).toBe('page')
      if (route !== 'dashboard') expect(wrapper.find('.page-heading h1').text()).toBe(titles[index])
      if (['containers', 'images', 'networks', 'volumes'].includes(route)) {
        expect(wrapper.find('resources-view-stub').attributes('kind')).toBe(route)
      } else {
        const component = { dashboard: 'dashboard', compose: 'compose', backups: 'backup', tasks: 'tasks', settings: 'settings' }[route]
        expect(wrapper.find(`${component}-view-stub`).exists()).toBe(true)
      }
    }
  })
})

describe('Collapsible desktop navigation', () => {
  const preference = 'docker-manager:sidebar-collapsed'
  function mountConsole() {
    const auth = useAuth()
    auth.ready = true
    auth.authenticated = true
    auth.user = { username: 'admin' }
    vi.spyOn(auth, 'refresh').mockResolvedValue(undefined)
    return shallowMount(App, { global: { renderStubDefaultSlot: true } })
  }

  it.each(['zh-CN', 'en'] as const)('toggles and restores the saved desktop preference in %s', async language => {
    setLocale(language, false)
    const wrapper = mountConsole()
    expect(wrapper.find('.shell').classes()).not.toContain('sidebar-collapsed')
    const toggle = wrapper.find('.sidebar-toggle')
    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(toggle.attributes('aria-controls')).toBe('main-navigation')

    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(toggle.attributes('aria-label')).toBe(language === 'en' ? 'Expand navigation' : '展开菜单')
    expect(wrapper.find('.shell').classes()).toContain('sidebar-collapsed')
    expect(localStorage.getItem(preference)).toBe('true')
    const items = wrapper.findAll('.sidebar nav button')
    expect(items).toHaveLength(9)
    for (const item of items) {
      expect(item.attributes('title')).toBe(item.find('.nav-label').text())
      expect(item.attributes('aria-label')).toBe(item.find('.nav-label').text())
    }
    expect(wrapper.find('.sidebar-user').attributes('title')).toContain('admin')
    await items[1]!.trigger('click')
    expect(location.hash).toBe('#containers')
    expect(wrapper.find('.shell').classes()).toContain('sidebar-collapsed')
    wrapper.unmount()

    const restored = mountConsole()
    await restored.vm.$nextTick()
    expect(restored.find('.shell').classes()).toContain('sidebar-collapsed')
    await restored.find('.sidebar-toggle').trigger('click')
    expect(localStorage.getItem(preference)).toBe('false')
    expect(restored.find('.shell').classes()).not.toContain('sidebar-collapsed')
    expect(restored.find('.sidebar nav button').attributes('title')).toBeUndefined()
  })

  it('keeps the mobile drawer expanded without losing the desktop preference', async () => {
    localStorage.setItem(preference, 'true')
    let resize = () => {}
    const query = { matches: true, addEventListener: vi.fn((_event, fn) => { resize = fn }), removeEventListener: vi.fn() }
    vi.mocked(window.matchMedia).mockReturnValue(query as unknown as MediaQueryList)
    const wrapper = mountConsole()
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.shell').classes()).not.toContain('sidebar-collapsed')
    expect(wrapper.find('.sidebar-toggle').exists()).toBe(false)
    await wrapper.find('.mobile-menu').trigger('click')
    expect(wrapper.find('.sidebar').attributes('aria-modal')).toBe('true')
    expect(wrapper.find('.sidebar').classes()).toContain('open')
    expect(localStorage.getItem(preference)).toBe('true')

    query.matches = false
    resize()
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.shell').classes()).toContain('sidebar-collapsed')
    expect(wrapper.find('.sidebar').classes()).not.toContain('open')
    expect(wrapper.find('.sidebar-toggle').exists()).toBe(true)
    expect(wrapper.find('.sidebar').attributes('aria-modal')).toBeUndefined()
  })

  it('still toggles when browser storage cannot be read or written', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new DOMException('denied', 'SecurityError') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('full', 'QuotaExceededError') })
    const wrapper = mountConsole()
    await wrapper.find('.sidebar-toggle').trigger('click')
    expect(wrapper.find('.shell').classes()).toContain('sidebar-collapsed')
    await wrapper.find('.sidebar-toggle').trigger('click')
    expect(wrapper.find('.shell').classes()).not.toContain('sidebar-collapsed')
  })

  it('uses expanded navigation when the saved preference is invalid', async () => {
    localStorage.setItem(preference, 'invalid')
    const wrapper = mountConsole()
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.shell').classes()).not.toContain('sidebar-collapsed')
  })
})
