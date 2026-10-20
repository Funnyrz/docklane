import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
vi.mock('naive-ui', async () => {
  const { defineComponent, nextTick, watch } = await import('vue')
  return {
    NButton: defineComponent({ props: ['disabled', 'loading', 'attrType'], template: '<button :type="attrType || \'button\'" :disabled="disabled || loading"><slot /></button>' }),
    NInput: defineComponent({ props: ['value', 'type', 'inputProps'], emits: ['update:value'], template: '<textarea v-if="type === \'textarea\'" v-bind="inputProps" :value="value" @input="$emit(\'update:value\', $event.target.value)" /><input v-else v-bind="inputProps" :type="type || \'text\'" :value="value" @input="$emit(\'update:value\', $event.target.value)" />' }),
    NDrawer: defineComponent({ name: 'NDrawer', props: ['show'], emits: ['update:show', 'after-leave'], setup(props, { emit }) { watch(() => props.show, async show => { if (!show) { await nextTick(); emit('after-leave') } }) }, template: '<section v-if="show" role="dialog" class="test-drawer"><slot /></section>' }),
    NDrawerContent: defineComponent({ props: ['title'], template: '<section><h2>{{ title }}</h2><slot /><slot name="footer" /></section>' }),
    NModal: defineComponent({ props: ['show', 'title'], template: '<section v-if="show" role="dialog" class="test-modal"><h2>{{ title }}</h2><slot /><slot name="footer" /></section>' }),
    NCheckbox: defineComponent({ props: ['checked'], emits: ['update:checked'], template: '<label><input type="checkbox" :checked="checked" @change="$emit(\'update:checked\', $event.target.checked)" /><slot /></label>' }),
  }
})
vi.mock('../components/CapabilitySettingsEditor.vue', () => ({ default: { name: 'CapabilitySettingsEditor', props: ['show', 'state'], emits: ['saved', 'update:show'], template: '<section v-if="show" class="test-editor" role="dialog"><button @click="$emit(\'update:show\', false)">Close editor</button></section>' } }))
import SettingsView from './SettingsView.vue'
import { version as appVersion } from '../../package.json'
import { setLocale } from '../i18n'
import type { Capability } from '../capabilities'
import type { CapabilitySettingsState } from '../settings'

const wrappers: VueWrapper[] = []
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.unstubAllGlobals() })

const capabilities: Record<string, Capability> = {
  'docker.manage': { available: true },
  'compose.manage': { available: false, reason_code: 'COMPOSE_V2_UNAVAILABLE' },
  'backup.local': { available: false, reason: 'Restic is not installed', reason_code: 'RESTIC_MISSING' },
  'backup.bind_mount': { available: false, reason_code: 'ALLOWED_PATHS_MISSING' },
  'host.daemon_config': { available: false, reason_code: 'DAEMON_WRITE_NOT_AUTHORIZED' },
  'host.service_restart': { available: false, reason_code: 'DAEMON_WRITE_NOT_AUTHORIZED' },
}
const configuration = { allowed_paths: [], path_mappings: [], enable_daemon_write: false }
const configurationState: CapabilitySettingsState = { effective: configuration, saved: configuration, pending_restart: false, daemon_toggle_supported: true }
interface FixtureOptions { capabilities?: Record<string, Capability>; settings?: Record<string, unknown>; respond?: (url: string, options?: RequestInit) => Promise<Response | undefined> }
function fixture(options: FixtureOptions = {}) {
  const fetch = vi.fn(async (input: string, init?: RequestInit): Promise<Response> => {
    const url = String(input)
    const custom = await options.respond?.(url, init)
    if (custom) return custom
    let data: unknown = []
    if (url.endsWith('/settings')) data = { mode: 'binary', version: appVersion, allowed_paths: null, path_mappings: null, capability_settings: configurationState, ...options.settings }
    if (url.endsWith('/capabilities')) data = { capabilities: options.capabilities ?? capabilities }
    if (url.endsWith('/host/daemon')) data = { available: false, reason: 'raw backend diagnosis', manual_guide: 'raw English guide' }
    if (url.endsWith('/host/daemon/preview')) data = { revision: 'fixture-revision', configuration: { 'registry-mirrors': ['https://registry.example.com'] } }
    if (url.endsWith('/host/diagnostics')) data = { network: { available: true }, registry_api: { available: true, status_code: 200 }, image_pull: { tested: false, task_id: 'pull-task-fixture' } }
    return new Response(JSON.stringify(data), { status: 200 })
  })
  vi.stubGlobal('fetch', fetch)
  return fetch
}
function render() { const wrapper = mount(SettingsView, { attachTo: document.body }); wrappers.push(wrapper); return wrapper }
function button(wrapper: VueWrapper, text: string) { const found = wrapper.findAll('button').find(el => el.text() === text); if (!found) throw new Error(`Missing button: ${text}`); return found }
async function tab(wrapper: VueWrapper, value: string) { await wrapper.get(`#settings-tab-${value}`).trigger('click'); await flushPromises() }
async function closeDrawer(wrapper: VueWrapper) { wrapper.findComponent({ name: 'NDrawer' }).vm.$emit('update:show', false); await flushPromises() }
const enabled = { ...capabilities, 'host.daemon_config': { available: true }, 'host.service_restart': { available: true } }

