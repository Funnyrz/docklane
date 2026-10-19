<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ name: string }>()
const paths: Record<string, string[]> = {
  dashboard: ['M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z'],
  containers: ['m12 3 9 5-9 5-9-5 9-5Z', 'm3 8 9 5 9-5M3 8v9l9 5 9-5V8M12 13v9'],
  compose: ['M4 4h6v6H4zM14 14h6v6h-6zM14 4h6v6h-6zM4 14h6v6H4z', 'M10 7h4M7 10v4M17 10v4M10 17h4'],
  images: ['M4 4h16v16H4z', 'm4 15 5-5 5 5 3-3 3 3M15 8h.01'],
  networks: ['M9 3h6v6H9zM3 15h6v6H3zM15 15h6v6h-6z', 'M12 9v3M6 15v-3h12v3'],
  volumes: ['M4 6c0-2 16-2 16 0s-16 2-16 0Z', 'M4 6v12c0 3 16 3 16 0V6M4 12c0 3 16 3 16 0'],
  backups: ['M4 10a8 8 0 1 1 1 8', 'M4 4v6h6M12 7v5l3 2'],
  tasks: ['M9 6h12M9 12h12M9 18h12M3 6h.01M3 12h.01M3 18h.01'],
  settings: ['m9 3-.6 2.2-2 .9-2.1-.6L2 9.5 3.6 11v2L2 14.5l2.3 4 2.1-.6 2 .9L9 21h6l.6-2.2 2-.9 2.1.6 2.3-4-1.6-1.5v-2L22 9.5l-2.3-4-2.1.6-2-.9L15 3H9Z', 'M15.5 12a3.5 3.5 0 1 1-7 0 3.5 3.5 0 0 1 7 0Z'],
  server: ['M4 3h16v7H4zM4 14h16v7H4zM8 6.5h.01M8 17.5h.01M12 6.5h5M12 17.5h5'],
  inbox: ['M4 4h16l2 10v6H2v-6L4 4Z', 'M2 14h6l2 3h4l2-3h6'],
  'arrow-up-right': ['M6 18 18 6M6 6h12v12'],
  'arrow-right': ['M4 12h16m-6-6 6 6-6 6'],
  refresh: ['M20 7v5h-5M4 17v-5h5', 'M6.1 7a7 7 0 0 1 11.6-1L20 9M4 15l2.3 3A7 7 0 0 0 18 17'],
  shield: ['m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6l8-3Z', 'm8 12 3 3 5-6'],
  menu: ['M4 6h16M4 12h16M4 18h16'],
  'panel-collapse': ['M3 4h18v16H3zM9 4v16', 'm16 9-3 3 3 3'],
  'panel-expand': ['M3 4h18v16H3zM9 4v16', 'm13 9 3 3-3 3'],
  logout: ['M9 4H4v16h5M9 12h12m-5-5 5 5-5 5'],
  'chevron-right': ['m9 5 7 7-7 7'],
  clock: ['M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z', 'M12 7v5l3 2'],
  plus: ['M12 4v16M4 12h16'],
  activity: ['M3 12h4l3-8 4 16 3-8h4'],
}
const iconPaths = computed(() => paths[props.name] ?? paths.inbox)
</script>

<template>
  <svg class="app-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    <path v-for="(path, index) in iconPaths" :key="index" :d="path" />
  </svg>
</template>
