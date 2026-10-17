import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../i18n'
vi.mock('naive-ui', async () => {
  const { defineComponent } = await import('vue')
  return {
    NButton: defineComponent({ props: ['disabled', 'loading', 'attrType'], template: '<button :type="attrType || \'button\'" :disabled="disabled || loading"><slot/></button>' }),
    NDrawer: defineComponent({ props: ['show'], template: '<section v-if="show" class="drawer"><slot/></section>' }),
    NDrawerContent: defineComponent({ props: ['title'], template: '<section><h2>{{title}}</h2><slot/></section>' }),
    NModal: defineComponent({ props: ['show', 'title'], template: '<section v-if="show" class="modal"><h2>{{title}}</h2><slot/><slot name="footer"/></section>' }),
    NInput: defineComponent({ props: ['value', 'inputProps', 'disabled', 'type'], emits: ['update:value'], template: '<textarea v-if="type === \'textarea\'" v-bind="inputProps" :disabled="disabled" :value="value" @input="$emit(\'update:value\',$event.target.value)"/><input v-else v-bind="inputProps" :value="value" @input="$emit(\'update:value\',$event.target.value)"/>' }),
    NSelect: defineComponent({ template: '<select/>' }),
    NCheckbox: defineComponent({ props: ['checked'], emits: ['update:checked'], template: '<label><input type="checkbox" :checked="checked" @change="$emit(\'update:checked\',$event.target.checked)"/><slot/></label>' }),
  }
})
import ComposeView from './ComposeView.vue'
const calls: { url: string; method: string; body?: Record<string, unknown> }[] = []
let savedContent: string, revision: string, failPreview: boolean, failDeploy: boolean
function project() { return { id: 'project-1', name: 'web', source_type: 'local', workdir: '/srv/web', config_paths: ['/srv/web/compose.yaml'], revision } }
beforeEach(() => {
  calls.length = 0; savedContent = 'services:\n  web:\n    image: nginx:1.27'; revision = 'revision-one'; failPreview = false; failDeploy = false
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    const method = init.method ?? 'GET', body = init.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined; calls.push({ url, method, body })
    let data: unknown = {}, status = 200
    if (url.endsWith('/compose/projects')) data = [project()]
    else if (url.endsWith('/compose/discover')) data = []
    else if (url.endsWith('/files') && method === 'PUT') { savedContent = (body?.files as { content: string }[])[0]!.content; revision = 'revision-two'; data = project() }
    else if (url.endsWith('/preview')) { if (failPreview) { data = { error: { code: 'INVALID_COMPOSE', message: 'YAML validation failed' } }; status = 400 } else data = { revision, configuration: { savedContent } } }
    else if (url.endsWith('/deploy') && failDeploy) { data = { error: { code: 'CHANGED', message: 'Configuration changed; preview again' } }; status = 400 }
    else if (url.endsWith('/compose/projects/project-1')) data = { project: project(), files: [{ path: '/srv/web/compose.yaml', content: savedContent }] }
    return new Response(JSON.stringify(data), { status })
  }))
})
afterEach(() => vi.unstubAllGlobals())
function button(wrapper: VueWrapper, text: string) { const result = wrapper.findAll('button').find(item => item.text() === text); if (!result) throw new Error(`missing button ${text}`); return result }
async function openProject(wrapper: VueWrapper) { await flushPromises(); await button(wrapper, '管理项目 ↗').trigger('click'); await flushPromises() }
describe('Compose draft safety', () => {
  it('requires saving a changed draft before previewing and deploying its new revision', async () => {
    const wrapper = mount(ComposeView); await openProject(wrapper)
    await wrapper.find('textarea').setValue('services:\n  web:\n    image: nginx:1.28')
    expect(wrapper.text()).toContain('有未保存的配置草稿')
    expect(button(wrapper, '校验并预览').attributes('disabled')).toBeDefined(); expect(button(wrapper, '部署项目').attributes('disabled')).toBeDefined()
    await button(wrapper, '校验并预览').trigger('click'); expect(calls.some(c => c.url.endsWith('/preview'))).toBe(false)
    await button(wrapper, '保存文件').trigger('click'); await flushPromises()
    expect(calls.find(c => c.method === 'PUT')?.body).toMatchObject({ revision: 'revision-one', files: [{ content: 'services:\n  web:\n    image: nginx:1.28' }] })
    expect(button(wrapper, '校验并预览').attributes('disabled')).toBeUndefined(); expect(button(wrapper, '部署项目').attributes('disabled')).toBeDefined()
    await button(wrapper, '校验并预览').trigger('click'); await flushPromises(); await button(wrapper, '部署项目').trigger('click')
    await wrapper.find('.modal input[type=checkbox]').setValue(true); await button(wrapper, '确认执行').trigger('click'); await flushPromises()
    expect(calls.find(c => c.url.endsWith('/deploy'))?.body).toEqual({ confirm: true, revision: 'revision-two' })
    wrapper.unmount()
  })
  it('shows validation and execution errors within the active drawer and modal', async () => {
    const wrapper = mount(ComposeView); await openProject(wrapper); failPreview = true
    await button(wrapper, '校验并预览').trigger('click'); await flushPromises()
    expect(wrapper.find('.drawer [role=alert]').text()).toContain('YAML validation failed')
    failPreview = false; await button(wrapper, '校验并预览').trigger('click'); await flushPromises()
    await button(wrapper, '部署项目').trigger('click'); await wrapper.find('.modal input[type=checkbox]').setValue(true); failDeploy = true
    await button(wrapper, '确认执行').trigger('click'); await flushPromises()
    expect(wrapper.find('.modal [role=alert]').text()).toContain('Configuration changed; preview again')
    wrapper.unmount()
  })
  it('preserves dirty files and deployment confirmation gates across language changes', async () => {
    const wrapper = mount(ComposeView); await openProject(wrapper)
    await button(wrapper, '校验并预览').trigger('click'); await flushPromises()
    expect(button(wrapper, '部署项目').attributes('disabled')).toBeUndefined()
    const draft = 'services:\n  web:\n    image: example/app:latest\n    environment:\n      MESSAGE: 原始配置'
    await wrapper.find('textarea').setValue(draft)
    const requests = calls.length

    setLocale('en', false); await flushPromises()

    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe(draft)
    expect(wrapper.find('[role="status"]').text()).toContain('unsaved configuration changes')
    expect(calls).toHaveLength(requests)
    expect(button(wrapper, 'Validate and preview').attributes('disabled')).toBeDefined()
    expect(button(wrapper, 'Deploy project').attributes('disabled')).toBeDefined()
    await button(wrapper, 'Save files').trigger('click'); await flushPromises()
    expect(calls.find(c => c.method === 'PUT')?.body).toEqual({ revision: 'revision-one', files: [{ path: '/srv/web/compose.yaml', content: draft }] })
    expect(button(wrapper, 'Deploy project').attributes('disabled')).toBeDefined()
    await button(wrapper, 'Validate and preview').trigger('click'); await flushPromises()
    await button(wrapper, 'Deploy project').trigger('click')
    expect(button(wrapper, 'Confirm action').attributes('disabled')).toBeDefined()
    const beforeConfirmation = calls.length

    setLocale('zh-CN', false); await flushPromises()

    expect(wrapper.find('.modal h2').text()).toBe('部署 Compose 项目')
    expect(button(wrapper, '确认执行').attributes('disabled')).toBeDefined()
    expect(calls).toHaveLength(beforeConfirmation)
    expect(calls.some(c => c.url.endsWith('/deploy'))).toBe(false)
    await wrapper.find('.modal input[type=checkbox]').setValue(true)
    await button(wrapper, '确认执行').trigger('click'); await flushPromises()
    expect(calls.find(c => c.url.endsWith('/deploy'))?.body).toEqual({ confirm: true, revision: 'revision-two' })
    wrapper.unmount()
  })
})
