<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, computed, watch, nextTick } from 'vue'
import { NButton, NInput, NDrawer, NDrawerContent } from 'naive-ui'
import { api, post, str, pretty, rows, type Row } from '../api'
import Notice from '../components/Notice.vue'
import AppIcon from '../components/AppIcon.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import CapabilitySettingsEditor from '../components/CapabilitySettingsEditor.vue'
import type { CapabilitySettingsState } from '../settings'
import { t, formatDateTime, formatNumber, displayState } from '../i18n'
import { describeCapabilities, type Capability } from '../capabilities'

interface SettingsResponse extends Row { capability_settings?: CapabilitySettingsState }
type SettingsTab = 'overview' | 'diagnostics' | 'audit'
type DrawerKind = 'capability' | 'paths' | 'mappings' | 'diagnostics' | 'daemon' | 'preview'
const tabs: SettingsTab[] = ['overview', 'diagnostics', 'audit']
const activeTab = ref<SettingsTab>('overview')
const settings = ref<SettingsResponse>({}), capabilities = ref<Record<string, Capability>>({})
const error = ref<unknown>(''), busy = ref(false), savedNotice = ref(''), editor = ref(false)
const auditPage = ref(0), auditHasNext = ref(false), auditBusy = ref(false), auditError = ref<unknown>(''), audits = ref<Row[]>([])
const pageSize = 10
let auditLoaded = false, auditDirty = false, auditRequest = 0, loadRequest = 0, alive = true
const mirror = ref(''), testImage = ref(''), diagnostics = ref<Row | null>(null), daemon = ref<Row | null>(null), preview = ref<Row | null>(null)
const diagnosticError = ref<unknown>(''), diagnosticBusy = ref(false), daemonBusy = ref(false), previewBusy = ref(false), actionBusy = ref(false), confirm = ref(false)
let mirrorRevision = 0
const drawer = ref<DrawerKind | null>(null), selectedCapability = ref('')
let drawerTrigger: HTMLElement | null = null, pendingEditor = false
const capabilityItems = computed(() => describeCapabilities(capabilities.value))
const capabilityDetail = computed(() => capabilityItems.value.find(item => item.key === selectedCapability.value))
const allowedPaths = computed(() => Array.isArray(settings.value.allowed_paths) ? settings.value.allowed_paths.map(str) : [])
const pathMappings = computed(() => rows(settings.value.path_mappings))
const canModify = computed(() => capabilities.value['host.daemon_config']?.available === true && capabilities.value['host.service_restart']?.available === true)
const canConfigureDetail = computed(() => Boolean(settings.value.capability_settings && ['ALLOWED_PATHS_MISSING', 'PATH_MAPPINGS_MISSING', 'DAEMON_WRITE_NOT_AUTHORIZED'].includes(capabilityDetail.value?.reasonCode ?? '')))
const drawerTitle = computed(() => drawer.value === 'capability' ? capabilityDetail.value?.name ?? '' : t({ paths: 'settings.allowedDirectories', mappings: 'settings.directoryMappings', diagnostics: 'settingsLayout.diagnosisResult', daemon: 'settingsLayout.configResult', preview: 'settingsLayout.previewResult' }[drawer.value as Exclude<DrawerKind, 'capability'>] ?? 'settingsLayout.details'))
const resultItems = computed(() => ([['diagnostics', diagnostics.value, 'settingsLayout.diagnosisResult'], ['daemon', daemon.value, 'settingsLayout.configResult'], ['preview', preview.value, 'settingsLayout.previewResult']] as const).filter(([, value]) => value))

