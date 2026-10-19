<script setup lang="ts">
import { ref, watch } from 'vue'
import { NModal, NButton, NInput, NCheckbox } from 'naive-ui'
import { post, type Row } from '../api'
import { t } from '../i18n'
import { configurationFromForm, type CapabilityConfiguration, type CapabilitySettingsState, type PathMapping } from '../settings'
import Notice from './Notice.vue'

const props = defineProps<{ show: boolean; state: CapabilitySettingsState }>()
const emit = defineEmits<{ 'update:show': [value: boolean]; saved: [value: Row & { capability_settings: CapabilitySettingsState }] }>()
const paths = ref(''), mappings = ref<PathMapping[]>([]), daemon = ref(false), password = ref(''), acknowledged = ref(false), busy = ref(false), error = ref<unknown>('')
const cancelButton = ref<{ $el: HTMLButtonElement } | null>(null)

function useValues(value: CapabilityConfiguration) {
  paths.value = value.allowed_paths.join('\n')
  mappings.value = value.path_mappings.map(mapping => ({ ...mapping }))
  daemon.value = value.enable_daemon_write && props.state.daemon_toggle_supported
  acknowledged.value = false
  error.value = ''
}
watch(() => props.show, show => {
  password.value = ''
  if (show) useValues(props.state.saved)
})
function close() { if (!busy.value) emit('update:show', false) }
async function save() {
  if (busy.value || !acknowledged.value || !password.value) return
  busy.value = true
  error.value = ''
  try {
    const result = await post<Row & { capability_settings: CapabilitySettingsState }>('/settings/capabilities', {
      ...configurationFromForm(paths.value, mappings.value, daemon.value), password: password.value, confirm: true,
    })
    password.value = ''
    emit('saved', result)
    emit('update:show', false)
  } catch (e) { password.value = ''; error.value = e }
  finally { busy.value = false }
}
</script>

<template>
  <n-modal :show="show" :auto-focus="false" :mask-closable="!busy" :close-on-esc="!busy" @after-enter="cancelButton?.$el.focus()" @update:show="close" preset="card" :title="t('settingsEditor.title')" class="dialog capability-editor">
    <p class="muted">{{ t('settingsEditor.description') }}</p>
    <p v-if="state.pending_restart" class="notice warning">{{ t('settingsEditor.pending') }}</p>
    <form @submit.prevent="save">
      <fieldset :disabled="busy" class="capability-fields">
        <div class="form-field">
          <label for="allowed-directories">{{ t('settingsEditor.paths') }}</label>
          <n-input :input-props="{ id: 'allowed-directories', 'aria-describedby': 'allowed-directories-hint', spellcheck: false }" v-model:value="paths" type="textarea" :autosize="{ minRows: 3, maxRows: 7 }" placeholder="/srv/app-data" />
          <small id="allowed-directories-hint">{{ t('settingsEditor.pathsHint') }}</small>
        </div>
        <div class="form-field">
          <label>{{ t('settingsEditor.mappings') }}</label>
          <p v-if="!mappings.length" class="muted">{{ t('settingsEditor.emptyMapping') }}</p>
          <div v-for="(mapping, index) in mappings" :key="index" class="mapping-editor-row">
            <div><label :for="`mapping-host-${index}`">{{ t('settingsEditor.host') }} {{ index + 1 }}</label><n-input :input-props="{ id: `mapping-host-${index}`, spellcheck: false }" v-model:value="mapping.host" placeholder="/srv/app-data" /></div>
            <div><label :for="`mapping-local-${index}`">{{ t('settingsEditor.local') }} {{ index + 1 }}</label><n-input :input-props="{ id: `mapping-local-${index}`, spellcheck: false }" v-model:value="mapping.local" placeholder="/host-data" /></div>
            <button type="button" class="file-button mapping-remove" :aria-label="t('settingsEditor.removeMapping', { index: index + 1 })" @click="mappings.splice(index, 1)">×</button>
          </div>
          <n-button size="small" :disabled="mappings.length >= 128" @click="mappings.push({ host: '', local: '' })">{{ t('settingsEditor.addMapping') }}</n-button>
          <small>{{ t('settingsEditor.mappingsHint') }}</small>
        </div>
        <div class="capability-authorization">
          <label class="daemon-checkbox"><input id="daemon-authorization" type="checkbox" v-model="daemon" :disabled="!state.daemon_toggle_supported || busy" aria-describedby="daemon-authorization-hint" /><span>{{ t('settingsEditor.daemon') }}</span></label>
          <p id="daemon-authorization-hint" class="muted">{{ t(state.daemon_toggle_supported ? 'settingsEditor.daemonHint' : 'settingsEditor.unsupported') }}</p>
        </div>
        <p class="footnote">{{ t('settingsEditor.precedence') }}</p>
        <div class="form-field">
          <label for="settings-admin-password">{{ t('settingsEditor.password') }}</label>
          <n-input :input-props="{ id: 'settings-admin-password', autocomplete: 'current-password' }" v-model:value="password" type="password" :placeholder="t('settingsEditor.passwordHint')" />
        </div>
        <n-checkbox v-model:checked="acknowledged">{{ t('settingsEditor.acknowledge') }}</n-checkbox>
      </fieldset>
      <Notice :error="error" />
      <div class="actions capability-editor-actions">
        <n-button :disabled="busy" @click="useValues(state.effective)">{{ t('settingsEditor.reset') }}</n-button>
        <n-button ref="cancelButton" :disabled="busy" @click="close">{{ t('common.cancel') }}</n-button>
        <n-button type="primary" attr-type="submit" :disabled="busy || !acknowledged || !password" :loading="busy">{{ t('settingsEditor.save') }}</n-button>
      </div>
      <p class="footnote">{{ t('settingsEditor.restartNote') }}</p>
    </form>
  </n-modal>
</template>
