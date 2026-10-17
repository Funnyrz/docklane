import { mount } from '@vue/test-utils'
import { describe,expect,it,vi } from 'vitest'
vi.mock('naive-ui',async()=>{const {defineComponent}=await import('vue');return {
 NModal:defineComponent({template:'<div><slot/><slot name="footer"/></div>'}),
 NButton:defineComponent({props:{disabled:Boolean},template:'<button :disabled="disabled"><slot/></button>'}),
 NCheckbox:defineComponent({props:{checked:Boolean},emits:['update:checked'],template:'<input type="checkbox" :checked="checked" @change="$emit(\'update:checked\',!checked)"/>'})
}})
import ConfirmDialog from './ConfirmDialog.vue'
describe('high risk confirmation',()=>{it('requires explicit acknowledgement and resets it when reopened',async()=>{const wrapper=mount(ConfirmDialog,{props:{show:true,title:'删除数据卷',detail:'将永久删除文件'}});expect(wrapper.findAll('button')[1]!.attributes('disabled')).toBeDefined();await wrapper.find('input').setValue(true);expect(wrapper.findAll('button')[1]!.attributes('disabled')).toBeUndefined();await wrapper.findAll('button')[1]!.trigger('click');expect(wrapper.emitted('confirm')).toHaveLength(1);await wrapper.setProps({show:false});await wrapper.setProps({show:true});expect(wrapper.findAll('button')[1]!.attributes('disabled')).toBeDefined()})})
