import { watch } from 'vue'
import { createI18n } from 'vue-i18n'
import { commonZh, commonEn, type MessageSchema } from './common'
import { resourcesZh, resourcesEn } from './resources'
import { backupZh, backupEn } from './backup'
import { viewsZh, viewsEn } from './views'
import { errorsZh, errorsEn } from './errors'
import { capabilitiesZh, capabilitiesEn } from './capabilities'
import { settingsEditorZh, settingsEditorEn } from './settings-editor'

export type Locale = 'zh-CN' | 'en'
export type TextParams = Record<string, string | number>
export interface LocalizedText { i18nKey: string; params?: TextParams }
export type Text = string | LocalizedText
export const localeStorageKey = 'docker-manager:locale'
export const zhMessages = { ...commonZh, resources: resourcesZh, backup: backupZh, ...viewsZh, errors: errorsZh, capabilities: capabilitiesZh, settingsEditor: settingsEditorZh }
export const enMessages: MessageSchema<typeof zhMessages> = { ...commonEn, resources: resourcesEn, backup: backupEn, ...viewsEn, errors: errorsEn, capabilities: capabilitiesEn, settingsEditor: settingsEditorEn }
export const i18n = createI18n({
  legacy: false, locale: 'zh-CN' as Locale, fallbackLocale: 'zh-CN',
  messages: { 'zh-CN': zhMessages, en: enMessages },
})
export const locale = i18n.global.locale
export function t(key: string, params: TextParams = {}): string { return i18n.global.t(key, params) }
export function localized(key: string, params?: TextParams): LocalizedText { return { i18nKey: key, params } }
export function isLocalized(value: unknown): value is LocalizedText {
  return !!value && typeof value === 'object' && 'i18nKey' in value && typeof value.i18nKey === 'string'
}
export function renderText(value: unknown): string {
  if (isLocalized(value)) return t(value.i18nKey, value.params)
  return value === null || value === undefined ? '' : String(value)
}
export function detectLocale(languages: readonly string[], saved?: string | null): Locale {
  if (saved === 'zh-CN' || saved === 'en') return saved
  for (const language of languages) {
    if (/^zh(?:-|$)/i.test(language)) return 'zh-CN'
    if (/^en(?:-|$)/i.test(language)) return 'en'
  }
  return 'zh-CN'
}
export function setLocale(value: Locale, persist = true) {
  locale.value = value
  if (persist) { try { localStorage.setItem(localeStorageKey, value) } catch { /* The in-memory preference still applies. */ } }
}
export function initializeLocale() {
  let saved: string | null = null
  try { saved = localStorage.getItem(localeStorageKey) } catch { /* Storage may be disabled. */ }
  setLocale(detectLocale(navigator.languages?.length ? navigator.languages : [navigator.language], saved), false)
}
watch(locale, value => {
  document.documentElement.lang = value
  document.title = t('common.documentTitle')
}, { immediate: true, flush: 'sync' })
export function formatNumber(value: number, options?: Intl.NumberFormatOptions): string {
  return new Intl.NumberFormat(locale.value === 'en' ? 'en-US' : 'zh-CN', options).format(value)
}
export function formatDateTime(value: unknown, options?: Intl.DateTimeFormatOptions): string {
  if (value === null || value === undefined || value === '') return '—'
  const date = value instanceof Date ? value : new Date(value as string | number)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat(locale.value === 'en' ? 'en-US' : 'zh-CN', options ?? { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(date)
}
export function displayState(value: unknown): string {
  const raw = value === undefined || value === null ? '—' : String(value)
  const key = raw.toLowerCase().replace(/[_\s-]/g, '')
  return Object.hasOwn(commonZh.states, key) ? t(`states.${key}`) : raw
}
const taskKeys: Record<string, string> = {
  backup: 'backup', 'backup.run': 'backup', restore: 'restore', 'backup.restore': 'restore',
  'backup.verify': 'backupVerify', 'container.start': 'containerStart', 'container.stop': 'containerStop',
  'container.restart': 'containerRestart', 'container.delete': 'containerDelete', 'container.update': 'containerUpdate',
  'container.rebuild': 'containerRebuild', 'container.config': 'containerUpdate', 'image.pull': 'imagePull',
  'image.import': 'imageImport', 'image.delete': 'imageDelete', 'storage.scan': 'storageScan',
  'compose.deploy': 'composeDeploy', 'daemon.configure': 'daemonConfigure', 'mirror.diagnostic_pull': 'mirrorDiagnosticPull',
}
export function displayTaskType(value: unknown): string {
  const raw = value === undefined || value === null ? '—' : String(value)
  return Object.hasOwn(taskKeys, raw) ? t(`taskTypes.${taskKeys[raw]}`) : raw
}
