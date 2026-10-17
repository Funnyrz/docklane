import { afterEach,describe,expect,it,vi } from 'vitest'
import { api,APIError,post,setCSRF,rows } from './api'
afterEach(()=>{vi.unstubAllGlobals();document.cookie='csrf_token=;max-age=0';setCSRF('')})
describe('same-origin authenticated API',()=>{
 it('sends session credentials and current CSRF cookie for mutations',async()=>{document.cookie='csrf_token=session-token';setCSRF('old-token');const mock=vi.fn().mockResolvedValue(new Response('{}',{status:200}));vi.stubGlobal('fetch',mock);await post('/tasks/1/cancel',{confirmed:true});const [url,init]=mock.mock.calls[0]!;expect(url).toBe('/api/v1/tasks/1/cancel');expect(init.credentials).toBe('same-origin');expect(init.headers.get('X-CSRF-Token')).toBe('session-token')})
 it('preserves server failures rather than returning empty resources',async()=>{vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response(JSON.stringify({error:{code:'ENGINE_OFFLINE',message:'Docker unavailable'}}),{status:503})));await expect(api('/hosts/local/containers')).rejects.toMatchObject({status:503,code:'ENGINE_OFFLINE',message:'Docker unavailable'} satisfies Partial<APIError>)})
 it('accepts paginated and direct lists',()=>{expect(rows({items:[{id:'one'}]})).toEqual([{id:'one'}]);expect(rows([{id:'two'}])).toEqual([{id:'two'}])})
})
