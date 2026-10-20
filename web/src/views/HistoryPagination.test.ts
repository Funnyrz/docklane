import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
vi.mock('naive-ui', async () => {
  const { defineComponent } = await import('vue')
  return {
    NButton: defineComponent({ props: ['disabled', 'loading'], template: '<button :disabled="disabled || loading"><slot/></button>' }),
    NDrawer: defineComponent({ props: ['show'], template: '<section v-if="show"><slot/></section>' }),
    NDrawerContent: defineComponent({ template: '<section><slot/></section>' }),
    NSelect: defineComponent({ props: ['value', 'options', 'inputProps'], template: '<select/>' }), NInput: defineComponent({ template: '<input/>' }),
    NModal: defineComponent({ props: ['show'], template: '<section v-if="show"><slot/><slot name="footer"/></section>' }), NCheckbox: defineComponent({ template: '<input type="checkbox"/>' }),
  }
})
import TasksView from './TasksView.vue'
import SettingsView from './SettingsView.vue'
const requested: string[] = []
beforeEach(() => {
  requested.length = 0
  vi.stubGlobal('EventSource', class { onopen = null; onerror = null; onmessage = null; addEventListener() {} close() {} })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    requested.push(url); let data: unknown = {}
    if (url.includes('/tasks?') || url.includes('/audit?')) { const params = new URL(url, 'http://localhost').searchParams; const offset = Number(params.get('offset')); const limit = Number(params.get('limit')); data = Array.from({ length: offset === 0 ? limit : 1 }, (_, index) => ({ id: `entry-${index + offset}`, ...(url.includes('/audit?') ? { actor_id: 'admin-user-id', action: 'backup', resource_type: 'volume', resource_id: `volume-${index + offset}`, result: 'success', created_at: '2026-10-16T10:00:00Z' } : { type: 'backup', status: 'Succeeded', target: `volume-${index + offset}` }) })) }
    else if (url.endsWith('/capabilities')) data = { capabilities: {} }
    else if (url.endsWith('/settings')) data = { mode: 'docker' }
    return new Response(JSON.stringify(data), { status: 200 })
  }))
})
afterEach(() => vi.unstubAllGlobals())
function button(wrapper: VueWrapper, text: string) { const result = wrapper.findAll('button').find(item => item.text() === text); if (!result) throw new Error(`missing ${text}`); return result }
describe('history pagination', () => {
  for (const [kind, component] of [['tasks', TasksView], ['audit', SettingsView]] as const) {
    it(`loads older ${kind} records without hiding the last partial page`, async () => {
      const wrapper = mount(component); await flushPromises()
      const pageSize = kind === 'audit' ? 10 : 50
      if (kind === 'audit') {
        expect(requested.some(url => url.includes('/audit?'))).toBe(false)
        await wrapper.get('#settings-tab-audit').trigger('click'); await flushPromises()
      }
      expect(requested).toContain(`/api/v1/${kind}?limit=${pageSize + 1}&offset=0`)
      if (kind === 'audit') { expect(wrapper.text()).toContain('admin-user-id'); expect(wrapper.text()).toContain('volume / volume-0'); expect(wrapper.text()).toContain('已成功') }
      expect(button(wrapper, '上一页').attributes('disabled')).toBeDefined(); expect(button(wrapper, '下一页').attributes('disabled')).toBeUndefined()
      await button(wrapper, '下一页').trigger('click'); await flushPromises()
      expect(requested).toContain(`/api/v1/${kind}?limit=${pageSize + 1}&offset=${pageSize}`); expect(wrapper.text()).toContain('第 2 页'); expect(wrapper.text()).toContain(`volume-${pageSize}`)
      expect(button(wrapper, '下一页').attributes('disabled')).toBeDefined(); expect(button(wrapper, '上一页').attributes('disabled')).toBeUndefined()
      await button(wrapper, '上一页').trigger('click'); await flushPromises(); expect(wrapper.text()).toContain('第 1 页')
      if (kind === 'audit') {
        const auditRequests = requested.filter(url => url.includes('/audit?')).length
        await wrapper.get('#settings-tab-overview').trigger('click')
        await wrapper.get('#settings-tab-audit').trigger('click'); await flushPromises()
        expect(requested.filter(url => url.includes('/audit?'))).toHaveLength(auditRequests)
      }
      wrapper.unmount()
    })
  }
})
