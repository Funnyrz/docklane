import { t, renderText, isLocalized, formatNumber, i18n } from './i18n'
export type Row = Record<string, unknown>
export class APIError extends Error { constructor(public status: number, public code: string, message: string) { super(message) } }
let csrfToken = ''
export function setCSRF(token: string) { csrfToken = token }
export function cookieToken(): string { const entry = document.cookie.split('; ').find(v => v.startsWith('csrf_token=')); return entry ? decodeURIComponent(entry.slice(11)) : '' }
export async function api<T = Row>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body && !(options.body instanceof Blob) && !(options.body instanceof FormData)) headers.set('Content-Type', 'application/json')
  if (options.method && !['GET', 'HEAD'].includes(options.method.toUpperCase())) headers.set('X-CSRF-Token', cookieToken() || csrfToken)
  const response = await fetch(`/api/v1${path}`, { ...options, headers, credentials: 'same-origin' })
  const data: unknown = response.status === 204 ? null : await response.json().catch(() => null)
  if (!response.ok) { const error = (data as {error?: {code?: string; message?: string}} | null)?.error; throw new APIError(response.status, error?.code ?? 'REQUEST_FAILED', error?.message ?? `HTTP ${response.status}`) }
  return data as T
}
export const post = <T = Row>(path: string, body: unknown = {}) => api<T>(path, { method: 'POST', body: JSON.stringify(body) })
export function rows(value: unknown): Row[] { if (Array.isArray(value)) return value as Row[]; if (value && typeof value === 'object') { const data = value as Row; for (const key of ['items', 'projects', 'policies', 'repositories', 'snapshots', 'tasks', 'Volumes', 'volumes']) if (Array.isArray(data[key])) return data[key] as Row[] } return [] }
export function str(value: unknown): string { if (value === undefined || value === null) return '—'; if (typeof value === 'object') return JSON.stringify(value); return String(value) }
export function field(row: Row, ...keys: string[]): unknown { for (const key of keys) if (row[key] !== undefined && row[key] !== null) return row[key]; return undefined }
export function ident(row: Row): string { return str(field(row, 'id', 'Id', 'ID', 'name', 'Name')) }
export function name(row: Row): string { const names = field(row, 'name', 'Name', 'Names', 'RepoTags', 'target', 'kind'); return Array.isArray(names) ? names.join(', ').replace(/^\//, '') : str(names) }
export function state(row: Row): string { const value = field(row, 'state', 'State', 'status', 'Status'); return value && typeof value === 'object' ? str((value as Row).Status) : str(value) }
export function bytes(value: unknown): string { if (typeof value !== 'number') return '—'; const unit = value > 1024 ** 3 ? 'GiB' : 'MiB'; return `${formatNumber(value / (unit === 'GiB' ? 1024 ** 3 : 1024 ** 2), { minimumFractionDigits: 1, maximumFractionDigits: 1 })} ${unit}` }
export function message(error: unknown): string {
  if (isLocalized(error)) return renderText(error)
  if (error instanceof APIError) {
    const key = `errors.${error.code}`
    const summary = i18n.global.te(key) ? t(key) : ''
    if (summary) return error.message && error.message !== error.code ? `${summary} ${error.message}` : summary
    if (error.message === `HTTP ${error.status}`) return t('common.requestFailed', { status: error.status })
  }
  return error instanceof Error ? error.message : str(error)
}
export function pretty(value: unknown) { return JSON.stringify(value, null, 2) }
