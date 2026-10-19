import { flushPromises, mount } from '@vue/test-utils'
import { NCheckbox, NModal } from 'naive-ui'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import CapabilitySettingsEditor from './CapabilitySettingsEditor.vue'
import { setLocale } from '../i18n'
import type { CapabilitySettingsState } from '../settings'

const state: CapabilitySettingsState = {
  effective: { allowed_paths: [], path_mappings: [], enable_daemon_write: false },
  saved: { allowed_paths: ['/srv/data'], path_mappings: [{ host: '/srv/data', local: '/data' }], enable_daemon_write: true },
  pending_restart: true, daemon_toggle_supported: true,
}
let wrapper: ReturnType<typeof mount> | undefined
beforeEach(() => {
  setLocale('zh-CN', false)
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals(); document.body.innerHTML = '' })
async function open(value = state) {
  wrapper = mount(CapabilitySettingsEditor, { attachTo: document.body, props: { show: false, state: structuredClone(value) } })
  await wrapper.setProps({ show: true }); await flushPromises()
  return wrapper
}
function button(text: string) {
  return Array.from(document.querySelectorAll('button')).find(el => el.textContent?.trim() === text)!
}
async function input(id: string, value: string) {
  const field = document.getElementById(id) as HTMLInputElement
  field.value = value
  field.dispatchEvent(new Event('input', { bubbles: true }))
  await flushPromises()
}

describe('Capability settings editor', () => {
  it('loads saved drafts and requires a password and explicit acknowledgement before saving', async () => {
    const fetch = vi.fn(async () => new Response(JSON.stringify({ capability_settings: state })))
    vi.stubGlobal('fetch', fetch)
    const view = await open()
    expect((document.getElementById('allowed-directories') as HTMLTextAreaElement).value).toBe('/srv/data')
    expect(button('保存配置').disabled).toBe(true)
    await input('allowed-directories', ' /srv/data \n\n /srv/backups ')
    await input('settings-admin-password', 'administrator-current-password')
    expect(button('保存配置').disabled).toBe(true)
    view.findAllComponents(NCheckbox).at(-1)!.vm.$emit('update:checked', true)
    await flushPromises()
    button('保存配置').click(); await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(1)
    const [url, options] = fetch.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/settings/capabilities')
    expect(JSON.parse(options.body as string)).toEqual({ allowed_paths: ['/srv/data', '/srv/backups'], path_mappings: [{ host: '/srv/data', local: '/data' }], enable_daemon_write: true, password: 'administrator-current-password', confirm: true })
    expect(view.emitted('saved')).toHaveLength(1)
    expect(view.emitted('update:show')).toEqual([[false]])
    expect((document.getElementById('settings-admin-password') as HTMLInputElement).value).toBe('')
    expect(view.findComponent(NModal).props('autoFocus')).toBe(false)
  })

  it('shows localized save errors, clears password and preserves the draft for correction', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { code: 'PASSWORD_CONFIRMATION_FAILED' } }), { status: 403 })))
    const view = await open()
    await input('settings-admin-password', 'wrong-password')
    view.findAllComponents(NCheckbox).at(-1)!.vm.$emit('update:checked', true)
    await flushPromises(); button('保存配置').click(); await flushPromises()
    expect(document.body.textContent).toContain('当前管理员密码不正确')
    expect((document.getElementById('settings-admin-password') as HTMLInputElement).value).toBe('')
    expect((document.getElementById('allowed-directories') as HTMLTextAreaElement).value).toBe('/srv/data')
    expect(button('保存配置').disabled).toBe(true)
    expect(view.emitted('saved')).toBeUndefined()
    setLocale('en', false); await flushPromises()
    expect(document.body.textContent).toContain('administrator password is incorrect')
  })

  it('disables unsupported host authorization while allowing mappings to be edited and removed', async () => {
    vi.stubGlobal('fetch', vi.fn())
    const view = await open({ ...state, daemon_toggle_supported: false })
    const daemonInput = document.getElementById('daemon-authorization') as HTMLInputElement
    expect(daemonInput.disabled).toBe(true)
    expect(daemonInput.checked).toBe(false)
    expect(document.body.textContent).toContain('目录配置仍可保存')
    button('添加映射').click(); await flushPromises()
    expect(document.getElementById('mapping-host-1')).not.toBeNull()
    document.querySelector<HTMLButtonElement>('[aria-label="删除第 2 条映射"]')!.click(); await flushPromises()
    expect(document.getElementById('mapping-host-1')).toBeNull()
    expect(view.emitted('saved')).toBeUndefined()
  })

  it('can restore active values and clears sensitive confirmation on reopening', async () => {
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    const view = await open()
    await input('settings-admin-password', 'do-not-retain')
    button('使用当前生效值').click(); await flushPromises()
    expect((document.getElementById('allowed-directories') as HTMLTextAreaElement).value).toBe('')
    expect(document.getElementById('mapping-host-0')).toBeNull()
    await view.setProps({ show: false }); await view.setProps({ show: true }); await flushPromises()
    expect((document.getElementById('settings-admin-password') as HTMLInputElement).value).toBe('')
    expect((document.getElementById('allowed-directories') as HTMLTextAreaElement).value).toBe('/srv/data')
    expect(view.findAllComponents(NCheckbox).at(-1)!.props('checked')).toBe(false)
    expect(fetch).not.toHaveBeenCalled()
  })
})