describe('Settings layout and capability details', () => {
  it('defaults to a six-row overview with localized categories and lazy audit loading', async () => {
    const fetch = fixture()
    const wrapper = render(); await flushPromises()
    expect(wrapper.findAll('[role="tab"]')).toHaveLength(3)
    expect(wrapper.get('#settings-tab-overview').attributes('aria-selected')).toBe('true')
    expect(wrapper.get('#settings-panel-overview').isVisible()).toBe(true)
    expect(wrapper.get('#settings-panel-diagnostics').isVisible()).toBe(false)
    expect(wrapper.findAll('[id^="capability-"]')).toHaveLength(6)
    expect(wrapper.get('[id="capability-backup.local"] .tag').text()).toBe('缺少工具')
    expect(wrapper.get('[id="capability-backup.bind_mount"] .tag').text()).toBe('未配置')
    expect(wrapper.get('[id="capability-host.daemon_config"] .tag').text()).toBe('未授权')
    expect(wrapper.get('[id="capability-docker.manage"] .capability-short-reason').text()).not.toContain('当前检测已通过')
    expect(wrapper.find('.capability-help').exists()).toBe(false)
    expect(fetch.mock.calls.some(([url]) => url.includes('/audit?'))).toBe(false)
    expect(wrapper.findAll('button').filter(el => el.text() === '配置功能')).toHaveLength(1)
    setLocale('en', false); await wrapper.vm.$nextTick()
    expect(wrapper.get('[id="capability-backup.local"] .tag').text()).toBe('Missing tool')
    expect(wrapper.get('[id="capability-host.daemon_config"] .tag').text()).toBe('Not authorized')
  })

  it('opens localized details with the raw diagnosis collapsed and no resource actions', async () => {
    const fetch = fixture()
    const wrapper = render(); await flushPromises()
    await wrapper.get('[id="capability-backup.local"] .capability-details-button').trigger('click')
    const drawer = wrapper.get('.test-drawer')
    expect(drawer.text()).toContain('DM_RESTIC_PATH')
    expect(drawer.text()).toContain('本地备份与恢复')
    expect(drawer.find('.capability-technical').attributes('open')).toBeUndefined()
    expect(drawer.find('.capability-technical').text()).toContain('RESTIC_MISSING')
    expect(drawer.find('.capability-technical').text()).toContain('Restic is not installed')
    expect(fetch.mock.calls.every(([, init]) => !init?.method)).toBe(true)
    await closeDrawer(wrapper)
    expect(wrapper.find('.test-drawer').exists()).toBe(false)
    expect(document.activeElement).toBe(wrapper.get('[id="capability-backup.local"] .capability-details-button').element)
  })

  it('previews at most two directory entries and exposes the complete list read-only', async () => {
    const paths = ['/srv/one', '/srv/two', '/srv/three', '/srv/four']
    fixture({ settings: { allowed_paths: paths, path_mappings: paths.map(path => ({ host: path, local: `/host${path}` })) } })
    const wrapper = render(); await flushPromises()
    const overview = wrapper.get('#settings-panel-overview')
    expect(overview.text()).toContain('/srv/one')
    expect(overview.text()).toContain('/srv/two')
    expect(overview.text()).not.toContain('/srv/three')
    await wrapper.findAll('.settings-path-details')[0]!.trigger('click')
    expect(wrapper.get('.test-drawer').text()).toContain('/srv/four')
    expect(wrapper.get('.test-drawer').findAll('input,textarea')).toHaveLength(0)
    await closeDrawer(wrapper)
    await wrapper.findAll('.settings-path-details')[1]!.trigger('click')
    expect(wrapper.get('.test-drawer').text()).toContain('/host/srv/four')
  })

  it('closes capability details before opening the editor and preserves pending notices across tabs', async () => {
    fixture({ settings: { capability_settings: { ...configurationState, pending_restart: true } } })
    const wrapper = render(); await flushPromises()
    await wrapper.get('[id="capability-backup.bind_mount"] .capability-details-button').trigger('click')
    const configure = wrapper.get('.test-drawer').findAll('button').find(el => el.text() === '配置功能')!
    await configure.trigger('click'); await flushPromises()
    expect(wrapper.find('.test-drawer').exists()).toBe(false)
    expect(wrapper.find('.test-editor').exists()).toBe(true)
    await button(wrapper, 'Close editor').trigger('click'); await flushPromises()
    expect(document.activeElement).toBe(wrapper.get('#settings-configure').element)
    const pending = wrapper.find('[role="status"]').text()
    await tab(wrapper, 'diagnostics'); await tab(wrapper, 'audit'); await tab(wrapper, 'overview')
    expect(wrapper.find('[role="status"]').text()).toBe(pending)
    const editor = wrapper.findComponent({ name: 'CapabilitySettingsEditor' })
    editor.vm.$emit('saved', { mode: 'binary', allowed_paths: ['/saved'], capability_settings: { ...configurationState, pending_restart: true } })
    await wrapper.vm.$nextTick()
    const saved = wrapper.find('[role="status"]').text()
    await tab(wrapper, 'diagnostics'); await tab(wrapper, 'overview')
    expect(wrapper.find('[role="status"]').text()).toBe(saved)
    expect(wrapper.get('#settings-panel-overview').text()).toContain('/saved')
  })

  it('preserves newly saved settings when an older refresh completes afterward', async () => {
    let settingsReads = 0
    let resolveOldSettings!: (response: Response) => void
    fixture({ respond: url => {
      if (url.endsWith('/settings') && ++settingsReads > 1) return new Promise<Response>(resolve => { resolveOldSettings = resolve })
      return Promise.resolve(undefined)
    } })
    const wrapper = render(); await flushPromises()
    await button(wrapper, '重新检测').trigger('click'); await flushPromises()
    expect(button(wrapper, '重新检测').attributes('disabled')).toBeDefined()
    const savedConfiguration = { ...configuration, allowed_paths: ['/srv/newly-saved'] }
    const savedState = { ...configurationState, saved: savedConfiguration, pending_restart: true }
    const editor = wrapper.findComponent({ name: 'CapabilitySettingsEditor' })
    editor.vm.$emit('saved', { mode: 'binary', allowed_paths: ['/srv/newly-saved'], capability_settings: savedState })
    await flushPromises()
    expect(button(wrapper, '重新检测').attributes('disabled')).toBeUndefined()
    const savedNotice = wrapper.get('.settings-pending').text()
    resolveOldSettings(new Response(JSON.stringify({ mode: 'binary', allowed_paths: ['/srv/old-before-save'], capability_settings: configurationState })))
    await flushPromises()
    expect(wrapper.get('#settings-panel-overview').text()).toContain('/srv/newly-saved')
    expect(wrapper.get('#settings-panel-overview').text()).not.toContain('/srv/old-before-save')
    expect(wrapper.get('.settings-pending').text()).toBe(savedNotice)
    expect(editor.props('state')).toEqual(savedState)
  })

  it('supports arrow, Home, and End navigation with one tabbable selected tab', async () => {
    fixture(); const wrapper = render(); await flushPromises()
    wrapper.get<HTMLButtonElement>('#settings-tab-overview').element.focus()
    await wrapper.get('#settings-tab-overview').trigger('keydown', { key: 'ArrowRight' }); await flushPromises()
    expect(wrapper.get('#settings-tab-diagnostics').attributes('aria-selected')).toBe('true')
    expect(document.activeElement).toBe(wrapper.get('#settings-tab-diagnostics').element)
    await wrapper.get('#settings-tab-diagnostics').trigger('keydown', { key: 'End' }); await flushPromises()
    expect(wrapper.get('#settings-tab-audit').attributes('tabindex')).toBe('0')
    await wrapper.get('#settings-tab-audit').trigger('keydown', { key: 'Home' }); await flushPromises()
    expect(wrapper.get('#settings-tab-overview').attributes('tabindex')).toBe('0')
    expect(wrapper.findAll('[role="tab"][tabindex="0"]')).toHaveLength(1)
  })
})

