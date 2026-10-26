import { describe, expect, it } from 'vitest'
import type { ForecastMember } from '../api/types'
import { readableMembers } from './forecastReadable'

const member = (set_id: number, over: Partial<ForecastMember> = {}): ForecastMember => ({
  set_id, symbol: '600519', report_date: '2026-10-19', made_on: '2026-10-19',
  made_at: '2026-10-19T01:00:00Z', stance: 'long', status: 'resolved', ...over,
})

describe('readable verification outcomes', () => {
  it('uses the server verdict and includes flat outcomes as misses', () => {
    const result = readableMembers([
      member(1, { upside: 0.2, direction: 'agree' }),
      member(2, { upside: 0.1, direction: 'flat' }),
      member(3, { upside: -0.2, direction: 'disagree' }),
      member(4, { upside: 0.02, direction: 'untested' }),
      member(5, { upside: -0.4 }),
    ])
    expect(result.up).toEqual({ tested: 2, agreed: 1 })
    expect(result.down).toEqual({ tested: 1, agreed: 0 })
  })

  it('compares stock and benchmark on the same paired observations, preserving missing values', () => {
    const result = readableMembers([
      member(1, { ret_bp: 200, bench_ret_bp: -100 }),
      member(2, { ret_bp: -400, bench_ret_bp: -200 }),
      member(3, { ret_bp: 900 }),
      member(4, { ret_bp: Number.NaN, bench_ret_bp: 100 }),
    ])
    expect(result.returns[0]).toMatchObject({ count: 2, stock: -100, bench: -150, excess: 50, median: -100 })
    expect(result.returns[1].stock).toBeUndefined()
  })

  it('deduplicates pages and excludes non-settled, excluded, and nonfinite scatter rows', () => {
    const good = member(1, { upside: 0.2, excess_bp: 300 })
    const result = readableMembers([
      good, good,
      member(2, { status: 'pending', upside: 1, excess_bp: 99 }),
      member(3, { upside: Infinity, excess_bp: 50 }),
      member(4, { upside: 1, excess_bp: 80, excluded_reason: 'corp_action' }),
    ])
    expect(result.members.map((m) => m.set_id)).toEqual([1, 3])
    expect(result.points.map((m) => m.set_id)).toEqual([1])
  })
})
