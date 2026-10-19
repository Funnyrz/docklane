<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { NConfigProvider, NButton, NInput, zhCN, dateZhCN, enUS, dateEnUS } from 'naive-ui'
import { useAuth } from './stores/auth'
import { message, str } from './api'
import { t, locale } from './i18n'
import LanguageSelect from './components/LanguageSelect.vue'
import { theme, mobileTheme } from './theme'
import AppIcon from './components/AppIcon.vue'
import DashboardView from './views/DashboardView.vue'
import ResourcesView from './views/ResourcesView.vue'
import ComposeView from './views/ComposeView.vue'
import BackupView from './views/BackupView.vue'
import TasksView from './views/TasksView.vue'
import SettingsView from './views/SettingsView.vue'

const auth = useAuth()
const error = ref<unknown>('')
const busy = ref(false)
const username = ref('')
const password = ref('')
const mobile = ref(false)
const isSmallScreen = ref(false)
const sidebarCollapsed = ref(false)
const sidebarStorageKey = 'docker-manager:sidebar-collapsed'
const desktopCollapsed = computed(() => !isSmallScreen.value && sidebarCollapsed.value)
const sidebar = ref<HTMLElement | null>(null)
const menuButton = ref<HTMLButtonElement | null>(null)
const sidebarToggle = ref<HTMLButtonElement | null>(null)
const navigationOpen = computed(() => isSmallScreen.value && mobile.value)
let mobileQuery: MediaQueryList | undefined
const nav = computed(() => [
  { key: 'dashboard', title: t('nav.dashboard') },
  { key: 'containers', title: t('nav.containers') },
  { key: 'compose', title: t('nav.compose') },
  { key: 'images', title: t('nav.images') },
  { key: 'networks', title: t('nav.networks') },
  { key: 'volumes', title: t('nav.volumes') },
  { key: 'backups', title: t('nav.backups') },
  { key: 'tasks', title: t('nav.tasks') },
  { key: 'settings', title: t('nav.settings') },
])
const page = ref(location.hash.slice(1) || 'dashboard')
const active = computed(() => nav.value.find(item => item.key === page.value) ?? nav.value[0]!)
const userInitial = computed(() => str(auth.user.username).slice(0, 1).toUpperCase())

function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
  try { localStorage.setItem(sidebarStorageKey, String(sidebarCollapsed.value)) }
  catch { /* Keep the current choice when browser storage is unavailable. */ }
}
function navigate(key: string) {
  page.value = key
  location.hash = key
  mobile.value = false
}
function hash() {
  page.value = location.hash.slice(1) || 'dashboard'
}
function navigationKeyboard(event: KeyboardEvent) {
  if (!navigationOpen.value) return
  if (event.key === 'Escape') {
    event.preventDefault()
    mobile.value = false
    return
  }
  if (event.key !== 'Tab') return
  const focusable = sidebar.value?.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])')
  if (!focusable?.length) return
  const first = focusable[0]!
  const last = focusable[focusable.length - 1]!
  if (event.shiftKey && (document.activeElement === first || !sidebar.value?.contains(document.activeElement))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && (document.activeElement === last || !sidebar.value?.contains(document.activeElement))) {
    event.preventDefault()
    first.focus()
  }
}
function updateScreenSize() {
  const focused = document.activeElement
  isSmallScreen.value = mobileQuery?.matches ?? false
  if (!isSmallScreen.value) {
    mobile.value = false
    if (focused === menuButton.value) void nextTick(() => sidebarToggle.value?.focus())
  } else if (!mobile.value && (sidebar.value?.contains(focused) || focused === sidebarToggle.value)) {
    void nextTick(() => menuButton.value?.focus())
  }
}
watch(navigationOpen, async (open, wasOpen) => {
  await nextTick()
  if (open && navigationOpen.value) {
    sidebar.value?.querySelector<HTMLButtonElement>('button[aria-current="page"]')?.focus()
  } else if (wasOpen && isSmallScreen.value) {
    menuButton.value?.focus()
  }
})
async function load() {
  error.value = ''
  try { await auth.refresh() }
  catch (reason) { error.value = reason }
}
async function login() {
  busy.value = true
  error.value = ''
  try {
    await auth.signIn(username.value, password.value)
    password.value = ''
  } catch (reason) { error.value = reason }
  finally { busy.value = false }
}
async function logout() {
  try { await auth.logout(); mobile.value = false }
  catch (reason) { error.value = reason }
}
onMounted(() => {
  try { sidebarCollapsed.value = localStorage.getItem(sidebarStorageKey) === 'true' }
  catch { /* Expanded navigation remains available without browser storage. */ }
  void load()
  mobileQuery = window.matchMedia('(max-width: 800px)')
  updateScreenSize()
  mobileQuery.addEventListener('change', updateScreenSize)
  window.addEventListener('hashchange', hash)
  window.addEventListener('keydown', navigationKeyboard)
})
onUnmounted(() => {
  mobileQuery?.removeEventListener('change', updateScreenSize)
  window.removeEventListener('hashchange', hash)
  window.removeEventListener('keydown', navigationKeyboard)
})
</script>

