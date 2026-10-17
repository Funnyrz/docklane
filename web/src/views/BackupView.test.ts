import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../i18n'
vi.mock('naive-ui', async () => {
  const { defineComponent } = await import('vue')
  return {
    NButton: defineComponent({ props: ['disabled', 'loading', 'attrType'], template: '<button :type="attrType || \'button\'" :disabled="disabled || loading"><slot/></button>' }),
    NDrawer: defineComponent({ props: ['show'], template: '<section v-if="show"><slot/></section>' }),
    NDrawerContent: defineComponent({ props: ['title'], template: '<section><h2>{{title}}</h2><slot/></section>' }),
    NModal: defineComponent({ props: ['show', 'title'], template: '<section v-if="show"><h2>{{title}}</h2><slot/><slot name="footer"/></section>' }),
    NInput: defineComponent({ props: ['value', 'inputProps', 'disabled', 'readonly', 'type', 'placeholder'], emits: ['update:value'], template: '<textarea v-if="type === \'textarea\'" v-bind="inputProps" :disabled="disabled" :readonly="readonly" :value="value" @input="$emit(\'update:value\',$event.target.value)"/><input v-else v-bind="inputProps" :disabled="disabled" :readonly="readonly" :value="value" :placeholder="placeholder" @input="$emit(\'update:value\',$event.target.value)"/>' }),
    NInputNumber: defineComponent({ props: ['value', 'inputProps'], emits: ['update:value'], template: '<input type="number" v-bind="inputProps" :value="value" @input="$emit(\'update:value\',Number($event.target.value))"/>' }),
    NSelect: defineComponent({ props: ['value', 'inputProps', 'options', 'disabled'], emits: ['update:value'], template: '<select v-bind="inputProps" :value="value" :disabled="disabled" @change="$emit(\'update:value\',$event.target.value)"><option value="">请选择</option><option v-for="o in options" :key="o.value" :value="o.value" :disabled="o.disabled">{{o.label}}</option></select>' }),
    NCheckbox: defineComponent({ props: ['checked', 'disabled'], emits: ['update:checked'], template: '<label><input type="checkbox" :checked="checked" :disabled="disabled" @change="$emit(\'update:checked\',$event.target.checked)"/><slot/></label>' }),
  }
})
import BackupView from './BackupView.vue'
interface Call { url: string; method: string; body?: Record<string, unknown> }
const calls: Call[] = []
let metadataVerified = true
const policy = { id: 'policy-1', name: 'nightly', repository_id: 'repo-1', targets: [{ type: 'directory', path: '/srv/one' }, { type: 'volume', name: 'data-volume' }], consistency_mode: 'stop', schedule: '0 3 * * *', timezone: 'Asia/Shanghai', keep_last: 7, retries: 1, retry_seconds: 30, excludes: ['*.tmp'], enabled: true }
beforeEach(() => {
  calls.length = 0; metadataVerified = true
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    const call = { url, method: init.method ?? 'GET', body: init.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined }; calls.push(call)
    let data: unknown = {}
    if (url.endsWith('/backup/repositories')) data = [{ id: 'repo-1', name: 'local', location: '/backups/restic' }]
    else if (url.endsWith('/backup/policies')) data = [policy]
    else if (url.endsWith('/backup/schedule-instances')) data = []
    else if (url.endsWith('/snapshots')) data = [{ id: 'snapshot-123', verification_status: metadataVerified ? 'verified' : 'unverified', time: '2026-10-16T01:00:00Z' }]
    else if (url.endsWith('/metadata')) data = { verification_status: metadataVerified ? 'verified' : 'unverified', verification_warning: metadataVerified ? '' : 'Backup was not fully verified; files may be missing', sources: [{ snapshot_path: '/sources/000', host_path: '/srv/one' }, { snapshot_path: '/sources/001', host_path: '/srv/two' }], containers: [{ id: 'container-1', name: 'api' }, { id: 'container-compose', name: 'web-1', compose_project: 'web' }], projects: [{ id: 'project-1', name: 'web' }] }
    else if (url.endsWith('/preview')) data = { revision: 'verified-revision', sources: [], stop_containers: [] }
    return new Response(JSON.stringify(data), { status: 200 })
  }))
})
afterEach(() => vi.unstubAllGlobals())
function button(wrapper: VueWrapper, text: string) { const found = wrapper.findAll('button').find(b => b.text() === text); if (!found) throw new Error(`button missing: ${text}`); return found }
function checkbox(wrapper: VueWrapper, text: string) { const label = wrapper.findAll('label').find(l => l.text().includes(text)); if (!label) throw new Error(`checkbox missing: ${text}`); return label.find('input[type=checkbox]') }
async function openRestore(wrapper: VueWrapper) { await flushPromises(); await button(wrapper, '浏览快照 / 恢复 ↗').trigger('click'); await flushPromises(); await button(wrapper, metadataVerified ? '恢复此快照 →' : '检查未验证快照 →').trigger('click'); await flushPromises() }
function request(suffix: string, method = 'POST') { return calls.filter(c => c.url.endsWith(suffix) && c.method === method).at(-1) }