function checkSummary(value: unknown) {
  const available = value && typeof value === 'object' ? (value as Row).available : undefined
  return t(available === true ? 'settingsLayout.passed' : available === false ? 'settingsLayout.failed' : 'settingsLayout.notTested')
}
function resultSummary(kind: 'diagnostics' | 'daemon' | 'preview') {
  if (kind === 'preview') return t('settingsLayout.previewReady')
  if (kind === 'daemon') return t(daemon.value?.available === true ? 'settingsLayout.configRead' : daemon.value?.available === false ? 'settingsLayout.configInstructions' : 'settingsLayout.configUnknown')
  const pull = diagnostics.value?.image_pull as Row | undefined
  return t('settingsLayout.diagnosticSummary', {
    network: checkSummary(diagnostics.value?.network),
    registry: checkSummary(diagnostics.value?.registry_api),
    pull: t(typeof pull?.task_id === 'string' && pull.task_id.trim() ? 'settingsLayout.pullSubmitted' : 'settingsLayout.pullNotTested'),
  })
}
function showDrawer(kind: DrawerKind, event?: Event, key = '') {
  drawerTrigger = event?.currentTarget instanceof HTMLElement ? event.currentTarget : document.activeElement instanceof HTMLElement ? document.activeElement : null
  selectedCapability.value = key
  drawer.value = kind
}
function closeDrawer() { drawer.value = null }
function drawerClosed() {
  if (pendingEditor) {
    pendingEditor = false
    // The modal restores focus to the element active when it opens.
    document.getElementById('settings-configure')?.focus()
    editor.value = true
  } else if (drawerTrigger?.isConnected) drawerTrigger.focus()
}
function configureFromDrawer() { pendingEditor = true; closeDrawer() }
watch(editor, async value => { if (!value) { await nextTick(); document.getElementById('settings-configure')?.focus() } })
function selectTab(tab: SettingsTab) {
  activeTab.value = tab
  if (tab === 'audit' && (!auditLoaded || auditDirty)) void loadAudit()
}
function tabKey(event: KeyboardEvent, tab: SettingsTab) {
  let index = tabs.indexOf(tab)
  if (event.key === 'ArrowRight') index = (index + 1) % tabs.length
  else if (event.key === 'ArrowLeft') index = (index + tabs.length - 1) % tabs.length
  else if (event.key === 'Home') index = 0
  else if (event.key === 'End') index = tabs.length - 1
  else return
  event.preventDefault()
  const selected = tabs[index]!
  selectTab(selected)
  document.getElementById(`settings-tab-${selected}`)?.focus()
}
function configurationSaved(value: Row & { capability_settings: CapabilitySettingsState }) {
  // Reads started before this save may describe the previous configuration.
  loadRequest++
  auditRequest++
  busy.value = false
  auditBusy.value = false
  settings.value = value
  savedNotice.value = value.capability_settings.pending_restart ? 'settingsEditor.saved' : 'settingsEditor.restored'
  auditDirty = true
  if (activeTab.value === 'audit') void loadAudit()
}
async function load() {
  const request = ++loadRequest
  busy.value = true
  try {
    const [configuration, capabilityResponse] = await Promise.all([api<SettingsResponse>('/settings'), api<{ capabilities?: Record<string, Capability> }>('/hosts/local/capabilities')])
    if (!alive || request !== loadRequest) return
    settings.value = configuration
    capabilities.value = capabilityResponse.capabilities ?? {}
    error.value = ''
  } catch (e) { if (alive && request === loadRequest) error.value = e }
  finally { if (alive && request === loadRequest) busy.value = false }
  if (alive && activeTab.value === 'audit') void loadAudit()
}
async function loadAudit() {
  const request = ++auditRequest, page = auditPage.value
  auditBusy.value = true
  auditError.value = ''
  try {
    const result = rows(await api(`/audit?limit=${pageSize + 1}&offset=${page * pageSize}`))
    if (!alive || request !== auditRequest) return
    auditHasNext.value = result.length > pageSize
    audits.value = result.slice(0, pageSize)
    auditLoaded = true
    auditDirty = false
  } catch (e) { if (alive && request === auditRequest) { audits.value = []; auditHasNext.value = false; auditError.value = e } }
  finally { if (alive && request === auditRequest) auditBusy.value = false }
}
async function diagnose(event: Event) {
  diagnosticBusy.value = true
  diagnosticError.value = ''
  try {
    diagnostics.value = await post('/host/diagnostics', { url: mirror.value.split('\n')[0], image: testImage.value, confirm_pull: Boolean(testImage.value) })
    if (alive) showDrawer('diagnostics', event)
  } catch (e) { if (alive) diagnosticError.value = e }
  finally { if (alive) diagnosticBusy.value = false }
}
async function readDaemon(event: Event) {
  daemonBusy.value = true
  diagnosticError.value = ''
  try { daemon.value = await api('/host/daemon'); if (alive) showDrawer('daemon', event) }
  catch (e) { if (alive) diagnosticError.value = e }
  finally { if (alive) daemonBusy.value = false }
}
function invalidatePreview() { mirrorRevision++; preview.value = null; if (drawer.value === 'preview') closeDrawer() }
async function previewDaemon(event: Event) {
  if (!canModify.value || previewBusy.value) return
  const revision = mirrorRevision
  previewBusy.value = true
  diagnosticError.value = ''
  preview.value = null
  try {
    const result = await post('/host/daemon/preview', { mirrors: mirror.value.split('\n').map(s => s.trim()).filter(Boolean) })
    if (alive && revision === mirrorRevision) { preview.value = result; showDrawer('preview', event) }
  } catch (e) { if (alive && revision === mirrorRevision) diagnosticError.value = e }
  finally { if (alive) previewBusy.value = false }
}
function confirmApply() { diagnosticError.value = ''; confirm.value = true }
async function apply() {
  if (actionBusy.value || !canModify.value || !preview.value) return
  actionBusy.value = true
  try {
    await post('/host/daemon/apply', { mirrors: mirror.value.split('\n').map(s => s.trim()).filter(Boolean), revision: preview.value.revision, confirm: true, confirm_restart: true })
    confirm.value = false
    preview.value = null
    auditDirty = true
    await load()
  } catch (e) { diagnosticError.value = e }
  finally { actionBusy.value = false }
}
onMounted(load)
onBeforeUnmount(() => { alive = false; loadRequest++; auditRequest++ })
</script>

