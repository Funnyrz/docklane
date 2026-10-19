import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
vi.mock('naive-ui', async () => {
  const { defineComponent } = await import('vue')
  return {
    NButton: defineComponent({ props: ['disabled', 'loading'], template: '<button :disabled="disabled"><slot /></button>' }),
    NInput: defineComponent({ props: ['value'], emits: ['update:value'], template: '<input :value="value" @input="$emit(\'update:value\', $event.target.value)" />' }),
  }
})
import SettingsView from './SettingsView.vue'
import { version as appVersion } from '../../package.json'
import { setLocale } from '../i18n'
import type { Capability } from '../capabilities'
vi.mock('../components/ConfirmDialog.vue', () => ({ default: { template: '<div />' } }))

afterEach(() => vi.unstubAllGlobals())

function fixture(capabilities: Record<string, Capability>, mode = 'binary') {
  const fetch = vi.fn(async (input: string, options?: RequestInit) => {
    const url = String(input)
    let data: unknown = url.endsWith('/settings') ? { version: appVersion, mode, allowed_paths: null, path_mappings: null } : url.endsWith('/capabilities') ? { capabilities } : []
    if (url.endsWith('/host/daemon')) data = { available: false, reason: 'raw backend diagnosis', manual_guide: 'raw English guide' }
    if (url.endsWith('/host/daemon/preview')) data = { revision: 'fixture', configuration: {} }
    return new Response(JSON.stringify(data), { status: 200 })
  })
  vi.stubGlobal('fetch', fetch)
  return fetch
}

function button(wrapper: ReturnType<typeof mount>, text: string) {
  return wrapper.findAll('button').find(el => el.text() === text)!
}

describe('Settings capability explanations', () => {
  it('shows distinct Chinese states and expandable solutions without submitting actions', async () => {
    const fetch = fixture({
      'docker.manage': { available: true },
      'backup.local': { available: false, reason: 'Restic is not installed', reason_code: 'RESTIC_MISSING' },
      'backup.bind_mount': { available: false, reason_code: 'ALLOWED_PATHS_MISSING' },
      'host.daemon_config': { available: false, reason_code: 'DAEMON_WRITE_NOT_AUTHORIZED' },
      'host.service_restart': { available: false, reason_code: 'DAEMON_WRITE_NOT_AUTHORIZED' },
    })
    const wrapper = mount(SettingsView)
    await flushPromises()
    expect(wrapper.find('[id="capability-backup.local"] .capability-heading').text()).toBe('本地备份与恢复缺少工具')
    expect(wrapper.find('[id="capability-backup.bind_mount"] .tag').text()).toBe('未配置')
    expect(wrapper.find('[id="capability-host.daemon_config"] .tag').text()).toBe('未授权')
    expect(wrapper.find('.capability-reason').text()).not.toContain('Restic is not installed')
    expect(wrapper.findAll('.capability-help[open]')).toHaveLength(0)
    const help = wrapper.find('[id="capability-backup.local"] .capability-help')
    await help.find('summary').trigger('click')
    expect(help.text()).toContain('DM_RESTIC_PATH')
    expect(fetch.mock.calls.every(([,options]) => !options?.method)).toBe(true)
    expect(wrapper.find('.settings-paths').text()).not.toContain('null')
    expect(wrapper.find('.settings-paths').text()).toContain('无需路径映射')
    expect(button(wrapper,'预览配置修改').attributes('disabled')).toBeDefined()
    expect(button(wrapper,'应用并重启 Docker').attributes('disabled')).toBeDefined()
    setLocale('en', false)
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[id="capability-backup.local"] .tag').text()).toBe('Missing tool')
    expect(wrapper.find('[id="capability-host.daemon_config"] .tag').text()).toBe('Not authorized')
    wrapper.unmount()
  })

  it('keeps write controls disabled if only one backend capability is available', async () => {
    fixture({ 'host.daemon_config': { available: true }, 'host.service_restart': { available: false, reason_code: 'HOST_TOOL_MISSING', reason_params: { tool: 'systemctl' } } })
    const wrapper = mount(SettingsView)
    await flushPromises()
    expect(button(wrapper,'预览配置修改').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[id="capability-host.service_restart"] .capability-reason').text()).toContain('systemctl')
    await button(wrapper,'读取配置 / 操作指引').trigger('click')
    await flushPromises()
    expect(wrapper.find('.notice:not(.warning)').text()).toContain('手工操作')
    expect(wrapper.find('.notice.warning').text()).not.toContain('未授予')
    wrapper.unmount()
  })

  it('requires both backend permissions and a preview before allowing the existing confirmation flow', async () => {
    const fetch = fixture({ 'host.daemon_config': { available: true }, 'host.service_restart': { available: true } })
    const wrapper = mount(SettingsView)
    await flushPromises()
    expect(button(wrapper,'预览配置修改').attributes('disabled')).toBeUndefined()
    expect(button(wrapper,'应用并重启 Docker').attributes('disabled')).toBeDefined()
    await button(wrapper,'预览配置修改').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls.some(([url,options]) => String(url).endsWith('/host/daemon/preview') && options?.method === 'POST')).toBe(true)
    expect(button(wrapper,'应用并重启 Docker').attributes('disabled')).toBeUndefined()
    expect(fetch.mock.calls.some(([url]) => String(url).endsWith('/host/daemon/apply'))).toBe(false)
    wrapper.unmount()
  })

  it('explains missing container mappings without treating them as missing authorization', async () => {
    fixture({ 'backup.bind_mount': { available: false, reason_code: 'PATH_MAPPINGS_MISSING' } },'docker')
    const wrapper = mount(SettingsView)
    await flushPromises()
    expect(wrapper.find('.settings-paths').text()).toContain('宿主机与容器')
    expect(wrapper.find('[id="capability-backup.bind_mount"] .tag').text()).toBe('未配置')
    expect(wrapper.find('[id="capability-backup.bind_mount"] .capability-help').text()).toContain('DM_PATH_MAPPINGS')
    wrapper.unmount()
  })
})


describe('Beta release information', () => {
  it.each([
    ['zh-CN', "本程序尚未经过充分、严格的测试，仅供学习与测试使用，请勿用于生产环境。使用前请自行评估风险并备份重要数据。"],
    ['en', "This program has not undergone sufficient, rigorous testing. It is for learning and testing only; do not use it in production. Assess the risks yourself and back up important data before use."],
  ] as const)('shows the full version and a nonmodal disclaimer in %s', async (language, disclaimer) => {
    setLocale(language, false)
    const fetch = fixture({})
    const wrapper = mount(SettingsView)
    await flushPromises()
    expect(wrapper.find('.release-disclaimer').text()).toBe(disclaimer)
    expect(wrapper.find('.release-disclaimer').attributes('role')).toBe('note')
    expect(wrapper.find('.facts').text()).toContain(appVersion)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(fetch.mock.calls.every(([, options]) => !options?.method)).toBe(true)
    wrapper.unmount()
  })
})