describe('backup recovery and policy management', () => {
  it('selects one original source before preview and requires acknowledgement before overwrite', async () => {
    const wrapper = mount(BackupView); await openRestore(wrapper)
    await wrapper.find('#restore-mode').setValue('original')
    expect(button(wrapper, '检查并预览覆盖范围').attributes('disabled')).toBeDefined()
    await wrapper.find('#restore-source').setValue('/sources/001')
    expect((wrapper.find('#restore-target').element as HTMLInputElement).value).toBe('/srv/two')
    expect(wrapper.find('#restore-target').attributes('readonly')).toBeDefined()
    await button(wrapper, '检查并预览覆盖范围').trigger('click'); await flushPromises()
    expect(request('/backup/restore/preview')?.body).toMatchObject({ source: '/sources/001', target: '/srv/two', overwrite: true, recreate: false })
    await button(wrapper, '继续确认').trigger('click')
    expect(button(wrapper, '确认执行').attributes('disabled')).toBeDefined(); expect(request('/backup/restore')).toBeUndefined()
    await checkbox(wrapper, '我已检查影响范围').setValue(true); await button(wrapper, '确认执行').trigger('click'); await flushPromises()
    expect(request('/backup/restore')?.body).toMatchObject({ source: '/sources/001', target: '/srv/two', confirm_overwrite: '/srv/two', revision: 'verified-revision', confirm: true })
    wrapper.unmount()
  })
  it('sends selected service name mappings and invalidates preview when inputs change', async () => {
    const wrapper = mount(BackupView); await openRestore(wrapper)
    await wrapper.find('#restore-target').setValue('/restore/isolated')
    await checkbox(wrapper, '同时选择性重建服务').setValue(true)
    await wrapper.find('#container-name-container-1').setValue('api-recovered')
    await wrapper.find('#project-name-project-1').setValue('web-recovered')
    await button(wrapper, '检查并预览覆盖范围').trigger('click'); await flushPromises()
    expect(request('/backup/restore/preview')?.body).toMatchObject({ overwrite: false, recreate: true, container_ids: ['container-1'], project_ids: ['project-1'], container_names: { 'container-1': 'api-recovered' }, project_names: { 'project-1': 'web-recovered' } })
    await wrapper.find('#container-name-container-1').setValue('api-restored-again')
    expect(button(wrapper, '继续确认').attributes('disabled')).toBeDefined()
    await button(wrapper, '检查并预览覆盖范围').trigger('click'); await flushPromises()
    await button(wrapper, '继续确认').trigger('click'); await checkbox(wrapper, '我已检查影响范围').setValue(true); await button(wrapper, '确认执行').trigger('click'); await flushPromises()
    expect(request('/backup/restore')?.body).toMatchObject({ container_names: { 'container-1': 'api-restored-again' }, project_names: { 'project-1': 'web-recovered' }, revision: 'verified-revision' })
    wrapper.unmount()
  })
  it('edits a multi-target policy without dropping other targets or retry settings', async () => {
    const wrapper = mount(BackupView); await flushPromises(); await button(wrapper, '编辑').trigger('click')
    await wrapper.find('#policy-name').setValue('nightly-revised'); await wrapper.find('#target-value-0').setValue('/srv/revised'); await wrapper.find('#policy-keep').setValue(10)
    await wrapper.find('form').trigger('submit'); await flushPromises()
    expect(request('/backup/policies/policy-1', 'PUT')?.body).toMatchObject({ id: 'policy-1', name: 'nightly-revised', targets: [{ type: 'directory', path: '/srv/revised' }, { type: 'volume', name: 'data-volume' }], retry_seconds: 30, retries: 1, keep_last: 10, excludes: ['*.tmp'], enabled: true })
    wrapper.unmount()
  })
  it('deletes only after confirmation with server-required confirm body', async () => {
    const wrapper = mount(BackupView); await flushPromises(); await button(wrapper, '删除').trigger('click')
    expect(request('/backup/policies/policy-1', 'DELETE')).toBeUndefined()
    await checkbox(wrapper, '我已检查影响范围').setValue(true); await button(wrapper, '确认执行').trigger('click'); await flushPromises()
    expect(request('/backup/policies/policy-1', 'DELETE')?.body).toEqual({ confirm: true })
    wrapper.unmount()
  })
  it('requires separate rescue acknowledgement and forbids overwrite or service recreation for unverified snapshots', async () => {
    metadataVerified = false
    const wrapper = mount(BackupView); await openRestore(wrapper)
    await wrapper.find('#restore-target').setValue('/restore/rescue')
    expect(wrapper.text()).toContain('未完成验证')
    expect(button(wrapper, '检查并预览覆盖范围').attributes('disabled')).toBeDefined()
    expect(checkbox(wrapper, '同时选择性重建服务').attributes('disabled')).toBeDefined()
    expect(wrapper.find('#restore-mode option[value="original"]').attributes('disabled')).toBeDefined()
    await checkbox(wrapper, '我了解快照未完成验证，仅救援到新目录').setValue(true)
    await button(wrapper, '检查并预览覆盖范围').trigger('click'); await flushPromises()
    expect(request('/backup/restore/preview')?.body).toMatchObject({ acknowledge_unverified: true, overwrite: false, recreate: false })
    await button(wrapper, '继续确认').trigger('click')
    expect(wrapper.text()).toContain('未验证快照可能缺少文件')
    expect(button(wrapper, '确认执行').attributes('disabled')).toBeDefined()
    await checkbox(wrapper, '我已检查影响范围').setValue(true); await button(wrapper, '确认执行').trigger('click'); await flushPromises()
    expect(request('/backup/restore')?.body).toMatchObject({ acknowledge_unverified: true, overwrite: false, recreate: false, confirm: true })
    wrapper.unmount()
  })

  it('preserves unverified rescue restrictions, inputs and confirmation requirements when translating', async () => {
    metadataVerified = false
    const wrapper = mount(BackupView); await openRestore(wrapper)
    await wrapper.find('#restore-target').setValue('/restore/原始救援目录')
    const requests = calls.length

    setLocale('en', false); await flushPromises()

    expect((wrapper.find('#restore-target').element as HTMLInputElement).value).toBe('/restore/原始救援目录')
    expect(wrapper.text()).toContain('Snapshot not fully verified')
    expect(wrapper.text()).toContain('Backup was not fully verified; files may be missing')
    expect(calls).toHaveLength(requests)
    expect(button(wrapper, 'Check and preview affected data').attributes('disabled')).toBeDefined()
    expect(button(wrapper, 'Continue to confirmation').attributes('disabled')).toBeDefined()
    expect(checkbox(wrapper, 'Selectively recreate services').attributes('disabled')).toBeDefined()
    expect(wrapper.find('#restore-mode option[value="original"]').attributes('disabled')).toBeDefined()
    expect(request('/backup/restore/preview')).toBeUndefined()
    await checkbox(wrapper, 'I understand this snapshot is unverified').setValue(true)
    await button(wrapper, 'Check and preview affected data').trigger('click'); await flushPromises()
    expect(request('/backup/restore/preview')?.body).toMatchObject({ target: '/restore/原始救援目录', acknowledge_unverified: true, overwrite: false, recreate: false })
    await button(wrapper, 'Continue to confirmation').trigger('click')
    expect(wrapper.text()).toContain('unverified snapshots may be missing files')
    expect(button(wrapper, 'Confirm action').attributes('disabled')).toBeDefined()
    const beforeConfirmation = calls.length

    setLocale('zh-CN', false); await flushPromises()

    expect((wrapper.find('#restore-target').element as HTMLInputElement).value).toBe('/restore/原始救援目录')
    expect(wrapper.text()).toContain('未验证快照可能缺少文件')
    expect(button(wrapper, '确认执行').attributes('disabled')).toBeDefined()
    expect(checkbox(wrapper, '同时选择性重建服务').attributes('disabled')).toBeDefined()
    expect(wrapper.find('#restore-mode option[value="original"]').attributes('disabled')).toBeDefined()
    expect(calls).toHaveLength(beforeConfirmation); expect(request('/backup/restore')).toBeUndefined()
    await checkbox(wrapper, '我已检查影响范围').setValue(true)
    await button(wrapper, '确认执行').trigger('click'); await flushPromises()
    expect(request('/backup/restore')?.body).toMatchObject({ target: '/restore/原始救援目录', acknowledge_unverified: true, overwrite: false, recreate: false, confirm: true })
    wrapper.unmount()
  })

})
