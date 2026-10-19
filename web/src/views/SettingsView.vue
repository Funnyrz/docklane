<script setup lang="ts">
import { ref,onMounted,computed } from 'vue'
import { NButton,NInput } from 'naive-ui'
import { api,post,str,pretty,rows,type Row } from '../api'
import Notice from '../components/Notice.vue'
import AppIcon from '../components/AppIcon.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import CapabilitySettingsEditor from '../components/CapabilitySettingsEditor.vue'
import type { CapabilitySettingsState } from '../settings'
import { t, formatDateTime, formatNumber, displayState } from '../i18n'
import { describeCapabilities, type Capability } from '../capabilities'
interface SettingsResponse extends Row { capability_settings?: CapabilitySettingsState }
const auditPage=ref(0),auditHasNext=ref(false),auditBusy=ref(false),auditError=ref<unknown>(''),pageSize=50,actionBusy=ref(false),settings=ref<SettingsResponse>({}),audits=ref<Row[]>([]),error=ref<unknown>(''),busy=ref(false),mirror=ref(''),testImage=ref(''),diagnostics=ref<Row|null>(null),daemon=ref<Row|null>(null),preview=ref<Row|null>(null),confirm=ref(false)
const editor = ref(false), savedNotice = ref('')
function configurationSaved(value: Row & { capability_settings: CapabilitySettingsState }) {
  settings.value = value
  savedNotice.value = value.capability_settings.pending_restart ? 'settingsEditor.saved' : 'settingsEditor.restored'
  void loadAudit()
}
const capabilities = ref<Record<string, Capability>>({})
const capabilityItems = computed(() => describeCapabilities(capabilities.value))
const allowedPaths = computed(() => Array.isArray(settings.value.allowed_paths) ? settings.value.allowed_paths.map(str) : [])
const pathMappings = computed(() => rows(settings.value.path_mappings))
const canModify = computed(() => capabilities.value['host.daemon_config']?.available === true && capabilities.value['host.service_restart']?.available === true)
async function load(){busy.value=true;try{settings.value=await api('/settings');const c=await api<{capabilities?: Record<string, Capability>}>('/hosts/local/capabilities');capabilities.value=c.capabilities??{};await loadAudit();error.value=''}catch(e){error.value=e}finally{busy.value=false}}
async function loadAudit(){auditBusy.value=true;auditError.value='';try{const pageRows=rows(await api(`/audit?limit=${pageSize+1}&offset=${auditPage.value*pageSize}`));auditHasNext.value=pageRows.length>pageSize;audits.value=pageRows.slice(0,pageSize)}catch(e){auditError.value=e}finally{auditBusy.value=false}}
async function diagnose(){busy.value=true;try{diagnostics.value=await post('/host/diagnostics',{url:mirror.value.split('\n')[0],image:testImage.value,confirm_pull:Boolean(testImage.value)})}catch(e){error.value=e}finally{busy.value=false}}
async function readDaemon(){try{daemon.value=await api('/host/daemon')}catch(e){error.value=e}}
async function previewDaemon(){try{preview.value=await post('/host/daemon/preview',{mirrors:mirror.value.split('\n').map(s=>s.trim()).filter(Boolean)})}catch(e){error.value=e}}
async function apply(){if(actionBusy.value)return;actionBusy.value=true;try{await post('/host/daemon/apply',{mirrors:mirror.value.split('\n').map(s=>s.trim()).filter(Boolean),revision:preview.value?.revision,confirm:true,confirm_restart:true});confirm.value=false;await load()}catch(e){error.value=e}finally{actionBusy.value=false}}
onMounted(load)
</script>
<template>
  <div class="toolbar">
    <p class="muted">{{ t('settings.capabilityInfo') }}</p>
    <n-button @click="load" :loading="busy"><AppIcon name="refresh" />{{ t('settings.refresh') }}</n-button>
  </div>
  <Notice :error="error" />
  <div class="dashboard-grid settings-overview">
    <section class="panel">
      <div class="panel-title"><h3>{{ t('settings.platform') }}</h3><n-button v-if="settings.capability_settings" size="small" @click="editor=true">{{ t('settingsEditor.configure') }}</n-button><span v-else class="eyebrow">{{ t('settings.application') }}</span></div>
      <p v-if="savedNotice" class="notice" role="status">{{ t(savedNotice) }}</p>
      <p v-else-if="settings.capability_settings?.pending_restart" class="notice warning" role="status">{{ t('settingsEditor.pending') }}</p>
      <dl class="facts">
        <div v-for="key in ['version','mode','listen','data_dir']" :key="key">
          <dt>{{ {version:t('settings.version'),mode:t('settings.mode'),listen:t('settings.listen'),data_dir:t('settings.data_dir')}[key] }}</dt>
          <dd>{{ key === 'mode' ? settings.mode === 'binary' ? t('settings.binaryMode') : settings.mode === 'docker' ? t('settings.dockerMode') : str(settings.mode) : str(settings[key]) }}</dd>
        </div>
      </dl>
      <h4>{{ t('settings.paths') }}</h4>
      <div class="settings-paths">
        <strong>{{ t('settings.allowedDirectories') }}</strong>
        <ul v-if="allowedPaths.length" class="path-list"><li v-for="path in allowedPaths" :key="path"><code>{{ path }}</code></li></ul>
        <p v-else class="muted">{{ t('settings.noAllowedPaths') }}</p>
        <strong>{{ t('settings.directoryMappings') }}</strong>
        <ul v-if="pathMappings.length" class="path-list"><li v-for="(mapping,i) in pathMappings" :key="i"><code>{{ str(mapping.host) }} → {{ str(mapping.local) }}</code></li></ul>
        <p v-else class="muted">{{ settings.mode === 'binary' ? t('settings.binaryPaths') : t('settings.noPathMappings') }}</p>
      </div>
    </section>
    <section class="panel capabilities-panel">
      <div class="panel-title"><h3>{{ t('settings.hostCapabilities') }}</h3></div>
      <div v-for="item in capabilityItems" :key="item.key" class="capability" :id="`capability-${item.key}`" role="group" :aria-label="item.name">
        <div class="capability-heading"><strong>{{ item.name }}</strong><span class="tag" :class="item.tone">{{ item.status }}</span></div>
        <p class="capability-reason">{{ item.reason }}</p>
        <details v-if="!item.available" class="capability-help">
          <summary>{{ t('capabilities.solution') }}</summary>
          <p>{{ item.solution }}</p>
          <details class="capability-technical">
            <summary>{{ t('capabilities.technical') }}</summary>
            <dl>
              <dt>{{ t('capabilities.identifier') }}</dt><dd><code>{{ item.key }}</code></dd>
              <dt v-if="item.reasonCode">{{ t('capabilities.reasonCode') }}</dt><dd v-if="item.reasonCode"><code>{{ item.reasonCode }}</code></dd>
              <dt v-if="item.rawReason">{{ t('capabilities.originalReason') }}</dt><dd v-if="item.rawReason"><code>{{ item.rawReason }}</code></dd>
            </dl>
          </details>
        </details>
        <n-button v-if="settings.capability_settings && ['ALLOWED_PATHS_MISSING','PATH_MAPPINGS_MISSING','DAEMON_WRITE_NOT_AUTHORIZED'].includes(item.reasonCode ?? '')" size="small" class="capability-configure" @click="editor=true">{{ t('settingsEditor.configure') }}</n-button>
      </div>
      <p v-if="capabilityItems.some(item => !item.available)" class="footnote">{{ t('capabilities.recheck') }}</p>
    </section>
  </div>
  <section class="panel section-gap">
    <div class="panel-title"><h3>{{ t('settings.diagnostics') }}</h3><span class="eyebrow">{{ t('settings.diagnosticsCaption') }}</span></div>
    <p class="muted">{{ t('settings.diagnosticsInfo') }}</p>
    <div class="form-field"><label for="diagnostic-mirror">{{ t('settings.mirrorLabel') }}</label><n-input :input-props="{id:'diagnostic-mirror'}" v-model:value="mirror" type="textarea" :autosize="{minRows:2}" placeholder="https://registry.example.com" @update:value="preview=null" /></div>
    <div class="form-field"><label for="diagnostic-image">{{ t('settings.imageLabel') }}</label><n-input :input-props="{id:'diagnostic-image'}" v-model:value="testImage" placeholder="hello-world:latest" /></div>
    <div class="actions">
      <n-button :loading="busy" :disabled="!mirror" @click="diagnose">{{ t('settings.diagnose') }}</n-button>
      <n-button @click="readDaemon">{{ t('settings.readConfig') }}</n-button>
      <n-button :disabled="!canModify" @click="previewDaemon">{{ t('settings.previewConfig') }}</n-button>
      <n-button type="warning" :disabled="!canModify||!preview" @click="error='';confirm=true">{{ t('settings.apply') }}</n-button>
    </div>
    <p v-if="!canModify" class="notice warning">{{ t('settings.manualWarning') }}</p>
    <pre v-if="diagnostics" class="json-block">{{ pretty(diagnostics) }}</pre>
    <template v-if="daemon">
      <template v-if="daemon.available === false">
        <p class="notice">{{ t('settings.manualGuide') }}</p>
        <details class="capability-technical"><summary>{{ t('capabilities.technical') }}</summary><pre class="json-block">{{ pretty(daemon) }}</pre></details>
      </template>
      <pre v-else class="json-block">{{ pretty(daemon) }}</pre>
    </template>
    <pre v-if="preview" class="json-block">{{ pretty(preview) }}</pre>
  </section>
  <section class="panel table-panel section-gap">
    <div class="panel-title"><h3>{{ t('settings.audit') }}</h3><span class="eyebrow">{{ t('settings.auditCaption') }}</span></div>
    <div class="table-scroll"><table><thead><tr><th>{{ t('settings.actor') }}</th><th>{{ t('settings.actionTarget') }}</th><th>{{ t('settings.result') }}</th><th>{{ t('settings.time') }}</th></tr></thead><tbody><tr v-for="(audit,i) in audits" :key="i"><td>{{ str(audit.actor_id) }}</td><td>{{ str(audit.action) }}<code>{{ [audit.resource_type,audit.resource_id].filter(Boolean).join(' / ')||'—' }}</code></td><td>{{ displayState(audit.result) }}</td><td>{{ formatDateTime(audit.created_at) }}</td></tr></tbody></table></div>
    <Notice :error="auditError" :busy="auditBusy" :empty="!auditBusy&&!auditError&&!audits.length" />
    <div class="pagination"><span>{{ t('settings.pagination', { page: formatNumber(auditPage+1), count: formatNumber(audits.length) }) }}</span><div class="actions"><n-button size="small" :disabled="auditBusy||auditPage===0" @click="auditPage--;loadAudit()">{{ t('settings.previous') }}</n-button><n-button size="small" :disabled="auditBusy||!auditHasNext" @click="auditPage++;loadAudit()">{{ t('settings.next') }}</n-button></div></div>
  </section>
  <ConfirmDialog v-model:show="confirm" :title="t('settings.confirmTitle')" :error="error" :busy="actionBusy" :detail="t('settings.restartWarning')" @confirm="apply" />
  <CapabilitySettingsEditor v-if="settings.capability_settings" v-model:show="editor" :state="settings.capability_settings" @saved="configurationSaved" />
</template>
