import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import FormDialog from './FormDialog.vue'
afterEach(() => { vi.unstubAllGlobals(); document.body.innerHTML = '' })
it('renders a real labelled select input rather than dropping input-props on a non-filterable select', async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  const wrapper = mount(FormDialog, { attachTo: document.body, props: { show: false, title: 'Fixture form', fields: [{ key: 'repository', label: '备份仓库', type: 'select', options: [{ label: 'Local', value: 'local' }] }], submit: async () => {} } })
  await wrapper.setProps({ show: true }); await flushPromises()
  const input = document.querySelector<HTMLInputElement>('#field-repository')
  expect(input).not.toBeNull(); expect(Array.from(input!.labels ?? []).map(label => label.textContent?.trim())).toContain('备份仓库')
  wrapper.unmount()
})

it('keeps the opening Enter keyup from acknowledging a destructive action', async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  const { defineComponent, h, ref } = await import('vue')
  const { default: ConfirmDialog } = await import('./ConfirmDialog.vue')
  const { NModal } = await import('naive-ui')
  const Harness = defineComponent({ setup() { const show = ref(false); return () => h('div', [h('button', { onClick: () => { show.value = true } }, 'Open confirmation'), h(ConfirmDialog, { show: show.value, title: '危险操作', detail: '需要独立确认', 'onUpdate:show': (value: boolean) => { show.value = value } })]) } })
  const wrapper = mount(Harness, { attachTo: document.body })
  const opener = wrapper.find('button'); opener.element.focus(); await opener.trigger('keydown', { key: 'Enter' }); await opener.trigger('click'); await flushPromises()
  document.activeElement?.dispatchEvent(new KeyboardEvent('keyup', { key: 'Enter', bubbles: true })); await flushPromises()
  expect(wrapper.findComponent(NModal).props('autoFocus')).toBe(false)
  const checkbox = document.querySelector('[role="checkbox"]')
  expect(checkbox?.getAttribute('aria-checked')).toBe('false')
  const execute = Array.from(document.querySelectorAll('button')).find(button => button.textContent === '确认执行')
  expect(execute?.disabled).toBe(true)
  wrapper.unmount()
})
