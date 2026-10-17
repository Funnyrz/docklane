import { beforeEach,afterEach,describe,expect,it,vi } from 'vitest'
import { createPinia,setActivePinia } from 'pinia'
import { useAuth } from './auth'
beforeEach(()=>setActivePinia(createPinia()));afterEach(()=>vi.unstubAllGlobals())
describe('admin initialization and login',()=>{
 it('uses setup only before initialization and rechecks authenticated status',async()=>{let initialized=false;const fetcher=vi.fn(async(input:string,init:RequestInit)=>{if(input.endsWith('/auth/setup'))initialized=true;return new Response(JSON.stringify({initialized,authenticated:initialized,csrf_token:'token',user:initialized?{username:'admin'}:undefined}),{status:200})});vi.stubGlobal('fetch',fetcher);const auth=useAuth();await auth.refresh();expect(auth.initialized).toBe(false);await auth.signIn('admin','long-secure-password');expect(fetcher.mock.calls.some(([p])=>p.endsWith('/auth/setup'))).toBe(true);expect(auth.authenticated).toBe(true);await auth.signIn('admin','long-secure-password');expect(fetcher.mock.calls.some(([p])=>p.endsWith('/auth/login'))).toBe(true)})
})
