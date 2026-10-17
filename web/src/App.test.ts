import { shallowMount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'
import { setLocale } from './i18n'
import { useAuth } from './stores/auth'

beforeEach(() => {
  setActivePinia(createPinia())
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({
    matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn(),
  }))
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

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

    auth.authenticated = true
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.sidebar-brand .brand-copy').text()).toBe('DOCKLANE')
    expect(wrapper.find('.page-caption').text()).toContain('DOCKLANE')
    expect(wrapper.find('.page-footer').text()).toContain('Docklane / v0.3')
    expect(wrapper.text()).not.toMatch(/docker\s*manager/i)
    wrapper.unmount()
  })
})