<template>
  <p class="notice warning release-disclaimer" role="note">{{ t('settings.releaseDisclaimer') }}</p>
  <div class="settings-page-header">
    <h1>{{ t('nav.settings') }}</h1>
    <div class="actions">
      <n-button :loading="busy" @click="load"><AppIcon name="refresh" />{{ t('settings.refresh') }}</n-button>
      <button v-if="settings.capability_settings" id="settings-configure" type="button" class="file-button" @click="editor = true">{{ t('settingsEditor.configure') }}</button>
    </div>
  </div>
  <Notice :error="error" />
  <p v-if="savedNotice" class="notice settings-pending" role="status">{{ t(savedNotice) }}</p>
  <p v-else-if="settings.capability_settings?.pending_restart" class="notice warning settings-pending" role="status">{{ t('settingsEditor.pending') }}</p>
  <div class="tabs settings-tabs" role="tablist" :aria-label="t('settingsLayout.tabsLabel')">
    <button v-for="tab in tabs" :key="tab" :id="`settings-tab-${tab}`" type="button" role="tab" :class="{ active: activeTab === tab }" :aria-selected="activeTab === tab" :aria-controls="`settings-panel-${tab}`" :tabindex="activeTab === tab ? 0 : -1" @click="selectTab(tab)" @keydown="tabKey($event, tab)">{{ t(`settingsLayout.${tab}`) }}</button>
  </div>
  <div v-show="activeTab === 'overview'" id="settings-panel-overview" role="tabpanel" aria-labelledby="settings-tab-overview" tabindex="0">
    <div class="dashboard-grid settings-overview">
      <section class="panel">
        <div class="panel-title"><h3>{{ t('settings.platform') }}</h3></div>
        <dl class="facts settings-facts">
          <div v-for="key in ['version', 'mode', 'listen', 'data_dir']" :key="key">
            <dt>{{ { version: t('settings.version'), mode: t('settings.mode'), listen: t('settings.listen'), data_dir: t('settings.data_dir') }[key] }}</dt>
            <dd :title="str(settings[key])">{{ key === 'mode' ? settings.mode === 'binary' ? t('settings.binaryMode') : settings.mode === 'docker' ? t('settings.dockerMode') : str(settings.mode) : str(settings[key]) }}</dd>
          </div>
        </dl>
        <h4>{{ t('settings.paths') }}</h4>
        <div class="settings-paths">
          <div class="settings-path-summary">
            <div class="settings-path-heading"><strong>{{ t('settings.allowedDirectories') }}</strong><button v-if="allowedPaths.length" type="button" class="resource-name settings-path-details" @click="showDrawer('paths', $event)">{{ t('settingsLayout.viewAll') }} ({{ formatNumber(allowedPaths.length) }})</button></div>
            <p v-if="allowedPaths.length" class="muted">{{ t('settingsLayout.pathsCount', { count: formatNumber(allowedPaths.length) }) }}</p>
            <ul v-if="allowedPaths.length" class="path-list"><li v-for="(path, index) in allowedPaths.slice(0, 2)" :key="index"><code :title="path">{{ path }}</code></li></ul>
            <p v-else class="muted">{{ t('settings.noAllowedPaths') }}</p>
          </div>
          <div class="settings-path-summary">
            <div class="settings-path-heading"><strong>{{ t('settings.directoryMappings') }}</strong><button v-if="pathMappings.length" type="button" class="resource-name settings-path-details" @click="showDrawer('mappings', $event)">{{ t('settingsLayout.viewAll') }} ({{ formatNumber(pathMappings.length) }})</button></div>
            <p v-if="pathMappings.length" class="muted">{{ t('settingsLayout.mappingsCount', { count: formatNumber(pathMappings.length) }) }}</p>
            <ul v-if="pathMappings.length" class="path-list"><li v-for="(mapping, index) in pathMappings.slice(0, 2)" :key="index"><code :title="`${str(mapping.host)} → ${str(mapping.local)}`">{{ str(mapping.host) }} → {{ str(mapping.local) }}</code></li></ul>
            <p v-else class="muted">{{ settings.mode === 'binary' ? t('settings.binaryPaths') : t('settings.noPathMappings') }}</p>
          </div>
        </div>
      </section>
      <section class="panel table-panel capabilities-panel">
        <div class="panel-title"><h3>{{ t('settings.hostCapabilities') }}</h3></div>
        <div class="table-scroll"><table class="capability-table"><thead><tr><th>{{ t('settingsLayout.function') }}</th><th>{{ t('settingsLayout.status') }}</th><th>{{ t('settingsLayout.reason') }}</th><th>{{ t('settingsLayout.action') }}</th></tr></thead><tbody>
          <tr v-for="item in capabilityItems" :key="item.key" :id="`capability-${item.key}`">
            <td><strong>{{ item.name }}</strong></td><td><span class="tag" :class="item.tone">{{ item.status }}</span></td><td class="capability-short-reason" :title="item.available ? '' : item.reason">{{ item.shortReason }}</td><td><button type="button" class="resource-name capability-details-button" :aria-label="t('settingsLayout.detailsFor', { name: item.name })" @click="showDrawer('capability', $event, item.key)">{{ t('settingsLayout.details') }}</button></td>
          </tr>
        </tbody></table></div>
        <Notice :busy="busy && !capabilityItems.length" :empty="!busy && !error && !capabilityItems.length" />
      </section>
    </div>
  </div>
  <div v-show="activeTab === 'diagnostics'" id="settings-panel-diagnostics" role="tabpanel" aria-labelledby="settings-tab-diagnostics" tabindex="0">
    <section class="panel settings-diagnostics">
      <div class="panel-title"><h3>{{ t('settings.diagnostics') }}</h3><span class="eyebrow">{{ t('settings.diagnosticsCaption') }}</span></div>
      <p class="muted">{{ t('settings.diagnosticsInfo') }}</p>
      <div class="form-field"><label for="diagnostic-mirror">{{ t('settings.mirrorLabel') }}</label><n-input :input-props="{ id: 'diagnostic-mirror' }" v-model:value="mirror" type="textarea" :autosize="{ minRows: 2, maxRows: 5 }" placeholder="https://registry.example.com" @update:value="invalidatePreview" /></div>
      <div class="form-field"><label for="diagnostic-image">{{ t('settings.imageLabel') }}</label><n-input :input-props="{ id: 'diagnostic-image' }" v-model:value="testImage" placeholder="hello-world:latest" /></div>
      <div class="actions">
        <n-button :loading="diagnosticBusy" :disabled="!mirror || diagnosticBusy" @click="diagnose">{{ t('settings.diagnose') }}</n-button>
        <n-button :loading="daemonBusy" :disabled="daemonBusy" @click="readDaemon">{{ t('settings.readConfig') }}</n-button>
        <n-button :loading="previewBusy" :disabled="!canModify || previewBusy" @click="previewDaemon">{{ t('settings.previewConfig') }}</n-button>
        <n-button type="warning" :disabled="!canModify || !preview || actionBusy" @click="confirmApply">{{ t('settings.apply') }}</n-button>
      </div>
      <Notice :error="diagnosticError" />
      <p v-if="!canModify" class="notice warning">{{ t('settings.manualWarning') }}</p>
      <div v-for="[kind, , label] in resultItems" :key="kind" class="settings-result-summary">
        <div><strong>{{ t(label) }}</strong><p class="muted">{{ resultSummary(kind) }}</p></div>
        <button type="button" class="resource-name" :aria-label="t('settingsLayout.detailsFor', { name: t(label) })" @click="showDrawer(kind, $event)">{{ t('settingsLayout.details') }}</button>
      </div>
    </section>
  </div>
  <div v-show="activeTab === 'audit'" id="settings-panel-audit" role="tabpanel" aria-labelledby="settings-tab-audit" tabindex="0">
    <section class="panel table-panel">
      <div class="panel-title"><h3>{{ t('settings.audit') }}</h3><span class="eyebrow">{{ t('settings.auditCaption') }}</span></div>
      <div class="table-scroll"><table><thead><tr><th>{{ t('settings.actor') }}</th><th>{{ t('settings.actionTarget') }}</th><th>{{ t('settings.result') }}</th><th>{{ t('settings.time') }}</th></tr></thead><tbody><tr v-for="(audit, index) in audits" :key="index"><td>{{ str(audit.actor_id) }}</td><td>{{ str(audit.action) }}<code>{{ [audit.resource_type, audit.resource_id].filter(Boolean).join(' / ') || '—' }}</code></td><td>{{ displayState(audit.result) }}</td><td>{{ formatDateTime(audit.created_at) }}</td></tr></tbody></table></div>
      <Notice :error="auditError" :busy="auditBusy" :empty="!auditBusy && !auditError && !audits.length" />
      <div class="pagination"><span>{{ t('settings.pagination', { page: formatNumber(auditPage + 1), count: formatNumber(audits.length) }) }}</span><div class="actions"><n-button size="small" :disabled="auditBusy || auditPage === 0" @click="auditPage--; loadAudit()">{{ t('settings.previous') }}</n-button><n-button size="small" :disabled="auditBusy || !auditHasNext" @click="auditPage++; loadAudit()">{{ t('settings.next') }}</n-button></div></div>
    </section>
  </div>
  <n-drawer :show="drawer !== null" :width="'min(680px,100vw)'" :auto-focus="true" @update:show="value => !value && closeDrawer()" @after-leave="drawerClosed">
    <n-drawer-content :title="drawerTitle" closable>
      <div class="settings-detail-content">
        <template v-if="drawer === 'capability' && capabilityDetail">
          <span class="tag" :class="capabilityDetail.tone">{{ capabilityDetail.status }}</span>
          <p class="capability-reason">{{ capabilityDetail.reason }}</p>
          <template v-if="!capabilityDetail.available"><h3>{{ t('settingsLayout.resolution') }}</h3><p class="capability-resolution">{{ capabilityDetail.solution }}</p></template>
          <details class="capability-technical"><summary>{{ t('capabilities.technical') }}</summary><dl>
            <dt>{{ t('capabilities.identifier') }}</dt><dd><code>{{ capabilityDetail.key }}</code></dd>
            <dt v-if="capabilityDetail.reasonCode">{{ t('capabilities.reasonCode') }}</dt><dd v-if="capabilityDetail.reasonCode"><code>{{ capabilityDetail.reasonCode }}</code></dd>
            <dt v-if="capabilityDetail.rawReason">{{ t('capabilities.originalReason') }}</dt><dd v-if="capabilityDetail.rawReason"><code>{{ capabilityDetail.rawReason }}</code></dd>
          </dl></details>
          <n-button v-if="canConfigureDetail" @click="configureFromDrawer">{{ t('settingsEditor.configure') }}</n-button>
        </template>
        <template v-else-if="drawer === 'paths' || drawer === 'mappings'">
          <p class="muted">{{ t('settingsLayout.readOnly') }}</p>
          <ul v-if="drawer === 'paths'" class="path-list"><li v-for="(path, index) in allowedPaths" :key="index"><code>{{ path }}</code></li></ul>
          <ul v-else class="path-list"><li v-for="(mapping, index) in pathMappings" :key="index"><code>{{ str(mapping.host) }} → {{ str(mapping.local) }}</code></li></ul>
        </template>
        <template v-else-if="drawer === 'daemon' && daemon?.available === false">
          <p class="notice">{{ t('settings.manualGuide') }}</p>
          <details class="capability-technical"><summary>{{ t('capabilities.technical') }}</summary><pre class="json-block">{{ pretty(daemon) }}</pre></details>
        </template>
        <pre v-else-if="drawer === 'diagnostics'" class="json-block">{{ pretty(diagnostics) }}</pre>
        <pre v-else-if="drawer === 'daemon'" class="json-block">{{ pretty(daemon) }}</pre>
        <template v-else-if="drawer === 'preview'"><p class="notice warning">{{ t('settings.restartWarning') }}</p><pre class="json-block">{{ pretty(preview) }}</pre></template>
        <button type="button" class="file-button settings-detail-close" @click="closeDrawer">{{ t('settingsLayout.close') }}</button>
      </div>
    </n-drawer-content>
  </n-drawer>
  <ConfirmDialog v-model:show="confirm" :title="t('settings.confirmTitle')" :error="diagnosticError" :busy="actionBusy" :detail="t('settings.restartWarning')" @confirm="apply" />
  <CapabilitySettingsEditor v-if="settings.capability_settings" v-model:show="editor" :state="settings.capability_settings" @saved="configurationSaved" />
</template>