<template>
  <n-config-provider :theme-overrides="isSmallScreen ? mobileTheme : theme" :locale="locale === 'en' ? enUS : zhCN" :date-locale="locale === 'en' ? dateEnUS : dateZhCN">
    <div v-if="!auth.ready || !auth.authenticated" class="auth-shell">
      <aside class="auth-story">
        <div class="brand"><span class="brand-mark"><AppIcon name="containers" /></span><span class="brand-copy"><b>DOCKLANE</b></span></div>
        <div class="auth-story-copy">
          <p class="eyebrow">{{ t('common.tagline') }}</p>
          <h1>{{ t('auth.headlineFirst') }}<br>{{ t('auth.headlineLast') }}</h1>
          <p>{{ t('auth.introductionFirst') }}<br>{{ t('auth.introductionLast') }}</p>
          <div class="auth-features">
            <span><AppIcon name="containers" />{{ t('auth.resources') }}</span>
            <span><AppIcon name="shield" />{{ t('auth.protection') }}</span>
            <span><AppIcon name="activity" />{{ t('auth.activity') }}</span>
          </div>
        </div>
        <small class="auth-story-footer">{{ t('common.privateOperations') }} <span>/</span> v0.3</small>
      </aside>
      <main class="auth-form">
        <div class="auth-form-inner">
          <div class="auth-language"><LanguageSelect /></div>
          <span class="eyebrow">{{ t('common.operationsConsole') }}</span>
          <h2>{{ !auth.ready ? t('auth.connecting') : auth.initialized ? t('auth.welcome') : t('auth.setup') }}</h2>
          <p class="muted">{{ auth.initialized ? t('auth.loginDescription') : t('auth.setupDescription') }}</p>
          <p v-if="error" class="notice error" role="alert">{{ message(error) }}</p>
          <form v-if="auth.ready" @submit.prevent="login">
            <div class="form-field">
              <label for="username">{{ t('auth.username') }}</label>
              <n-input v-model:value="username" :input-props="{ id: 'username', autocomplete: 'username', required: true }" :placeholder="t('auth.usernamePlaceholder')" />
            </div>
            <div class="form-field">
              <label for="password">{{ auth.initialized ? t('auth.password') : t('auth.setPassword') }}</label>
              <n-input v-model:value="password" type="password" show-password-on="click" :input-props="{ id: 'password', autocomplete: auth.initialized ? 'current-password' : 'new-password', required: true, minlength: 12 }" :placeholder="t('auth.passwordPlaceholder')" />
            </div>
            <n-button class="auth-submit" type="primary" block attr-type="submit" :loading="busy">
              {{ auth.initialized ? t('auth.login') : t('auth.createAdmin') }}<AppIcon name="arrow-right" />
            </n-button>
          </form>
          <n-button v-else :loading="!error" @click="load">{{ t('auth.reconnect') }}</n-button>
          <p class="auth-note"><AppIcon name="shield" />{{ t('auth.credentialsNote') }}</p>
        </div>
      </main>
    </div>
    <div v-else class="shell" :class="{ 'sidebar-collapsed': desktopCollapsed }">
      <aside id="main-navigation" ref="sidebar" class="sidebar" :class="{ open: navigationOpen }" :inert="isSmallScreen && !navigationOpen" :role="navigationOpen ? 'dialog' : undefined" :aria-modal="navigationOpen ? true : undefined" :aria-label="t('nav.main')">
        <a class="brand sidebar-brand" href="#dashboard" aria-label="Docklane" :title="desktopCollapsed ? 'Docklane' : undefined" @click.prevent="navigate('dashboard')">
          <span class="brand-mark"><AppIcon name="containers" /></span>
          <span class="brand-copy"><b>DOCKLANE</b></span>
        </a>
        <div class="host-label"><AppIcon name="server" />{{ t('nav.localHost') }}<span>01</span></div>
        <p class="sidebar-caption">{{ t('nav.caption') }}</p>
        <nav :aria-label="t('nav.main')">
          <button v-for="item in nav" :key="item.key" :class="{ active: active.key === item.key }" :aria-label="item.title" :title="desktopCollapsed ? item.title : undefined" :aria-current="active.key === item.key ? 'page' : undefined" @click="navigate(item.key)">
            <AppIcon class="nav-icon" :name="item.key" />
            <span class="nav-label">{{ item.title }}</span>
          </button>
        </nav>
        <div class="sidebar-bottom">
          <span class="version">v0.3 <span>{{ t('nav.platform') }}</span></span>
          <button class="sidebar-user" :aria-label="`${t('nav.logout')} · ${str(auth.user.username)}`" :title="desktopCollapsed ? `${t('nav.logout')} · ${str(auth.user.username)}` : undefined" @click="logout">
            <span class="avatar">{{ userInitial }}</span>
            <span class="sidebar-user-copy"><strong>{{ str(auth.user.username) }}</strong><small>{{ t('nav.logout') }}</small></span>
            <AppIcon class="logout-icon" name="logout" />
          </button>
        </div>
      </aside>
      <div v-if="navigationOpen" class="nav-backdrop" @click="mobile = false" />
      <div class="workspace" :inert="navigationOpen">
        <header class="topbar">
          <div class="topbar-title">
            <button v-if="!isSmallScreen" ref="sidebarToggle" class="sidebar-toggle" :aria-label="t(desktopCollapsed ? 'nav.expand' : 'nav.collapse')" :title="t(desktopCollapsed ? 'nav.expand' : 'nav.collapse')" :aria-expanded="!desktopCollapsed" aria-controls="main-navigation" @click="toggleSidebar"><AppIcon :name="desktopCollapsed ? 'panel-expand' : 'panel-collapse'" /></button>
            <button ref="menuButton" class="mobile-menu" :aria-label="navigationOpen ? t('nav.close') : t('nav.open')" :aria-expanded="navigationOpen" aria-controls="main-navigation" @click="mobile = !mobile"><AppIcon name="menu" /></button>
            <div><span class="breadcrumb">{{ t('nav.console') }} <span>/</span></span>{{ active.title }}</div>
          </div>
          <div class="topbar-end"><LanguageSelect /><span class="host-pill"><AppIcon name="server" />{{ t('nav.localHost') }}</span><span class="avatar" :title="str(auth.user.username)">{{ userInitial }}</span></div>
        </header>
        <main class="page">
          <div v-if="active.key !== 'dashboard'" class="page-heading">
            <h1>{{ active.title }}</h1>
          </div>
          <p v-if="error" class="notice error" role="alert">{{ message(error) }}</p>
          <DashboardView v-if="page === 'dashboard'" @navigate="navigate" />
          <ResourcesView v-else-if="['containers', 'images', 'networks', 'volumes'].includes(page)" :key="page" :kind="page" @navigate="navigate" />
          <ComposeView v-else-if="page === 'compose'" />
          <BackupView v-else-if="page === 'backups'" />
          <TasksView v-else-if="page === 'tasks'" />
          <SettingsView v-else-if="page === 'settings'" />
          <DashboardView v-else @navigate="navigate" />
          <footer class="page-footer"><span>{{ t('nav.footer') }}</span><span>Docklane / v0.3</span></footer>
        </main>
      </div>
    </div>
  </n-config-provider>
</template>
