<script setup lang="ts">
import { computed, ref, watch, onMounted, onUnmounted } from 'vue'
import { NButton } from 'naive-ui'
import { api, str, rows, state, bytes, ident, name, type Row } from '../api'
import AppIcon from '../components/AppIcon.vue'
import Notice from '../components/Notice.vue'
import ResourceTrend from '../components/ResourceTrend.vue'
import { appendSample, cpuValue, usage, sampleInterval, type ResourceKey, type ResourceSample } from '../dashboard-history'
import { t, formatDateTime, formatNumber, displayState, displayTaskType } from '../i18n'

const emit = defineEmits<{ navigate: [key: string] }>()
const data = ref<Row>({}), tasks = ref<Row[]>([]), error = ref<unknown>(''), taskError = ref<unknown>('')
const busy = ref(true), updated = ref<Date | null>(null), history = ref<ResourceSample[]>([]), clock = ref(Date.now())
const query = ref(''), containerItems = ref<Row[]>([]), containerBusy = ref(true), containerError = ref<unknown>(''), containerTotal = ref(0)
const host = computed(() => (data.value.host ?? {}) as Row)
const counts = computed(() => (data.value.containers ?? {}) as Row)
const backup = computed(() => (data.value.backup ?? {}) as Row)
const coverage = computed(() => (backup.value.coverage ?? {}) as Row)
const latest = computed(() => backup.value.latest as Row | null | undefined)
const memoryUsage = computed(() => usage(host.value.memory_total, host.value.memory_available))
const memory = computed(() => memoryUsage.value ? t('dashboard.memorySummary', { available: bytes(host.value.memory_available), total: bytes(host.value.memory_total) }) : '—')
const uptime = computed(() => typeof host.value.uptime === 'number' ? t('dashboard.uptime', { days: formatNumber(Math.floor(host.value.uptime / 86400)), hours: formatNumber(Math.floor(host.value.uptime % 86400 / 3600)) }) : '—')
const disk = computed(() => host.value.disk as Row | null | undefined)
const diskUsage = computed(() => usage(disk.value?.total, disk.value?.available))
const docker = computed(() => (data.value.docker ?? {}) as Row)
const version = computed(() => (docker.value.version ?? {}) as Row)
const currentCPU = computed(() => error.value ? null : cpuValue(host.value, clock.value, updated.value?.getTime() ?? clock.value))
const cpuUnavailable = computed(() => t(host.value.cpu_status === 'unsupported' ? 'dashboardConsole.cpuUnsupported' : host.value.cpu_status === 'warming_up' ? 'dashboardConsole.cpuWarming' : 'dashboardConsole.cpuStale'))
const metrics = computed(() => [
  { label: t('dashboard.running'), value: counts.value.running, route: 'containers', icon: 'activity', tone: 'green' },
  { label: t('dashboardConsole.stopped'), value: counts.value.stopped, route: 'containers', icon: 'containers', tone: 'red' },
  { label: t('dashboard.images'), value: data.value.images, route: 'images', icon: 'images', tone: 'blue' },
  { label: t('dashboard.volumes'), value: data.value.volumes, route: 'volumes', icon: 'volumes', tone: 'amber' },
])
const resources = computed(() => [
  { key: 'cpu' as ResourceKey, tone: 'green', label: t('dashboardConsole.cpu'), value: currentCPU.value === null ? '—' : `${formatNumber(currentCPU.value, { maximumFractionDigits: 1 })}%`, total: '', available: currentCPU.value !== null, unavailable: cpuUnavailable.value },
  ...([{ key: 'memory', tone: 'blue', usage: memoryUsage.value }, { key: 'disk', tone: 'amber', usage: diskUsage.value }] as const).map(item => ({ key: item.key, tone: item.tone, label: t(`dashboardConsole.${item.key}`), value: !error.value && item.usage ? bytes(item.usage.used) : '—', total: !error.value && item.usage ? bytes(item.usage.total) : '', available: !error.value && !!item.usage, unavailable: t('dashboardConsole.unavailable') })),
])
const facts = computed(() => [
  { label: t('dashboard.osArchitecture'), value: `${str(host.value.os)} / ${str(host.value.architecture)}` },
  { label: t('dashboard.dockerApiVersion'), value: `${str(version.value.Version)} / ${str(version.value.ApiVersion)}` },
  { label: t('dashboard.cpuLoad'), value: t('dashboard.cpuSummary', { count: numeric(host.value.cpus), load: numeric(host.value.load_1m) }) },
  { label: t('dashboard.hostMemory'), value: memory.value },
  { label: t('dashboard.hostUptime'), value: uptime.value },
  { label: t('dashboard.disk'), value: diskUsage.value ? `${bytes(disk.value?.available)} / ${bytes(disk.value?.total)}` : '—' },
  { label: t('dashboard.kernel'), value: str(host.value.kernel) },
  { label: t('dashboard.dockerRoot'), value: str(host.value.docker_root_dir), mono: true },
])
function numeric(value: unknown) { return typeof value === 'number' && Number.isFinite(value) ? formatNumber(value) : str(value) }
function ports(row: Row) { return rows(row.Ports).map(port => `${port.PublicPort ? `${str(port.PublicPort)} → ` : ''}${str(port.PrivatePort)}/${str(port.Type)}`).join(', ') || '—' }
function createdValue(row: Row) { return typeof row.Created === 'number' ? row.Created * 1000 : typeof row.Created === 'string' ? row.Created : '' }
function created(row: Row) { return formatDateTime(createdValue(row), { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) }
function createdISO(row: Row) { const date = new Date(createdValue(row)); return Number.isFinite(date.getTime()) ? date.toISOString() : undefined }
function inspect(row: Row) {
  try { sessionStorage.setItem('docker-manager:inspect', JSON.stringify({ kind: 'containers', id: ident(row) })); emit('navigate', 'containers') }
  catch { containerError.value = t('dashboardConsole.storageError') }
}
let mounted = false, loading = false, lastCPUSample = ''
let timer: ReturnType<typeof setInterval> | undefined, searchTimer: ReturnType<typeof setTimeout> | undefined
let summaryController: AbortController | undefined, taskController: AbortController | undefined, containerController: AbortController | undefined
let containerSequence = 0
async function loadContainers() {
  if (!mounted) return
  const sequence = ++containerSequence, search = query.value.trim()
  containerController?.abort(); const controller = new AbortController(); containerController = controller
  containerBusy.value = true; containerError.value = ''
  try {
    const result = await api(`/hosts/local/containers?page=1&page_size=5&q=${encodeURIComponent(search)}`, { signal: controller.signal })
    if (!mounted || sequence !== containerSequence || search !== query.value.trim()) return
    containerItems.value = rows(result).slice(0, 5)
    containerTotal.value = typeof result.total === 'number' ? result.total : containerItems.value.length
  } catch (reason) { if (mounted && sequence === containerSequence && !controller.signal.aborted) { containerError.value = reason; containerItems.value = [] } }
  finally { if (mounted && sequence === containerSequence) containerBusy.value = false }
}
async function loadTasks() {
  taskController?.abort()
  const controller = new AbortController(); taskController = controller
  const signal = controller.signal
  try { const result = await api('/tasks', { signal }); if (mounted && !signal.aborted) { tasks.value = rows(result).slice(0, 5); taskError.value = '' } }
  catch (reason) { if (mounted && !signal.aborted) taskError.value = reason }
}
async function load(record: boolean) {
  clock.value = Date.now()
  if (!mounted || loading) return
  loading = true; busy.value = true
  summaryController = new AbortController()
  void loadTasks()
  if (searchTimer === undefined) void loadContainers()
  try {
    const result = await api('/hosts/local/dashboard', { signal: summaryController.signal })
    if (!mounted) return
    data.value = result; error.value = docker.value.connected === false ? str(docker.value.error) : ''; updated.value = new Date(); clock.value = updated.value.getTime()
    if (record) {
      const cpu = currentCPU.value, stamp = str(host.value.cpu_sampled_at)
      history.value = appendSample(history.value, { at: updated.value.getTime(), cpu: stamp === lastCPUSample ? null : cpu, memory: error.value ? null : memoryUsage.value?.percent ?? null, disk: error.value ? null : diskUsage.value?.percent ?? null })
      if (cpu !== null) lastCPUSample = stamp
    }
  } catch (reason) {
    if (mounted && !summaryController.signal.aborted) {
      error.value = reason
      if (record) history.value = appendSample(history.value, { at: Date.now(), cpu: null, memory: null, disk: null })
    }
  } finally {
    loading = false; if (mounted) busy.value = false
  }
}
watch(query, () => {
  clearTimeout(searchTimer); containerSequence++; containerController?.abort()
  searchTimer = setTimeout(() => { searchTimer = undefined; void loadContainers() }, 300)
})
function visibilityChanged() {
  if (document.visibilityState === 'hidden') history.value = appendSample(history.value, { at: Date.now(), cpu: null, memory: null, disk: null })
  else void load(true)
}
onMounted(() => { mounted = true; void load(true); timer = setInterval(() => { if (document.visibilityState !== 'hidden') void load(true) }, sampleInterval); document.addEventListener('visibilitychange', visibilityChanged) })
onUnmounted(() => { mounted = false; clearInterval(timer); clearTimeout(searchTimer); summaryController?.abort(); taskController?.abort(); containerController?.abort(); document.removeEventListener('visibilitychange', visibilityChanged) })
</script>

<template>
  <section class="host-summary" :aria-label="t('dashboard.hostOverview')">
    <div class="host-identity">
      <h1>{{ t('nav.dashboard') }}</h1>
      <span class="host-name" :title="str(host.hostname ?? t('dashboard.localHost'))"><AppIcon name="server" />{{ host.hostname ?? t('dashboard.localHost') }}</span>
      <span class="tag" :class="busy ? 'pending' : error ? 'danger' : updated ? 'success' : 'pending'">{{ busy ? t('dashboard.reading') : error ? t('dashboard.connectionError') : updated ? t('dashboard.loaded') : t('dashboard.connecting') }}</span>
    </div>
    <n-button :loading="busy" @click="load(false)"><template #icon><AppIcon name="refresh" /></template>{{ t('dashboard.refresh') }}</n-button>
  </section>
  <p v-if="error" class="muted host-error-note">{{ t('dashboard.waitingDocker') }}</p>
  <Notice :error="error" :busy="busy && !updated" />
  <Notice v-if="data.resource_error" :error="t('dashboard.resourceError', { error: str(data.resource_error) })" />
  <div v-if="!error && updated" class="metrics">
    <button v-for="metric in metrics" :key="metric.label" class="metric-card" :class="`tone-${metric.tone}`" @click="emit('navigate', metric.route)">
      <span class="metric-icon"><AppIcon :name="metric.icon" /></span>
      <span class="metric-content"><span class="metric-heading">{{ metric.label }}</span><strong class="metric-value">{{ numeric(metric.value) }}</strong></span>
      <AppIcon class="metric-link" name="arrow-up-right" />
    </button>
  </div>
  <div class="resource-charts">
    <section v-for="resource in resources" :key="resource.key" class="panel resource-chart" :class="`tone-${resource.tone}`">
      <h3 class="resource-chart-heading">{{ resource.label }}</h3>
      <div class="resource-chart-value"><strong>{{ resource.value }}</strong><span v-if="resource.total"> / {{ resource.total }}</span></div>
      <ResourceTrend :history="history" :resource="resource.key" :label="resource.label" :available="resource.available" :unavailable="resource.unavailable" />
    </section>
  </div>
  <section class="panel dashboard-containers">
    <div class="panel-title">
      <div class="panel-heading"><h3>{{ t('dashboardConsole.containers') }} <span class="count container-count">{{ error ? '—' : numeric(counts.total) }}</span></h3></div>
      <div class="dashboard-container-tools"><label class="dashboard-search"><AppIcon name="search" /><input v-model="query" type="search" :aria-label="t('dashboardConsole.search')" :placeholder="t('dashboardConsole.searchPlaceholder')"></label><n-button text @click="emit('navigate', 'containers')">{{ t('dashboardConsole.allContainers') }}<AppIcon name="arrow-up-right" /></n-button></div>
    </div>
    <Notice :error="containerError" :busy="containerBusy && !containerItems.length" />
    <p v-if="query.trim() && !containerError && !containerBusy" class="footnote">{{ t('dashboardConsole.filteredCount', { count: formatNumber(containerTotal) }) }}</p>
    <div v-if="!containerError" class="table-scroll"><table class="dashboard-container-table"><thead><tr><th>{{ t('dashboardConsole.name') }}</th><th>{{ t('dashboardConsole.status') }}</th><th>{{ t('dashboardConsole.image') }}</th><th>{{ t('dashboardConsole.ports') }}</th><th>{{ t('dashboardConsole.created') }}</th><th class="align-right">{{ t('dashboardConsole.actions') }}</th></tr></thead><tbody>
      <tr v-for="container in containerItems" :key="ident(container)"><td><button class="resource-name container-name" :title="name(container)" @click="inspect(container)">{{ name(container) }}</button></td><td><span class="tag" :class="state(container).toLowerCase()">{{ displayState(state(container)) }}</span></td><td><span class="container-image mono" :title="str(container.Image)">{{ str(container.Image) }}</span></td><td><span class="container-ports mono" :title="ports(container)">{{ ports(container) }}</span></td><td><time class="container-created" :datetime="createdISO(container)">{{ created(container) }}</time></td><td class="align-right"><n-button size="small" text @click="inspect(container)">{{ t('dashboardConsole.view') }}<AppIcon name="arrow-up-right" /></n-button></td></tr>
    </tbody></table></div>
    <div v-if="!containerBusy && !containerError && !containerItems.length" class="quiet-empty dashboard-container-empty"><AppIcon name="inbox" />{{ t(query.trim() ? 'dashboardConsole.noMatches' : 'dashboardConsole.noContainers') }}</div>
  </section>
  <div class="dashboard-grid dashboard-details">
    <section class="panel host-facts">
      <div class="panel-title"><div class="panel-heading"><span class="panel-icon"><AppIcon name="server" /></span><h3>{{ t('dashboard.hostOverview') }}</h3></div></div>
      <dl class="facts"><div v-for="fact in facts" :key="fact.label"><dt>{{ fact.label }}</dt><dd :class="{ mono: fact.mono }">{{ fact.value }}</dd></div></dl>
      <p class="footnote">{{ t('dashboard.missingMetric') }}</p>
    </section>
    <section class="panel protection">
      <div class="panel-title"><div class="panel-heading"><span class="panel-icon"><AppIcon name="shield" /></span><h3>{{ t('dashboard.protection') }}</h3></div></div>
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
    <div class="panel-title"><div class="panel-heading"><span class="panel-icon"><AppIcon name="tasks" /></span><h3>{{ t('dashboard.recentTasks') }}<span class="count">{{ formatNumber(tasks.length) }}</span></h3></div><n-button text @click="emit('navigate', 'tasks')">{{ t('dashboard.allTasks') }}<AppIcon name="arrow-up-right" /></n-button></div>
    <Notice :error="taskError" />
    <div v-if="!taskError && !tasks.length" class="quiet-empty"><AppIcon name="inbox" /><span>{{ t('dashboard.noTasks') }}</span></div>
    <div v-for="task in tasks" :key="str(task.id)" class="activity-row">
      <span class="activity-icon"><AppIcon name="tasks" /></span>
      <strong class="activity-title">{{ displayTaskType(task.type ?? task.kind) }}</strong>
      <span class="muted activity-target" :title="str(task.target)" tabindex="0">{{ str(task.target) }}</span>
      <span class="tag" :class="state(task).toLowerCase()">{{ displayState(state(task)) }}</span>
    </div>
  </section>
  <div class="freshness"><AppIcon name="clock" />{{ updated ? t('dashboard.freshness', { time: formatDateTime(updated, { hour: '2-digit', minute: '2-digit', second: '2-digit' }) }) : t('dashboard.waitingHost') }}</div>
</template>
