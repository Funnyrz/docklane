<script setup lang="ts">
import { ref, watch } from 'vue'
import { NModal, NButton, NCheckbox } from 'naive-ui'
import { t, renderText } from '../i18n'
import { message } from '../api'
const props = defineProps<{show:boolean;title:unknown;detail:unknown;busy?:boolean;error?:unknown}>()
const emit = defineEmits<{ 'update:show':[value:boolean]; confirm:[] }>()
const agreed = ref(false)
const cancelButton = ref<{ $el: HTMLButtonElement } | null>(null)
watch(() => props.show, () => {agreed.value = false})
</script>
<template><n-modal :show="show" :auto-focus="false" @after-enter="cancelButton?.$el.focus()" @update:show="emit('update:show',$event)" preset="card" :title="renderText(title)" class="dialog"><p class="confirm-copy">{{ renderText(detail) }}</p><p v-if="error" class="notice error" role="alert">{{ message(error) }}</p><n-checkbox v-model:checked="agreed">{{ t('common.acknowledge') }}</n-checkbox><template #footer><div class="actions"><n-button ref="cancelButton" @click="emit('update:show',false)">{{ t('common.cancel') }}</n-button><n-button type="error" :disabled="!agreed||busy" :loading="busy" @click="emit('confirm')">{{ t('common.confirm') }}</n-button></div></template></n-modal></template>
