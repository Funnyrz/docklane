import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, post, setCSRF, type Row } from '../api'
export const useAuth = defineStore('auth', () => {
 const ready = ref(false), initialized = ref(false), authenticated = ref(false), user = ref<Row>({})
 async function refresh() { const data = await api<{initialized:boolean;authenticated:boolean;user?:Row;csrf_token?:string}>('/auth/status'); initialized.value = data.initialized; authenticated.value = data.authenticated; user.value = data.user ?? {}; setCSRF(data.csrf_token ?? ''); ready.value = true }
 async function signIn(username:string,password:string) { await post(initialized.value ? '/auth/login' : '/auth/setup', {username,password}); await refresh() }
 async function logout() { await post('/auth/logout'); await refresh() }
 return {ready,initialized,authenticated,user,refresh,signIn,logout}
})
