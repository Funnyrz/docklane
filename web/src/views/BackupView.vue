<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { NButton, NDrawer, NDrawerContent, NInput, NSelect, NCheckbox, NInputNumber } from 'naive-ui'
import { api, post, rows, name, ident, str, pretty, type Row } from '../api'
import { t, localized, renderText, formatDateTime, formatNumber } from '../i18n'
import Notice from '../components/Notice.vue'
import AppIcon from '../components/AppIcon.vue'
import FormDialog, { type FormField } from '../components/FormDialog.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'

interface Target { type: string; path?: string; name?: string }
interface Policy {
  id?: string; name: string; repository_id: string; targets: Target[]; consistency_mode: string
  schedule: string; timezone: string; keep_last: number | null; excludes: string[]
  retries: number | null; retry_seconds: number | null; enabled: boolean
}
const repositories = ref<Row[]>([]), policies = ref<Row[]>([]), snapshots = ref<Row[]>([]), schedules = ref<Row[]>([])
const error = ref<unknown>(''), busy = ref(false), feedback = ref<unknown>(''), selectedRepository = ref(''), snapshotOpen = ref(false)
const actionBusy = ref(false), confirm = ref(false), confirmTitle = ref<unknown>(''), confirmError = ref<unknown>('')
const confirmDetailBuilder = ref<() => string>(() => '')
const confirmDetail = computed(() => confirmDetailBuilder.value())
const confirmAction = ref<() => Promise<void>>(async () => {})
const repositoryForm = ref(false)
const repositoryFields: FormField[] = [
  { key: 'name', label: localized('backup.repositoryName'), required: true },
  { key: 'location', label: localized('backup.repositoryPath'), required: true, placeholder: '/backups/restic' },
  { key: 'password', label: localized('backup.repositoryPassword'), type: 'password', required: true, hint: localized('backup.passwordHint') },
  { key: 'import', label: localized('backup.importRepository'), type: 'checkbox' },
]
const repositoryOptions = computed(() => repositories.value.map(r => ({ label: name(r), value: ident(r) })))
const targetOptions = computed(() => [{ label: t('backup.directory'), value: 'directory' }, { label: t('backup.volume'), value: 'volume' }, { label: t('backup.container'), value: 'container' }, { label: 'Compose', value: 'compose' }])
const policyOpen = ref(false), policyBusy = ref(false), policyError = ref<unknown>(''), exclusions = ref('')
function freshPolicy(): Policy { return { name: '', repository_id: '', targets: [{ type: 'directory', path: '' }], consistency_mode: 'file', schedule: '', timezone: 'Asia/Shanghai', keep_last: 7, excludes: [], retries: 0, retry_seconds: 30, enabled: true } }
const policyDraft = ref<Policy>(freshPolicy())
function editPolicy(policy?: Row) {
  policyDraft.value = policy ? { ...freshPolicy(), ...JSON.parse(JSON.stringify(policy)) } as Policy : freshPolicy()
  exclusions.value = policyDraft.value.excludes.join('\n'); policyError.value = ''; policyOpen.value = true
}
function updateTarget(target: Target, value: string) { if (target.type === 'directory') target.path = value; else target.name = value }
function changeTargetType(target: Target, type: string) { target.type = type; delete target.name; delete target.path }
async function savePolicy() {
  if (policyBusy.value) return
  const draft = policyDraft.value
  if (!draft.name || !draft.repository_id || !draft.targets.length || draft.targets.some(t => !String(t.type === 'directory' ? t.path ?? '' : t.name ?? '').trim())) { policyError.value = localized('backup.policyRequired'); return }
  policyBusy.value = true; policyError.value = ''
  try {
    const body = { ...draft, excludes: exclusions.value.split('\n').map(x => x.trim()).filter(Boolean) }
    if (draft.id) await api(`/backup/policies/${draft.id}`, { method: 'PUT', body: JSON.stringify(body) })
    else await post('/backup/policies', body)
    policyOpen.value = false; feedback.value = draft.id ? localized('backup.policyUpdated') : localized('backup.policyCreated'); await load()
  } catch (e) { policyError.value = e } finally { policyBusy.value = false }
}
function danger(title: unknown, detail: () => string, action: () => Promise<void>) { confirmTitle.value = title; confirmDetailBuilder.value = detail; confirmError.value = ''; confirmAction.value = action; confirm.value = true }
async function execute() {
  if (actionBusy.value) return
  actionBusy.value = true
  try { await confirmAction.value(); confirm.value = false; feedback.value = localized('backup.actionSubmitted'); await load() }
  catch (e) { error.value = e; confirmError.value = e }
  finally { actionBusy.value = false }
}
async function load() {
  busy.value = true
  try {
    repositories.value = rows(await api('/backup/repositories')); policies.value = rows(await api('/backup/policies')); schedules.value = rows(await api('/backup/schedule-instances')); error.value = ''
  } catch (e) { error.value = e } finally { busy.value = false }
}
async function createRepository(value: Row) { await post('/backup/repositories', value) }
async function viewSnapshots(repo: Row) {
  selectedRepository.value = ident(repo); snapshotOpen.value = true; snapshots.value = []; error.value = ''
  try { snapshots.value = rows(await api(`/backup/repositories/${ident(repo)}/snapshots`)) } catch (e) { error.value = e }
}
async function exportRecovery(repo: Row) {
  try {
    const recovery = await post(`/backup/repositories/${ident(repo)}/export`, { confirm: true })
    const blob = new Blob([pretty(recovery)], { type: 'application/json' }), url = URL.createObjectURL(blob), anchor = document.createElement('a')
    anchor.href = url; anchor.download = `docklane-recovery-${ident(repo)}.json`; anchor.click(); URL.revokeObjectURL(url)
    feedback.value = localized('backup.recoveryDownloaded')
  } catch (e) { error.value = e }
}
async function runPolicy(policy: Row) {
  try {
    const plan = await post(`/backup/policies/${ident(policy)}/preview`)
    danger(localized('backup.runBackup'), () => t('backup.runDetail', { name: name(policy), mode: t(policy.consistency_mode === 'stop' ? 'backup.stopModeWarning' : 'backup.fileModeWarning'), warning: str(plan.warning), containers: pretty(plan.stop_containers ?? []), sources: pretty(plan.sources ?? []) }), async () => { await post(`/backup/policies/${ident(policy)}/run`, { confirm: true, revision: plan.revision }) })
  } catch (e) { error.value = e }
}
function deletePolicy(policy: Row) {
  danger(localized('backup.deletePolicy'), () => t('backup.deletePolicyDetail', { name: name(policy) }), async () => { await api(`/backup/policies/${ident(policy)}`, { method: 'DELETE', body: JSON.stringify({ confirm: true }) }) })
}

