<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { NButton } from 'naive-ui'
import { api, str, rows, state, bytes, type Row } from '../api'
import AppIcon from '../components/AppIcon.vue'
import Notice from '../components/Notice.vue'
import { t, formatDateTime, formatNumber, displayState, displayTaskType } from '../i18n'

const emit = defineEmits<{ navigate: [key: string] }>()
const data = ref<Row>({})
const tasks = ref<Row[]>([])
const error = ref<unknown>('')
const busy = ref(true)
const updated = ref<Date | null>(null)
const host = computed(() => (data.value.host ?? {}) as Row)
const counts = computed(() => (data.value.containers ?? {}) as Row)
const backup = computed(() => (data.value.backup ?? {}) as Row)
const coverage = computed(() => (backup.value.coverage ?? {}) as Row)
const latest = computed(() => backup.value.latest as Row | null | undefined)
const memory = computed(() => typeof host.value.memory_total === 'number' ? t('dashboard.memorySummary', { available: bytes(host.value.memory_available), total: bytes(host.value.memory_total) }) : '—')
const uptime = computed(() => typeof host.value.uptime === 'number' ? t('dashboard.uptime', { days: formatNumber(Math.floor(host.value.uptime / 86400)), hours: formatNumber(Math.floor(host.value.uptime % 86400 / 3600)) }) : '—')
const disk = computed(() => host.value.disk as Row | null | undefined)
const docker = computed(() => (data.value.docker ?? {}) as Row)
const version = computed(() => (docker.value.version ?? {}) as Row)
const metrics = computed(() => [
  { label: t('dashboard.allContainers'), value: counts.value.total, caption: t('dashboard.containersCaption'), route: 'containers', icon: 'containers', tone: 'blue' },
  { label: t('dashboard.running'), value: counts.value.running, caption: t('dashboard.runningCaption'), route: 'containers', icon: 'activity', tone: 'green' },
  { label: t('dashboard.images'), value: data.value.images, caption: t('dashboard.imagesCaption'), route: 'images', icon: 'images', tone: 'violet' },
  { label: t('dashboard.volumes'), value: data.value.volumes, caption: t('dashboard.volumesCaption'), route: 'volumes', icon: 'volumes', tone: 'slate' },
])
const facts = computed(() => [
  { label: t('dashboard.osArchitecture'), value: `${str(host.value.os)} / ${str(host.value.architecture)}` },
  { label: t('dashboard.dockerApiVersion'), value: `${str(version.value.Version)} / ${str(version.value.ApiVersion)}` },
  { label: t('dashboard.cpuLoad'), value: t('dashboard.cpuSummary', { count: numeric(host.value.cpus), load: numeric(host.value.load_1m) }) },
  { label: t('dashboard.hostMemory'), value: memory.value },
  { label: t('dashboard.hostUptime'), value: uptime.value },
  { label: t('dashboard.disk'), value: disk.value ? `${bytes(disk.value.available)} / ${bytes(disk.value.total)}` : '—' },
  { label: t('dashboard.kernel'), value: str(host.value.kernel) },
  { label: t('dashboard.dockerRoot'), value: str(host.value.docker_root_dir), mono: true },
])
function numeric(value: unknown) { return typeof value === 'number' ? formatNumber(value) : str(value) }
async function load() {
  busy.value = true
  error.value = ''
  try {
    data.value = await api('/hosts/local/dashboard')
    if (docker.value.connected === false) error.value = str(docker.value.error)
    updated.value = new Date()
  } catch (reason) { error.value = reason }
  finally { busy.value = false }
  try { tasks.value = rows(await api('/tasks')).slice(0, 5) }
  catch { tasks.value = [] }
}
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => { void load(); timer = setInterval(load, 15000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section class="host-banner">
    <div class="host-identity">
      <span class="host-symbol"><AppIcon name="server" /></span>
      <div class="host-copy">
        <span class="eyebrow">{{ t('dashboard.hostEyebrow') }}</span>
        <h2>{{ host.hostname ?? t('dashboard.localHost') }}<span class="tag" :class="busy ? 'pending' : error ? 'danger' : updated ? 'success' : 'pending'">{{ busy ? t('dashboard.reading') : error ? t('dashboard.connectionError') : updated ? t('dashboard.loaded') : t('dashboard.connecting') }}</span></h2>
        <p>{{ error ? t('dashboard.waitingDocker') : t('dashboard.discoveryInfo') }}</p>
      </div>
    </div>
    <n-button :loading="busy" @click="load"><template #icon><AppIcon name="refresh" /></template>{{ t('dashboard.refresh') }}</n-button>
  </section>
  <Notice :error="error" :busy="busy && !updated" />
  <Notice v-if="data.resource_error" :error="t('dashboard.resourceError', { error: str(data.resource_error) })" />
  <div v-if="!error && updated" class="metrics">
    <button v-for="metric in metrics" :key="metric.label" class="metric-card" :class="`metric-${metric.tone}`" @click="emit('navigate', metric.route)">
      <span class="metric-heading"><span>{{ metric.label }}</span><span class="metric-icon"><AppIcon :name="metric.icon" /></span></span>
      <strong class="metric-value">{{ numeric(metric.value) }}</strong>
      <span class="metric-caption"><span>{{ metric.caption }}</span><AppIcon class="metric-link" name="arrow-up-right" /></span>
    </button>
  </div>
  <div class="dashboard-grid">
    <section class="panel">
      <div class="panel-title"><div class="panel-heading"><span class="panel-icon"><AppIcon name="server" /></span><h3>{{ t('dashboard.hostOverview') }}</h3></div><span class="eyebrow">{{ t('dashboard.systemProfile') }}</span></div>
      <dl class="facts"><div v-for="fact in facts" :key="fact.label"><dt>{{ fact.label }}</dt><dd :class="{ mono: fact.mono }">{{ fact.value }}</dd></div></dl>
      <p class="footnote">{{ t('dashboard.missingMetric') }}</p>
    </section>
    <section class="panel protection">
      <div class="panel-title"><div class="panel-heading"><span class="panel-icon"><AppIcon name="shield" /></span><h3>{{ t('dashboard.protection') }}</h3></div><span class="eyebrow">{{ t('dashboard.backupRecovery') }}</span></div>
      <h2>{{ latest ? t('dashboard.recoveryPoint') : t('dashboard.backupStart') }}</h2>
      <div class="backup-summary">
        <div><span>{{ t('dashboard.unprotected') }}</span><strong>{{ numeric(backup.unprotected) }}</strong></div>
        <div><span>{{ t('dashboard.latestVerified') }}</span><strong class="small-value">{{ latest ? formatDateTime(latest.created_at) : t('dashboard.noRecord') }}</strong></div>
      </div>
      <p v-if="coverage.basis" class="muted">{{ t('dashboard.coverage', { protectedContainers: numeric((coverage.containers as Row | undefined)?.protected), totalContainers: numeric((coverage.containers as Row | undefined)?.total), protectedVolumes: numeric((coverage.volumes as Row | undefined)?.protected), totalVolumes: numeric((coverage.volumes as Row | undefined)?.total) }) }}<br>{{ t('dashboard.coverageNote') }}</p>
      <p v-else class="muted">{{ t('dashboard.createPolicy') }}<br>{{ t('dashboard.findRecovery') }}</p>
      <n-button type="primary" @click="emit('navigate', 'backups')">{{ t('dashboard.manageBackups') }}<AppIcon name="arrow-right" /></n-button>
      <small class="protection-note"><AppIcon name="shield" />{{ t('dashboard.consistencyWarning') }}</small>
    </section>
  </div>
  <section class="panel recent-tasks">
    <div class="panel-title"><div class="panel-heading"><span class="panel-icon"><AppIcon name="tasks" /></span><h3>{{ t('dashboard.recentTasks') }}</h3></div><n-button text @click="emit('navigate', 'tasks')">{{ t('dashboard.allTasks') }}<AppIcon name="arrow-up-right" /></n-button></div>
    <div v-if="!tasks.length" class="quiet-empty"><AppIcon name="inbox" /><span>{{ t('dashboard.noTasks') }}</span></div>
    <div v-for="task in tasks" :key="str(task.id)" class="activity-row">
      <span class="activity-icon"><AppIcon name="tasks" /></span>
      <strong class="activity-title">{{ displayTaskType(task.type ?? task.kind) }}</strong>
      <span class="muted activity-target">{{ str(task.target) }}</span>
      <span class="tag" :class="state(task).toLowerCase()">{{ displayState(state(task)) }}</span>
    </div>
  </section>
  <div class="freshness"><AppIcon name="clock" />{{ updated ? t('dashboard.freshness', { time: formatDateTime(updated, { hour: '2-digit', minute: '2-digit', second: '2-digit' }) }) : t('dashboard.waitingHost') }}</div>
</template>
