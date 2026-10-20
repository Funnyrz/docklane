<script setup lang="ts">
import { computed } from 'vue'
import { chartSegments, type ResourceSample, type ResourceKey } from '../dashboard-history'
import { t } from '../i18n'
const props = defineProps<{ history: ResourceSample[]; resource: ResourceKey; label: string; unavailable: string; available: boolean }>()
const segments = computed(() => chartSegments(props.history, props.resource))
</script>

<template>
  <svg v-if="segments.length" class="trend-plot" viewBox="0 0 320 64" preserveAspectRatio="none" role="img" :aria-label="label">
    <g v-for="(segment, index) in segments" :key="index"><path class="trend-area" :d="segment.area" /><path class="trend-line" :d="segment.line" /></g>
  </svg>
  <div v-else class="trend-placeholder">{{ available ? t('dashboardConsole.collecting') : unavailable }}</div>
  <small v-if="available || segments.length" class="trend-caption">{{ available ? t('dashboardConsole.sessionTrend') : unavailable }}</small>
</template>