const restoreOpen = ref(false), restoreSnapshot = ref(''), restoreMode = ref('new'), target = ref(''), source = ref('')
const metadata = ref<Row | null>(null), metadataBusy = ref(false), metadataError = ref<unknown>(''), restoreError = ref<unknown>(''), previewBusy = ref(false), restorePreview = ref<Row | null>(null)
const acknowledgeUnverified = ref(false)
const unverified = computed(() => Boolean(metadata.value) && metadata.value?.verification_status !== 'verified')
const recreate = ref(false), selectedContainers = ref<string[]>([]), selectedProjects = ref<string[]>([])
const containerNames = ref<Record<string, string>>({}), projectNames = ref<Record<string, string>>({})
const sources = computed(() => rows(metadata.value?.sources))
const recoveryContainers = computed(() => rows(metadata.value?.containers).filter(row => !row.compose_project))
const recoveryProjects = computed(() => rows(metadata.value?.projects))
const sourceOptions = computed(() => sources.value.map(row => ({ label: `${str(row.host_path ?? row.volume)} · ${str(row.snapshot_path)}`, value: str(row.snapshot_path) })))
const chosenSource = computed(() => sources.value.find(row => row.snapshot_path === source.value))
function invalidatePreview() { restorePreview.value = null; restoreError.value = '' }
function chooseSource(value: string) { source.value = value; if (restoreMode.value === 'original') target.value = String(chosenSource.value?.host_path ?? ''); invalidatePreview() }
function changeRestoreMode(value: string) {
  restoreMode.value = value; invalidatePreview()
  if (value === 'original') { recreate.value = false; target.value = String(chosenSource.value?.host_path ?? '') }
  else target.value = ''
}
function selectResource(kind: 'containers' | 'projects', id: string, checked: boolean) {
  const selection = kind === 'containers' ? selectedContainers : selectedProjects
  selection.value = checked ? [...new Set([...selection.value, id])] : selection.value.filter(value => value !== id)
  invalidatePreview()
}
async function loadMetadata() {
  metadataBusy.value = true; metadataError.value = ''
  try {
    metadata.value = await api(`/backup/repositories/${encodeURIComponent(selectedRepository.value)}/snapshots/${encodeURIComponent(restoreSnapshot.value)}/metadata`)
    source.value = sources.value.length === 1 ? str(sources.value[0]!.snapshot_path) : ''
    selectedContainers.value = recoveryContainers.value.map(row => str(row.id)); selectedProjects.value = recoveryProjects.value.map(row => str(row.id))
    if (restoreMode.value === 'original') target.value = String(chosenSource.value?.host_path ?? '')
  } catch (e) { metadataError.value = e } finally { metadataBusy.value = false }
}
async function restore(snapshot: Row) {
  restoreSnapshot.value = ident(snapshot); restoreOpen.value = true; acknowledgeUnverified.value = false; restoreMode.value = 'new'; target.value = ''; source.value = ''; restorePreview.value = null
  metadata.value = null; recreate.value = false; selectedContainers.value = []; selectedProjects.value = []; containerNames.value = {}; projectNames.value = {}; restoreError.value = ''
  await loadMetadata()
}
function selectedNames(names: Record<string, string>, selected: string[]) { return Object.fromEntries(Object.entries(names).filter(([id, value]) => selected.includes(id) && value.trim()).map(([id, value]) => [id, value.trim()])) }
function restoreBody() {
  return {
    acknowledge_unverified: unverified.value && acknowledgeUnverified.value,
    repository_id: selectedRepository.value, snapshot: restoreSnapshot.value, target: target.value, overwrite: restoreMode.value === 'original',
    source: restoreMode.value === 'original' ? source.value : undefined, recreate: recreate.value && restoreMode.value === 'new',
    container_ids: recreate.value ? [...selectedContainers.value] : [], project_ids: recreate.value ? [...selectedProjects.value] : [],
    container_names: selectedNames(containerNames.value, selectedContainers.value), project_names: selectedNames(projectNames.value, selectedProjects.value),
  }
}
const canPreview = computed(() => Boolean(target.value) && Boolean(metadata.value) && !metadataBusy.value && (!unverified.value || (acknowledgeUnverified.value && restoreMode.value === 'new' && !recreate.value)) && (restoreMode.value !== 'original' || Boolean(source.value)) && (!recreate.value || Boolean(metadata.value)))
async function previewRestore() {
  if (!canPreview.value || previewBusy.value) return
  previewBusy.value = true; restoreError.value = ''; restorePreview.value = null
  try { restorePreview.value = await post('/backup/restore/preview', restoreBody()) }
  catch (e) { restoreError.value = e } finally { previewBusy.value = false }
}
function applyRestore() {
  if (!restorePreview.value) return
  const body = restoreBody(), revision = restorePreview.value.revision
  danger(localized('backup.confirmRestore'), () => t('backup.restoreDetail', { warning: unverified.value ? t('backup.unverifiedRestoreWarning') : '', snapshot: restoreSnapshot.value, target: target.value, mode: restoreMode.value === 'original' ? t('backup.originalRestoreWarning', { source: source.value }) : t('backup.newRestore'), services: t(recreate.value ? 'backup.recreateServicesWarning' : 'backup.filesOnlyWarning') }), async () => { await post('/backup/restore', { ...body, revision, confirm: true, confirm_overwrite: body.overwrite ? body.target : undefined }); restoreOpen.value = false })
}
onMounted(load)
</script>

