<script setup lang="ts">
import { locale, setLocale, t, type Locale } from '../i18n'
function change(event: Event) { setLocale((event.target as HTMLSelectElement).value as Locale) }
</script>
<template>
  <label class="language-select">
    <span class="sr-only">{{ t('common.language') }}</span>
    <select :value="locale" :aria-label="t('common.language')" @change="change">
      <option value="zh-CN" lang="zh-CN">简体中文</option>
      <option value="en" lang="en">English</option>
    </select>
  </label>
</template>
