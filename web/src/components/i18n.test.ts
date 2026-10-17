import { mount, flushPromises } from '@vue/test-utils'
import { nextTick } from 'vue'
import { describe, expect, it, vi } from 'vitest'
vi.mock('naive-ui', async () => {
  const { defineComponent } = await import('vue')
  return {
    NModal: defineComponent({ props: ['show', 'title'], template: '<section v-if="show"><h2>{{ title }}</h2><slot/><slot name="footer"/></section>' }),
    NButton: defineComponent({ props: ['disabled', 'loading', 'attrType'], template: '<button :type="attrType || \'button\'" :disabled="disabled || loading"><slot/></button>' }),
    NInput: defineComponent({ props: ['value', 'inputProps', 'placeholder'], emits: ['update:value'], template: '<input v-bind="inputProps" :value="value" :placeholder="placeholder" @input="$emit(\'update:value\', $event.target.value)"/>' }),
    NSelect: defineComponent({ props: ['value', 'inputProps', 'options'], emits: ['update:value'], template: '<select v-bind="inputProps" :value="value" @change="$emit(\'update:value\', $event.target.value)"><option v-for="option in options" :value="option.value">{{ option.label }}</option></select>' }),
    NCheckbox: defineComponent({ props: ['checked'], emits: ['update:checked'], template: '<label><input type="checkbox" :checked="checked" @change="$emit(\'update:checked\', $event.target.checked)"/><slot/></label>' }),
  }
})
import FormDialog from './FormDialog.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import Notice from './Notice.vue'
import { APIError } from '../api'
import { enMessages, localized, setLocale, zhMessages } from '../i18n'

describe('live localization in shared dialogs', () => {
  it('translates existing validation and field labels without discarding entered values or submitting', async () => {
    const submit = vi.fn().mockResolvedValue({})
    const wrapper = mount(FormDialog, { props: {
      show: false, title: localized('compose.importTitle'), description: localized('compose.importInfo'), submit,
      fields: [
        { key: 'name', label: localized('compose.name'), required: true },
        { key: 'workdir', label: localized('compose.workdir'), required: true },
      ],
    } })
    await wrapper.setProps({ show: true })
    await wrapper.find('#field-name').setValue('existing-user-project')
    await wrapper.find('form').trigger('submit')
    expect(wrapper.find('[role="alert"]').text()).toBe('请填写工作目录')
    expect(submit).not.toHaveBeenCalled()
    setLocale('en', false); await nextTick()
    expect(wrapper.find('h2').text()).toBe(enMessages.compose.importTitle)
    expect(wrapper.find('label[for="field-name"]').text()).toContain('Project name')
    expect(wrapper.find('[role="alert"]').text()).toBe('Please enter Working directory')
    expect((wrapper.find('#field-name').element as HTMLInputElement).value).toBe('existing-user-project')
    expect(submit).not.toHaveBeenCalled()
    await wrapper.find('#field-workdir').setValue('/srv/fixture')
    await wrapper.find('form').trigger('submit'); await flushPromises()
    expect(submit).toHaveBeenCalledExactlyOnceWith({ name: 'existing-user-project', workdir: '/srv/fixture' })
    wrapper.unmount()
  })

  it('keeps acknowledgement mandatory while translating an open risk confirmation', async () => {
    const wrapper = mount(ConfirmDialog, { props: { show: true, title: localized('compose.deployTitle'), detail: localized('compose.deployWarning') } })
    const confirmButton = () => wrapper.findAll('button')[1]!
    expect(confirmButton().attributes('disabled')).toBeDefined()
    setLocale('en', false); await nextTick()
    expect(wrapper.find('h2').text()).toBe(enMessages.compose.deployTitle)
    expect(wrapper.find('.confirm-copy').text()).toBe(enMessages.compose.deployWarning)
    expect(wrapper.find('label').text()).toBe(enMessages.common.acknowledge)
    expect(confirmButton().text()).toBe(enMessages.common.confirm)
    expect(confirmButton().attributes('disabled')).toBeDefined()
    await confirmButton().trigger('click'); expect(wrapper.emitted('confirm')).toBeUndefined()
    await wrapper.find('input[type="checkbox"]').setValue(true)
    setLocale('zh-CN', false); await nextTick()
    expect(wrapper.find('label').text()).toBe(zhMessages.common.acknowledge)
    expect((wrapper.find('input').element as HTMLInputElement).checked).toBe(true)
    expect(confirmButton().attributes('disabled')).toBeUndefined()
    await confirmButton().trigger('click'); expect(wrapper.emitted('confirm')).toHaveLength(1)
    await wrapper.setProps({ show: false }); await wrapper.setProps({ show: true })
    expect(confirmButton().attributes('disabled')).toBeDefined()
    expect((wrapper.find('input').element as HTMLInputElement).checked).toBe(false)
    wrapper.unmount()
  })

  it('re-renders an existing API failure without losing the original diagnostic detail', async () => {
    const failure = new APIError(503, 'ENGINE_OFFLINE', 'socket diagnostic remains raw')
    const wrapper = mount(Notice, { props: { error: failure } })
    expect(wrapper.find('[role="alert"]').text()).toContain(zhMessages.errors.ENGINE_OFFLINE)
    setLocale('en', false); await nextTick()
    expect(wrapper.find('[role="alert"]').text()).toContain(enMessages.errors.ENGINE_OFFLINE)
    expect(wrapper.find('[role="alert"]').text()).toContain('socket diagnostic remains raw')
    expect(failure).toMatchObject({ status: 503, code: 'ENGINE_OFFLINE', message: 'socket diagnostic remains raw' })
    wrapper.unmount()
  })
})
