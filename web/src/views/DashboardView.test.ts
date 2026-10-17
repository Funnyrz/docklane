import { mount,flushPromises } from '@vue/test-utils'
import { afterEach,describe,expect,it,vi } from 'vitest'
vi.mock('naive-ui',async()=>{const {defineComponent}=await import('vue');return {NButton:defineComponent({template:'<button><slot/></button>'})}})
import DashboardView from './DashboardView.vue'
afterEach(()=>vi.unstubAllGlobals())
describe('Docker disconnected dashboard',()=>{it('shows Engine error and does not display zero resource counts',async()=>{vi.stubGlobal('fetch',vi.fn(async(path:string)=>new Response(JSON.stringify(path.endsWith('/tasks')?[]:{host:{hostname:'test'},docker:{connected:false,error:'Docker socket unavailable'},containers:null,images:null,volumes:null}),{status:200})));const wrapper=mount(DashboardView);await flushPromises();expect(wrapper.text()).toContain('Docker socket unavailable');expect(wrapper.find('.metrics').exists()).toBe(false);expect(wrapper.text()).toContain('等待 Docker Engine 恢复连接');wrapper.unmount()})})