<template>
  <div class="toolbar"><p class="muted">{{ t('backup.tagline') }}</p><div class="actions"><n-button @click="load" :loading="busy"><AppIcon name="refresh"/>{{ t('backup.refresh') }}</n-button><n-button @click="repositoryForm = true"><AppIcon name="plus"/>{{ t('backup.addRepository') }}</n-button><n-button type="primary" :disabled="!repositories.length" @click="editPolicy()"><AppIcon name="plus"/>{{ t('backup.createPolicy') }}</n-button></div></div>
  <Notice :error="error"/><p v-if="feedback" class="notice success" role="status">{{ renderText(feedback) }}</p>
  <div class="notice warning"><b>{{ t('backup.consistencyNotice') }}</b><span>{{ t('backup.consistencyWarning') }}</span></div>
  <section class="panel table-panel"><div class="panel-title"><h3>{{ t('backup.policies') }} <span class="count">{{ formatNumber(policies.length) }}</span></h3><span class="eyebrow">{{ t('backup.policyEyebrow') }}</span></div><div class="table-scroll"><table><thead><tr><th>{{ t('backup.nameTargets') }}</th><th>{{ t('backup.consistency') }}</th><th>{{ t('backup.scheduleTimezone') }}</th><th>{{ t('backup.retention') }}</th><th>{{ t('backup.actions') }}</th></tr></thead><tbody><tr v-for="policy in policies" :key="ident(policy)"><td><strong>{{ name(policy) }}</strong><code>{{ str(policy.targets) }}</code></td><td><span class="tag" :class="policy.consistency_mode === 'stop' ? 'success' : 'warning'">{{ policy.consistency_mode === 'stop' ? t('backup.stopBackup') : t('backup.fileLevel') }}</span><code v-if="!policy.enabled">{{ t('backup.scheduleDisabled') }}</code></td><td>{{ policy.schedule || t('backup.manual') }}<code>{{ str(policy.timezone) }}</code></td><td>{{ t('backup.snapshotCount', { count: formatNumber(Number(policy.keep_last)) }) }}</td><td><div class="row-actions"><n-button size="small" @click="editPolicy(policy)">{{ t('backup.edit') }}</n-button><n-button size="small" @click="runPolicy(policy)">{{ t('backup.backupNow') }}</n-button><n-button size="small" type="error" quaternary @click="deletePolicy(policy)">{{ t('backup.delete') }}</n-button></div></td></tr></tbody></table></div><Notice :busy="busy" :empty="!busy && !policies.length"/></section>
  <section class="panel table-panel section-gap"><div class="panel-title"><h3>{{ t('backup.repositories') }} <span class="count">{{ formatNumber(repositories.length) }}</span></h3><span class="eyebrow">{{ t('backup.repositoryEyebrow') }}</span></div><div v-for="repo in repositories" :key="ident(repo)" class="repository-row"><div class="repo-icon"><AppIcon name="volumes"/></div><div><strong>{{ name(repo) }}</strong><code>{{ str(repo.location) }}</code></div><div class="actions"><n-button size="small" @click="exportRecovery(repo)">{{ t('backup.exportRecovery') }}</n-button><n-button size="small" @click="viewSnapshots(repo)">{{ t('backup.browseSnapshots') }}</n-button></div></div><Notice :empty="!busy && !repositories.length"/></section>
  <section v-if="schedules.length" class="panel table-panel section-gap"><div class="panel-title"><h3>{{ t('backup.scheduleHistory') }}</h3><span class="eyebrow">{{ t('backup.scheduleEyebrow') }}</span></div><div class="table-scroll"><table><thead><tr><th>{{ t('backup.policy') }}</th><th>{{ t('backup.scheduledUTC') }}</th><th>{{ t('backup.taskReason') }}</th></tr></thead><tbody><tr v-for="(instance, i) in schedules" :key="i"><td>{{ str(instance.policy_id) }}</td><td>{{ formatDateTime(instance.scheduled_at_utc, { timeZone: 'UTC', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }) }}</td><td>{{ instance.task_id || instance.error || t('backup.awaitingTask') }}</td></tr></tbody></table></div></section>

  <n-drawer :show="policyOpen" :width="'min(700px,100vw)'" @update:show="policyOpen = $event"><n-drawer-content :title="policyDraft.id ? t('backup.editBackupPolicy') : t('backup.createBackupPolicy')" closable>
    <form @submit.prevent="savePolicy"><div class="form-field"><label for="policy-name">{{ t('backup.policyName') }}</label><n-input v-model:value="policyDraft.name" :input-props="{ id: 'policy-name', required: true }"/></div><div class="form-field"><label for="policy-repository">{{ t('backup.repositories') }}</label><n-select filterable v-model:value="policyDraft.repository_id" :input-props="{ id: 'policy-repository' }" :options="repositoryOptions"/></div>
      <h3>{{ t('backup.targets') }}</h3><div v-for="(item, index) in policyDraft.targets" :key="index" class="section-gap"><div class="form-field"><label :for="`target-type-${index}`">{{ t('backup.targetType', { index: formatNumber(index + 1) }) }}</label><n-select filterable :value="item.type" :input-props="{ id: `target-type-${index}` }" :options="targetOptions" @update:value="changeTargetType(item, String($event))"/></div><div class="form-field"><label :for="`target-value-${index}`">{{ item.type === 'directory' ? t('backup.authorizedDirectory') : t('backup.targetNameID') }}</label><n-input :value="String(item.type === 'directory' ? item.path ?? '' : item.name ?? '')" :input-props="{ id: `target-value-${index}`, required: true }" @update:value="updateTarget(item, $event)"/></div><n-button size="small" :disabled="policyDraft.targets.length < 2" @click="policyDraft.targets.splice(index, 1)">{{ t('backup.removeTarget') }}</n-button></div><n-button class="section-gap" @click="policyDraft.targets.push({ type: 'directory', path: '' })"><AppIcon name="plus"/>{{ t('backup.addTarget') }}</n-button>
      <div class="form-field section-gap"><label for="policy-consistency">{{ t('backup.consistencyMode') }}</label><n-select filterable v-model:value="policyDraft.consistency_mode" :input-props="{ id: 'policy-consistency' }" :options="[{ label: t('backup.fileModeOption'), value: 'file' }, { label: t('backup.stopBackup'), value: 'stop' }]"/></div><div class="notice warning">{{ t('backup.stopWarning') }}</div>
      <div class="form-field"><label for="policy-schedule">{{ t('backup.cronSchedule') }}</label><n-input v-model:value="policyDraft.schedule" :input-props="{ id: 'policy-schedule' }" :placeholder="t('backup.cronPlaceholder')"/><small>{{ t('backup.missedSchedule') }}</small></div><div class="form-field"><label for="policy-timezone">{{ t('backup.ianaTimezone') }}</label><n-input v-model:value="policyDraft.timezone" :input-props="{ id: 'policy-timezone', required: true }" placeholder="Asia/Shanghai"/></div><div class="form-field"><n-checkbox v-model:checked="policyDraft.enabled">{{ t('backup.enableSchedule') }}</n-checkbox></div>
      <div class="form-field"><label for="policy-keep">{{ t('backup.keepSnapshots') }}</label><n-input-number v-model:value="policyDraft.keep_last" :min="1" :max="10000" :precision="0" :input-props="{ id: 'policy-keep' }"/></div><div class="form-field"><label for="policy-retries">{{ t('backup.retries') }}</label><n-input-number v-model:value="policyDraft.retries" :min="0" :max="10" :precision="0" :input-props="{ id: 'policy-retries' }"/></div><div class="form-field"><label for="policy-retry-seconds">{{ t('backup.retrySeconds') }}</label><n-input-number v-model:value="policyDraft.retry_seconds" :min="1" :max="86400" :precision="0" :input-props="{ id: 'policy-retry-seconds' }"/></div><div class="form-field"><label for="policy-exclusions">{{ t('backup.excludePaths') }}</label><n-input v-model:value="exclusions" type="textarea" :input-props="{ id: 'policy-exclusions' }"/></div>
      <Notice :error="policyError"/><div class="actions"><n-button @click="policyOpen = false">{{ t('backup.cancel') }}</n-button><n-button type="primary" attr-type="submit" :loading="policyBusy">{{ policyDraft.id ? t('backup.saveChanges') : t('backup.createPolicy') }}</n-button></div>
    </form>
  </n-drawer-content></n-drawer>

  <n-drawer :show="snapshotOpen" :width="'min(800px,100vw)'" @update:show="snapshotOpen = $event"><n-drawer-content :title="t('backup.repositorySnapshots')" closable><Notice :error="error"/><p class="muted">{{ t('backup.snapshotDiscovery') }}</p><div v-for="snapshot in snapshots" :key="ident(snapshot)" class="snapshot-row"><div><strong>{{ ident(snapshot).slice(0,16) }}</strong><code>{{ formatDateTime(snapshot.time ?? snapshot.created_at) }}</code><span class="tag" :class="snapshot.verification_status === 'verified' ? 'success' : 'warning'">{{ snapshot.verification_status === 'verified' ? t('backup.verified') : t('backup.unverified') }}</span><small v-if="snapshot.verification_status !== 'verified'" class="muted">{{ t('backup.incompleteWarning') }} <span v-if="snapshot.verification_warning">{{ snapshot.verification_warning }}</span></small></div><n-button @click="restore(snapshot)">{{ snapshot.verification_status === 'verified' ? t('backup.restoreSnapshot') : t('backup.inspectSnapshot') }}</n-button></div><Notice :empty="!snapshots.length"/></n-drawer-content></n-drawer>
  <n-drawer :show="restoreOpen" :width="'min(750px,100vw)'" @update:show="restoreOpen = $event"><n-drawer-content :title="t('backup.restoreWizard')" closable>
    <div class="steps"><span class="active">{{ t('backup.stepSnapshot') }}</span><span class="active">{{ t('backup.stepTarget') }}</span><span :class="{ active: restorePreview }">{{ t('backup.stepConfirm') }}</span></div><p class="muted">{{ t('backup.snapshotLabel', { snapshot: restoreSnapshot }) }}</p><Notice :error="metadataError" :busy="metadataBusy"/><n-button v-if="metadataError" @click="loadMetadata">{{ t('backup.reloadMetadata') }}</n-button>
    <div v-if="unverified" class="notice warning" role="status"><b>{{ t('backup.snapshotUnverified') }}</b><span>{{ t('backup.rescueWarning') }} <span v-if="metadata?.verification_warning">{{ metadata.verification_warning }}</span></span><n-checkbox v-model:checked="acknowledgeUnverified" @update:checked="invalidatePreview">{{ t('backup.acknowledgeUnverified') }}</n-checkbox></div>
    <div class="form-field"><label for="restore-mode">{{ t('backup.restoreMode') }}</label><n-select filterable :value="restoreMode" :input-props="{ id: 'restore-mode' }" :options="[{ label: t('backup.newMode'), value: 'new' }, { label: t('backup.originalMode'), value: 'original', disabled: unverified }]" @update:value="changeRestoreMode(String($event))"/></div>
    <template v-if="restoreMode === 'original'"><div class="notice warning">{{ t('backup.overwriteWarning') }}</div><div class="form-field"><label for="restore-source">{{ t('backup.originalSource') }}</label><n-select filterable :value="source" :input-props="{ id: 'restore-source' }" :options="sourceOptions" :placeholder="t('backup.sourcePlaceholder')" @update:value="chooseSource(String($event))"/></div></template>
    <div class="form-field"><label for="restore-target">{{ restoreMode === 'original' ? t('backup.originalTarget') : t('backup.newTarget') }}</label><n-input v-model:value="target" :readonly="restoreMode === 'original'" :input-props="{ id: 'restore-target' }" placeholder="/restore/isolated-copy" @update:value="invalidatePreview"/><small v-if="restoreMode === 'new'">{{ t('backup.preservePaths') }}</small></div>
    <div class="form-field"><n-checkbox v-model:checked="recreate" :disabled="restoreMode === 'original' || !metadata || unverified" @update:checked="invalidatePreview">{{ t('backup.recreateServices') }}</n-checkbox></div>
    <template v-if="recreate"><p class="muted">{{ t('backup.serviceNamesWarning') }}</p>
      <h3>{{ t('backup.standaloneContainers') }}</h3><p v-if="!recoveryContainers.length" class="muted">{{ t('backup.noContainers') }}</p><div v-for="item in recoveryContainers" :key="str(item.id)" class="form-field section-gap"><n-checkbox :checked="selectedContainers.includes(str(item.id))" @update:checked="selectResource('containers', str(item.id), $event)">{{ str(item.name) }} · {{ str(item.id).slice(0,12) }}</n-checkbox><label :for="`container-name-${str(item.id)}`">{{ t('backup.restoredContainerName') }}</label><n-input v-model:value="containerNames[str(item.id)]" :disabled="!selectedContainers.includes(str(item.id))" :input-props="{ id: `container-name-${str(item.id)}` }" :placeholder="str(item.name)" @update:value="invalidatePreview"/></div>
      <h3>{{ t('backup.composeProjects') }}</h3><p v-if="!recoveryProjects.length" class="muted">{{ t('backup.noProjects') }}</p><div v-for="item in recoveryProjects" :key="str(item.id)" class="form-field section-gap"><n-checkbox :checked="selectedProjects.includes(str(item.id))" @update:checked="selectResource('projects', str(item.id), $event)">{{ str(item.name) }}</n-checkbox><label :for="`project-name-${str(item.id)}`">{{ t('backup.restoredProjectName') }}</label><n-input v-model:value="projectNames[str(item.id)]" :disabled="!selectedProjects.includes(str(item.id))" :input-props="{ id: `project-name-${str(item.id)}` }" :placeholder="`${str(item.name)}-restored-${restoreSnapshot.slice(0,8)}`" @update:value="invalidatePreview"/></div>
    </template>
    <Notice :error="restoreError"/><div class="actions"><n-button :disabled="!canPreview" :loading="previewBusy" @click="previewRestore">{{ t('backup.previewRestore') }}</n-button><n-button type="primary" :disabled="!restorePreview" @click="applyRestore">{{ t('backup.continueConfirm') }}</n-button></div><pre v-if="restorePreview" class="json-block">{{ pretty(restorePreview) }}</pre>
  </n-drawer-content></n-drawer>
  <FormDialog v-model:show="repositoryForm" :title="t('backup.initializeRepository')" :description="t('backup.repositoryDescription')" :fields="repositoryFields" :submit="createRepository" @saved="load"/>
  <ConfirmDialog v-model:show="confirm" :title="confirmTitle" :detail="confirmDetail" :busy="actionBusy" :error="confirmError" @confirm="execute"/>
</template>
