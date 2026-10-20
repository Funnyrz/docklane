import { describe, expect, it } from 'vitest'
import { appendSample, chartSegments, cpuValue, usage, type ResourceSample } from './dashboard-history'

const point = (at: number, cpu: number | null): ResourceSample => ({ at, cpu, memory: cpu, disk: cpu })
describe('real resource history', () => {
  it('accepts zero real usage but rejects missing, invalid and inconsistent capacity', () => {
    expect(usage(100, 100)).toEqual({ used: 0, total: 100, percent: 0 })
    for (const pair of [[0, 0], [100, 101], [100, -1], [Infinity, 10], [100, NaN], [null, 10], [100, undefined]]) expect(usage(...pair as [unknown, unknown])).toBeNull()
  })
  it('only accepts a recent, ready CPU sample in range', () => {
    const now = Date.parse('2026-10-19T10:00:00Z')
    const host = { cpu_status: 'ready', cpu_sampled_at: new Date(now).toISOString(), cpu_percent: 0 }
    expect(cpuValue(host, now)).toBe(0)
    expect(cpuValue({ ...host, cpu_status: 'warming_up' }, now)).toBeNull()
    expect(cpuValue(host, now + 30001)).toBeNull()
    expect(cpuValue({ ...host, cpu_percent: 101 }, now)).toBeNull()
    expect(cpuValue({ ...host, cpu_sampled_at: 'bad' }, now)).toBeNull()
    expect(cpuValue({ ...host, cpu_sampled_at: new Date(now + 10000).toISOString() }, now)).toBeNull()
  })
  it.each([-60000, 60000])('uses server sample age despite a %i ms clock difference', offset => {
    const received = Date.parse('2026-10-19T10:00:00Z')
    const observed = received + offset
    const host = { cpu_status: 'ready', cpu_percent: 25, cpu_observed_at: new Date(observed).toISOString(), cpu_sampled_at: new Date(observed - 15000).toISOString() }
    expect(cpuValue(host, received, received)).toBe(25)
    expect(cpuValue(host, received + 15000, received)).toBe(25)
    expect(cpuValue(host, received + 15001, received)).toBeNull()
    expect(cpuValue({ ...host, cpu_sampled_at: new Date(observed - 30001).toISOString() }, received, received)).toBeNull()
    expect(cpuValue({ ...host, cpu_sampled_at: new Date(observed + 1).toISOString() }, received, received)).toBeNull()
  })
  it('rejects invalid observation times and elapsed time while retaining old response compatibility', () => {
    const now = Date.parse('2026-10-19T10:00:00Z')
    const host = { cpu_status: 'ready', cpu_percent: 25, cpu_sampled_at: new Date(now).toISOString() }
    expect(cpuValue(host, now, now)).toBe(25)
    expect(cpuValue(host, now + 30001, now)).toBeNull()
    for (const observed of ['bad', null, 123]) expect(cpuValue({ ...host, cpu_observed_at: observed }, now, now)).toBeNull()
    expect(cpuValue({ ...host, cpu_observed_at: new Date(now).toISOString() }, now - 1, now)).toBeNull()
    expect(cpuValue({ ...host, cpu_observed_at: new Date(now).toISOString() }, now, NaN)).toBeNull()
  })
  it('caps a session at 120 points and inserts a gap after unobserved time', () => {
    let history: ResourceSample[] = []
    for (let i = 0; i < 125; i++) history = appendSample(history, point(i * 15000, i % 100))
    expect(history).toHaveLength(120)
    expect(history[0]!.at).toBe(75000)
    history = appendSample(history, point(2000000, 10))
    expect(history.at(-2)!.cpu).toBeNull()
    expect(history).toHaveLength(120)
  })
  it('draws only adjacent real points and uses timestamps instead of point indices', () => {
    expect(chartSegments([point(0, 10)], 'cpu')).toEqual([])
    expect(chartSegments([point(0, 10), point(15000, null), point(30000, 20)], 'cpu')).toEqual([])
    const result = chartSegments([point(0, 10), point(15000, 20), point(30000, null), point(45000, 30), point(75000, 40)], 'cpu')
    expect(result).toHaveLength(2)
    expect(result[0]!.line).toBe('M0,54.4 L64,48.8')
    expect(result[1]!.line).toBe('M192,43.2 L320,37.6')
    expect(result[1]!.area).toContain('L320,64 L192,64 Z')
  })
})
