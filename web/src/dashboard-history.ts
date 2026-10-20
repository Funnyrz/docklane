import type { Row } from './api'

export interface ResourceSample { at: number; cpu: number | null; memory: number | null; disk: number | null }
export type ResourceKey = 'cpu' | 'memory' | 'disk'
export const sampleInterval = 15000
export function usage(total: unknown, available: unknown): { used: number; total: number; percent: number } | null {
  if (typeof total !== 'number' || typeof available !== 'number' || !Number.isFinite(total) || !Number.isFinite(available) || total <= 0 || available < 0 || available > total) return null
  return { used: total - available, total, percent: (total - available) / total * 100 }
}
export function cpuValue(host: Row, now: number, receivedAt = now): number | null {
  const at = typeof host.cpu_sampled_at === 'string' ? Date.parse(host.cpu_sampled_at) : NaN
  const value = host.cpu_percent
  if (host.cpu_status !== 'ready' || !Number.isFinite(at) || typeof value !== 'number' || !Number.isFinite(value) || value < 0 || value > 100) return null
  // Older responses have no server observation clock; preserve their existing check.
  if (host.cpu_observed_at === undefined) return at <= now + 1000 && now - at <= sampleInterval * 2 ? value : null
  const observed = typeof host.cpu_observed_at === 'string' ? Date.parse(host.cpu_observed_at) : NaN
  const elapsed = now - receivedAt, age = observed - at
  return Number.isFinite(age) && Number.isFinite(elapsed) && age >= 0 && elapsed >= 0 && age + elapsed <= sampleInterval * 2 ? value : null
}
export function appendSample(history: ResourceSample[], sample: ResourceSample): ResourceSample[] {
  const previous = history.at(-1)
  const next = [...history]
  if (previous && sample.at - previous.at > sampleInterval * 1.5) next.push({ at: previous.at + sampleInterval, cpu: null, memory: null, disk: null })
  next.push(sample)
  return next.slice(-120)
}
export function chartSegments(history: ResourceSample[], key: ResourceKey): { line: string; area: string }[] {
  if (history.length < 2) return []
  const first = history[0]!.at, duration = Math.max(sampleInterval, history.at(-1)!.at - first)
  const groups: { x: number; y: number }[][] = []
  let current: { x: number; y: number }[] = []
  for (const point of history) {
    const value = point[key]
    if (value === null) { if (current.length > 1) groups.push(current); current = []; continue }
    current.push({ x: Number(((point.at - first) / duration * 320).toFixed(2)), y: Number((60 - value / 100 * 56).toFixed(2)) })
  }
  if (current.length > 1) groups.push(current)
  return groups.map(group => {
    const line = group.map((point, index) => `${index ? 'L' : 'M'}${point.x},${point.y}`).join(' ')
    return { line, area: `${line} L${group.at(-1)!.x},64 L${group[0]!.x},64 Z` }
  })
}
