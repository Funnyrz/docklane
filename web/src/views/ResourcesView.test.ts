import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../i18n'
vi.mock('naive-ui', async () => {
  const { defineComponent } = await import('vue')
  return {
    NButton: defineComponent({ props: ['disabled'], template: '<button :disabled="disabled"><slot/></button>' }),
    NDrawer: defineComponent({ props: ['show'], template: '<section v-if="show"><slot/></section>' }),
    NDrawerContent: defineComponent({ props: ['title'], template: '<section><h2>{{title}}</h2><slot/></section>' }),
    NSelect: defineComponent({ props: ['value', 'inputProps', 'options'], emits: ['update:value'], template: '<select v-bind="inputProps" :value="value" @change="$emit(\'update:value\',$event.target.value)"><option v-for="option in options" :key="option.value" :value="option.value">{{option.label}}</option></select>' }),
    NInput: defineComponent({ props: ['value', 'inputProps'], emits: ['update:value'], template: '<input v-bind="inputProps" :value="value" @input="$emit(\'update:value\',$event.target.value)"/>' }),
    NCheckbox: defineComponent({ template: '<label><input type="checkbox"/><slot/></label>' }),
    NModal: defineComponent({ props: ['show', 'title'], template: '<section v-if="show" class="modal"><h2>{{title}}</h2><slot/><slot name="footer"/></section>' }),
  }
})
import ResourcesView from './ResourcesView.vue'
class Socket {
  static OPEN = 1
  readyState = 1
  binaryType = ''
  onopen?: () => void
  onmessage?: (event: { data: ArrayBuffer }) => void
  onerror?: () => void
  onclose?: () => void
  send = vi.fn()
  close = vi.fn()
  constructor() { sockets.push(this) }
}
let sockets: Socket[] = []
beforeEach(() => {
  sockets = []
  vi.stubGlobal('WebSocket', Socket)
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    let data: unknown = {}
    if (url.includes('?page=')) data = { items: [{ Id: 'resource-1', Names: ['fixture'], Name: 'fixture', RepoTags: ['fixture:latest'] }], total: 1 }
    else if (url.endsWith('/volumes/resource-1')) data = { volume: { Name: 'fixture-volume' }, containers: [] }
    else if (url.endsWith('/images/resource-1')) data = { image: { RepoTags: ['fixture:latest'] }, containers: [] }
    else if (url.endsWith('/containers/resource-1')) data = { Id: 'resource-1', Name: 'fixture' }
    return new Response(JSON.stringify(data), { status: 200 })
  }))
})
afterEach(() => { vi.unstubAllGlobals(); document.cookie = 'csrf_token=; Max-Age=0' })
function button(wrapper: VueWrapper, text: string) { const found = wrapper.findAll('button').find(b => b.text() === text); if (!found) throw new Error(`missing ${text}`); return found }
describe('resource detail interactions', () => {
  for (const [kind, title] of [['volumes', 'fixture-volume'], ['images', 'fixture:latest']] as const) {
    it(`labels wrapped ${kind} inspection with the actual resource name`, async () => {
      const wrapper = mount(ResourcesView, { props: { kind } }); await flushPromises()
      await button(wrapper, '详情').trigger('click'); await flushPromises()
      expect(wrapper.find('h2').text()).toBe(title)
      wrapper.unmount()
    })
  }
  it('initializes the PTY size, sends commands, and closes the socket when leaving', async () => {
    document.cookie = 'csrf_token=test-csrf'
    const wrapper = mount(ResourcesView, { props: { kind: 'containers' } }); await flushPromises()
    await button(wrapper, '详情').trigger('click'); await flushPromises(); await button(wrapper, '终端').trigger('click')
    await button(wrapper, '连接 TTY').trigger('click'); sockets[0]!.onopen?.()
    expect(sockets[0]!.send).toHaveBeenCalledWith(JSON.stringify({ type: 'resize', rows: 24, cols: 80 }))
    await wrapper.find('[aria-label="终端命令"]').setValue('printf ready'); await wrapper.find('form').trigger('submit')
    expect(sockets[0]!.send).toHaveBeenCalledWith('printf ready\r')
    const output = new ArrayBuffer(7); new Uint8Array(output).set([114, 101, 97, 100, 121, 13, 10]); sockets[0]!.onmessage?.({ data: output }); await flushPromises(); expect(wrapper.find('.terminal').text()).toContain('ready')
    wrapper.unmount(); expect(sockets[0]!.close).toHaveBeenCalledOnce()
  })
  it('keeps the terminal tab, command draft, socket and raw output when switching languages', async () => {
    const wrapper = mount(ResourcesView, { props: { kind: 'containers' } }); await flushPromises()
    await button(wrapper, '详情').trigger('click'); await flushPromises(); await button(wrapper, '终端').trigger('click')
    await button(wrapper, '连接 TTY').trigger('click'); sockets[0]!.onopen?.()
    await wrapper.find('[aria-label="终端命令"]').setValue('printf 尚未发送')
    const encoded = new TextEncoder().encode('原始容器输出: ready\r\n'), output = new ArrayBuffer(encoded.length)
    new Uint8Array(output).set(encoded)
    sockets[0]!.onmessage?.({ data: output }); await flushPromises()
    const rawOutput = wrapper.find('.terminal').element.textContent
    const requests = vi.mocked(fetch).mock.calls.length, sends = sockets[0]!.send.mock.calls.length

    setLocale('en', false); await flushPromises()

    expect(wrapper.find('.tabs button.active').text()).toBe('Terminal')
    expect((wrapper.find('[aria-label="Terminal command"]').element as HTMLInputElement).value).toBe('printf 尚未发送')
    expect(wrapper.find('.terminal').element.textContent).toBe(rawOutput)
    expect(wrapper.find('.terminal').text()).toContain('原始容器输出')
    expect(sockets).toHaveLength(1); expect(sockets[0]!.send).toHaveBeenCalledTimes(sends)
    expect(sockets[0]!.close).not.toHaveBeenCalled(); expect(fetch).toHaveBeenCalledTimes(requests)
    wrapper.unmount()
  })
  it('translates an open pull form and validation without losing values or changing submitted keys', async () => {
    const wrapper = mount(ResourcesView, { props: { kind: 'images' } }); await flushPromises()
    await button(wrapper, '拉取镜像').trigger('click')
    await wrapper.find('#field-platform').setValue('linux/arm64')
    await wrapper.find('.modal form').trigger('submit')
    expect(wrapper.find('[role="alert"]').text()).toContain('请填写镜像引用')
    const requests = vi.mocked(fetch).mock.calls.length

    setLocale('en', false); await flushPromises()

    expect(wrapper.find('.modal h2').text()).toBe('Pull image')
    expect(wrapper.find('label[for="field-image"]').text()).toContain('Image reference')
    expect(wrapper.find('[role="alert"]').text()).toBe('Please enter Image reference')
    expect(wrapper.find('#field-credential_id option').text()).toBe('Anonymous pull')
    expect((wrapper.find('#field-platform').element as HTMLInputElement).value).toBe('linux/arm64')
    expect(fetch).toHaveBeenCalledTimes(requests)
    await wrapper.find('#field-image').setValue('registry.example/team/app:latest')
    await wrapper.find('#field-credential_id').setValue('')
    await wrapper.find('.modal form').trigger('submit'); await flushPromises()
    const pull = vi.mocked(fetch).mock.calls.find(([url]) => String(url).endsWith('/images/pull'))
    expect(JSON.parse(String(pull?.[1]?.body))).toEqual({ image: 'registry.example/team/app:latest', credential_id: '', platform: 'linux/arm64' })
    expect(wrapper.text()).toContain('Operation submitted')
    wrapper.unmount()
  })
})
