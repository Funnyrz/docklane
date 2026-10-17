<script setup lang="ts">
import AppIcon from './AppIcon.vue'
import { t } from '../i18n'
import { message } from '../api'
defineProps<{ error?:unknown; busy?:boolean; empty?:boolean }>()</script>
<template><div v-if="error" class="notice error" role="alert"><b>{{ t('common.requestIncomplete') }}</b><span>{{ message(error) }}</span></div><div v-else-if="busy" class="notice" role="status">{{ t('common.reading') }}</div><div v-else-if="empty" class="empty"><span class="empty-symbol"><AppIcon name="inbox"/></span><strong>{{ t('common.noRecords') }}</strong><p>{{ t('common.actualResources') }}</p></div></template>