describe('Settings diagnostics and permission gates', () => {
  it('keeps writes disabled when only one backend permission is available', async () => {
    fixture({ capabilities: { ...enabled, 'host.service_restart': { available: false, reason_code: 'HOST_TOOL_MISSING', reason_params: { tool: 'systemctl' } } } })
    const wrapper = render(); await flushPromises(); await tab(wrapper, 'diagnostics')
    expect(button(wrapper, '预览配置修改').attributes('disabled')).toBeDefined()
    expect(button(wrapper, '应用并重启 Docker').attributes('disabled')).toBeDefined()
    await button(wrapper, '读取配置 / 操作指引').trigger('click'); await flushPromises()
    expect(wrapper.get('.test-drawer').text()).toContain('手工操作')
    expect(wrapper.get('.test-drawer .capability-technical').attributes('open')).toBeUndefined()
    expect(wrapper.get('.test-drawer .capability-technical').text()).toContain('raw English guide')
    expect(wrapper.get('.settings-result-summary').text()).toContain('仅提供宿主机手工操作指引')
    expect(wrapper.get('.settings-result-summary').text()).not.toContain('已读取当前 Docker 配置')
  })


  it.each([
    { network: false, registry: false, task: '', expected: '网络：失败 · Registry API：失败 · 镜像拉取未测试' },
    { network: true, registry: false, task: 'pull-pending-api-failed', expected: '网络：通过 · Registry API：失败 · 拉取任务已提交' },
    { network: true, registry: true, task: 'pull-still-pending', expected: '网络：通过 · Registry API：通过 · 拉取任务已提交' },
  ])('summarizes real diagnostic layers without claiming queued pulls succeeded: $expected', async ({ network, registry, task, expected }) => {
    fixture({ respond: url => Promise.resolve(url.endsWith('/host/diagnostics') ? new Response(JSON.stringify({
      network: { available: network }, registry_api: { available: registry }, image_pull: { tested: false, ...(task ? { task_id: task } : {}) },
    })) : undefined) })
    const wrapper = render(); await flushPromises(); await tab(wrapper, 'diagnostics')
    await wrapper.get('#diagnostic-mirror').setValue('https://registry.example.com')
    if (task) await wrapper.get('#diagnostic-image').setValue('alpine:latest')
    await button(wrapper, '执行分层诊断').trigger('click'); await flushPromises()
    expect(wrapper.get('.test-drawer').text()).toContain('"tested": false')
    await closeDrawer(wrapper)
    const summary = wrapper.get('.settings-result-summary')
    expect(summary.text()).toContain(expected)
    expect(summary.text()).not.toContain('成功')
    if (task) {
      setLocale('en', false); await wrapper.vm.$nextTick()
      expect(summary.text()).toContain('Pull task submitted')
      expect(summary.text()).not.toMatch(/pull.*success/i)
      expect(summary.text()).toContain(`Registry API: ${registry ? 'Passed' : 'Failed'}`)
    }
  })

  it('retains diagnostic inputs and preview across tabs, and invalidates preview when mirrors change', async () => {
    const fetch = fixture({ capabilities: enabled })
    const wrapper = render(); await flushPromises(); await tab(wrapper, 'diagnostics')
    await wrapper.get('#diagnostic-mirror').setValue('https://registry.example.com')
    await wrapper.get('#diagnostic-image').setValue('alpine:latest')
    expect(button(wrapper, '应用并重启 Docker').attributes('disabled')).toBeDefined()
    await button(wrapper, '预览配置修改').trigger('click'); await flushPromises()
    expect(wrapper.get('.test-drawer').text()).toContain('fixture-revision')
    await closeDrawer(wrapper)
    expect(wrapper.find('.settings-result-summary').exists()).toBe(true)
    expect(button(wrapper, '应用并重启 Docker').attributes('disabled')).toBeUndefined()
    await tab(wrapper, 'overview'); await tab(wrapper, 'audit'); await tab(wrapper, 'diagnostics')
    expect(wrapper.get<HTMLTextAreaElement>('#diagnostic-mirror').element.value).toBe('https://registry.example.com')
    expect(wrapper.get<HTMLInputElement>('#diagnostic-image').element.value).toBe('alpine:latest')
    expect(button(wrapper, '应用并重启 Docker').attributes('disabled')).toBeUndefined()
    await button(wrapper, '应用并重启 Docker').trigger('click'); await flushPromises()
    expect(wrapper.get('.test-modal button:last-child').attributes('disabled')).toBeDefined()
    expect(fetch.mock.calls.some(([url]) => url.endsWith('/host/daemon/apply'))).toBe(false)
    await button(wrapper, '取消').trigger('click')
    await wrapper.get('#diagnostic-mirror').setValue('https://changed.example.com')
    expect(button(wrapper, '应用并重启 Docker').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('.settings-result-summary').some(el => el.text().includes('fixture-revision'))).toBe(false)
  })

  it('submits an acknowledged preview using its revision and the existing restart confirmations', async () => {
    const fetch = fixture({ capabilities: enabled })
    const wrapper = render(); await flushPromises(); await tab(wrapper, 'diagnostics')
    await wrapper.get('#diagnostic-mirror').setValue('https://registry.example.com')
    await button(wrapper, '预览配置修改').trigger('click'); await flushPromises(); await closeDrawer(wrapper)
    await button(wrapper, '应用并重启 Docker').trigger('click'); await flushPromises()
    await wrapper.get('.test-modal input[type="checkbox"]').setValue(true)
    await button(wrapper, '确认执行').trigger('click'); await flushPromises()
    const request = fetch.mock.calls.find(([url]) => url.endsWith('/host/daemon/apply'))
    expect(request).toBeDefined()
    expect(JSON.parse(String(request![1]?.body))).toEqual({ mirrors: ['https://registry.example.com'], revision: 'fixture-revision', confirm: true, confirm_restart: true })
    expect(wrapper.find('.test-modal').exists()).toBe(false)
    expect(button(wrapper, '应用并重启 Docker').attributes('disabled')).toBeDefined()
  })

  it('ignores an in-flight preview after the mirror input changes', async () => {
    let resolvePreview!: (response: Response) => void
    fixture({ capabilities: enabled, respond: url => url.endsWith('/host/daemon/preview') ? new Promise<Response>(resolve => { resolvePreview = resolve }) : Promise.resolve(undefined) })
    const wrapper = render(); await flushPromises(); await tab(wrapper, 'diagnostics')
    await wrapper.get('#diagnostic-mirror').setValue('https://first.example.com')
    await button(wrapper, '预览配置修改').trigger('click'); await flushPromises()
    expect(button(wrapper, '预览配置修改').attributes('disabled')).toBeDefined()
    await wrapper.get('#diagnostic-mirror').setValue('https://second.example.com')
    resolvePreview(new Response(JSON.stringify({ revision: 'outdated' })))
    await flushPromises()
    expect(wrapper.find('.test-drawer').exists()).toBe(false)
    expect(button(wrapper, '应用并重启 Docker').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('.settings-result-summary')).toHaveLength(0)
  })

  it('keeps the latest audit response when tab revisits overlap in-flight requests', async () => {
    const pending: Array<(response: Response) => void> = []
    fixture({ respond: url => url.includes('/audit?') ? new Promise<Response>(resolve => pending.push(resolve)) : Promise.resolve(undefined) })
    const wrapper = render(); await flushPromises(); await tab(wrapper, 'audit')
    expect(wrapper.get('#settings-panel-audit [role="status"]').text()).toContain('读取')
    expect(button(wrapper, '下一页').attributes('disabled')).toBeDefined()
    await tab(wrapper, 'overview'); await tab(wrapper, 'audit')
    expect(pending).toHaveLength(2)
    pending[1]!(new Response(JSON.stringify([{ id: 'latest', actor_id: 'current-actor', action: 'latest' }])))
    await flushPromises()
    pending[0]!(new Response(JSON.stringify([{ id: 'stale', actor_id: 'old-actor', action: 'obsolete' }])))
    await flushPromises()
    expect(wrapper.get('#settings-panel-audit').text()).toContain('current-actor')
    expect(wrapper.get('#settings-panel-audit').text()).not.toContain('old-actor')
    expect(wrapper.find('#settings-panel-audit [role="status"]').exists()).toBe(false)
  })

  it('refreshes dirty audit after a save even when an older audit response arrives later', async () => {
    let auditReads = 0
    let resolveOldAudit!: (response: Response) => void
    fixture({ respond: url => {
      if (!url.includes('/audit?')) return Promise.resolve(undefined)
      auditReads++
      if (auditReads === 1) return new Promise<Response>(resolve => { resolveOldAudit = resolve })
      return Promise.resolve(new Response(JSON.stringify([{ id: 'after-save', actor_id: 'saved-config-actor', action: 'settings.capabilities.update' }])))
    } })
    const wrapper = render(); await flushPromises(); await tab(wrapper, 'audit')
    expect(wrapper.find('#settings-panel-audit [role="status"]').exists()).toBe(true)
    await tab(wrapper, 'overview')
    wrapper.findComponent({ name: 'CapabilitySettingsEditor' }).vm.$emit('saved', { mode: 'binary', capability_settings: { ...configurationState, pending_restart: true } })
    await flushPromises()
    expect(auditReads).toBe(1)
    resolveOldAudit(new Response(JSON.stringify([{ id: 'before-save', actor_id: 'outdated-config-actor', action: 'old.action' }])))
    await flushPromises()
    expect(wrapper.get('#settings-panel-audit').text()).not.toContain('outdated-config-actor')
    await tab(wrapper, 'audit')
    expect(auditReads).toBe(2)
    expect(wrapper.get('#settings-panel-audit').text()).toContain('saved-config-actor')
    expect(wrapper.get('#settings-panel-audit').text()).not.toContain('outdated-config-actor')
    expect(wrapper.find('#settings-panel-audit [role="status"]').exists()).toBe(false)
  })

  it('loads audit on first visit only and keeps its errors separate from overview refresh', async () => {
    let fail = true
    const fetch = fixture({ respond: async url => url.includes('/audit?') && fail ? new Response(JSON.stringify({ error: { code: 'REQUEST_FAILED', message: 'audit temporarily unavailable' } }), { status: 503 }) : undefined })
    const wrapper = render(); await flushPromises()
    expect(fetch.mock.calls.some(([url]) => url.includes('/audit?'))).toBe(false)
    await tab(wrapper, 'audit')
    expect(wrapper.get('#settings-panel-audit').text()).toContain('audit temporarily unavailable')
    fail = false
    await tab(wrapper, 'overview')
    expect(wrapper.get('#settings-panel-overview').text()).not.toContain('audit temporarily unavailable')
    await button(wrapper, '重新检测').trigger('click'); await flushPromises()
    expect(fetch.mock.calls.filter(([url]) => url.includes('/audit?'))).toHaveLength(1)
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
