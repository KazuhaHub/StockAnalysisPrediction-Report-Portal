import { describe, expect, it } from 'vitest'
import { FILE_OVERHEAD_SECONDS, assignShards, shardWeight } from './balancedShards'

const names = (n: number) => Array.from({ length: n }, (_, i) => `src/f${String(i).padStart(2, '0')}.test.ts`)

describe('assignShards', () => {
  it('puts every file in exactly one shard', () => {
    const files = names(37)
    const shards = assignShards(files, {}, 3)
    expect(shards).toHaveLength(3)
    const flat = shards.flat()
    expect(flat).toHaveLength(files.length)
    expect(new Set(flat)).toEqual(new Set(files))
  })

  it('is deterministic and independent of input order', () => {
    const files = names(20)
    const durations = Object.fromEntries(files.map((f, i) => [f, (i * 7) % 11]))
    const a = assignShards(files, durations, 3)
    const b = assignShards([...files].reverse(), durations, 3)
    expect(b).toEqual(a)
  })

  it('balances a skewed input far better than equal file counts would', () => {
    const files = names(12)
    const durations: Record<string, number> = {}
    files.forEach((f, i) => { durations[f] = i < 3 ? 25 : 2 })
    const shards = assignShards(files, durations, 3)
    const totals = shards.map(s => s.reduce((sum, f) => sum + shardWeight(f, durations, 2), 0))
    // Each heavy file lands alone at the head of its own shard; the light ones fill in evenly.
    expect(Math.max(...totals) - Math.min(...totals)).toBeLessThanOrEqual(shardWeight(files[3], durations, 2))
    for (const s of shards) expect(s.filter(f => durations[f] === 25)).toHaveLength(1)
  })

  it('gives files with no recorded duration the median of the known ones', () => {
    const durations = { 'a.test.ts': 1, 'b.test.ts': 5, 'c.test.ts': 9 }
    expect(shardWeight('new.test.ts', durations, 0)).toBe(5)
    expect(shardWeight('a.test.ts', durations, 0.8)).toBeCloseTo(1.8)
  })

  it('falls back to the overhead alone when nothing is recorded', () => {
    expect(shardWeight('x.test.ts', {}, FILE_OVERHEAD_SECONDS)).toBe(FILE_OVERHEAD_SECONDS)
  })

  it('breaks ties toward the lowest shard index and ignores durations for stale paths', () => {
    const files = ['b.test.ts', 'a.test.ts']
    const shards = assignShards(files, { 'gone.test.ts': 99 }, 2)
    expect(shards).toEqual([['a.test.ts'], ['b.test.ts']])
  })

  it('works when there are more shards than files', () => {
    const shards = assignShards(['a.test.ts', 'b.test.ts'], {}, 5)
    expect(shards).toHaveLength(5)
    expect(shards.flat().sort()).toEqual(['a.test.ts', 'b.test.ts'])
    expect(shards.filter(s => s.length === 0)).toHaveLength(3)
  })
})
