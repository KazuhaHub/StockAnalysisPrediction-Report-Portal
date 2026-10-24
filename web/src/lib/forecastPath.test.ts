import { describe, expect, it } from 'vitest'
import type { PredictionSet, QuoteBar } from '../api/types'
import { dayIndex, forecastPath, forecastWindow, priceTicks } from './forecastPath'

const set: PredictionSet = {
  id: 7,
  report_id: 14088,
  state: 'accepted',
  symbol: '600519',
  rtype: '投资决策建议',
  title: '',
  version: 'V3.17',
  made_at: '2026-09-17T13:00:00Z',
  made_on: '2026-09-17',
  cohort: '2026-09',
  zone: 'Asia/Shanghai',
  bench_code: 'sh000852',
  horizon_std: true,
  horizon_end: '2027-09-17',
  baseline_check: 'match',
  body_changed: false,
  s0_date: '2026-09-18',
  s0_fen: 120000,
  claims: [
    { key: 'expected_terminal', kind: 'terminal_value', price_fen: 130000, prob_bp: 6000 },
    { key: 'scenario.bull', kind: 'scenario', label: 'bull', price_fen: 150000, prob_bp: 2500 },
    { key: 'scenario.bear', kind: 'scenario', label: 'bear', price_fen: 95000, prob_bp: 2500 },
    { key: 'scenario.base', kind: 'scenario', label: 'base', price_fen: 125000, prob_bp: 5000 },
  ],
  marks: [
    { checkpoint: 'd5', status: 'resolved', session_date: '2026-09-25', bar_date: '2026-09-25', close_fen: 123400, direction: 'agree' },
    { checkpoint: 'd20', status: 'resolved', session_date: '2026-10-23', close_fen: 118800, direction: 'disagree' },
    { checkpoint: 'd60', status: 'pending' },
    { checkpoint: 'd120', status: 'refused', session_date: '2026-11-20' },
  ],
}

const bar = (d: string, c: number): QuoteBar => ({ d, o: c, h: c, l: c, c, v: 1 })
const bars = [bar('2026-09-17', 119000), bar('2026-09-18', 120100), bar('2026-09-25', 123300), bar('2026-10-23', 118900), bar('2026-10-26', 121000)]

describe('forecastPath', () => {
  it('draws nothing until the set has a start, a horizon and a price to aim at', () => {
    expect(forecastPath({ ...set, s0_date: undefined }, bars, '2026-10-24')).toBeUndefined()
    expect(forecastPath({ ...set, s0_fen: undefined }, bars, '2026-10-24')).toBeUndefined()
    expect(forecastPath({ ...set, horizon_end: undefined }, bars, '2026-10-24')).toBeUndefined()
    expect(forecastPath({ ...set, horizon_end: '2026-09-18' }, bars, '2026-10-24')).toBeUndefined()
    expect(forecastPath({ ...set, claims: [] }, bars, '2026-10-24')).toBeUndefined()
  })

  it('reads the scenarios bear to bull and the expected terminal value', () => {
    const path = forecastPath(set, bars, '2026-10-24')!
    expect(path.from).toBe('2026-09-18')
    expect(path.to).toBe('2027-09-17')
    expect(path.s0).toBe(120000)
    expect(path.scenarios).toEqual([
      { label: 'bear', fen: 95000 },
      { label: 'base', fen: 125000 },
      { label: 'bull', fen: 150000 },
    ])
    expect(path.terminal).toBe(130000)
  })

  it('keeps a set with only a terminal value, or only some scenarios', () => {
    const terminalOnly = forecastPath({ ...set, claims: [set.claims[0]] }, bars, '2026-10-24')!
    expect(terminalOnly.scenarios).toEqual([])
    expect(terminalOnly.terminal).toBe(130000)
  })

  it('draws the actual closes from the start date to today, and nothing before or after', () => {
    const path = forecastPath(set, bars, '2026-10-24')!
    expect(path.actual.map((b) => b.d)).toEqual(['2026-09-18', '2026-09-25', '2026-10-23'])
  })

  it('marks only settled checkpoints, at their bar date and recorded close', () => {
    const path = forecastPath(set, bars, '2026-10-24')!
    expect(path.checkpoints).toEqual([
      { checkpoint: 'd5', d: '2026-09-25', fen: 123400, direction: 'agree' },
      { checkpoint: 'd20', d: '2026-10-23', fen: 118800, direction: 'disagree' },
    ])
  })

  it('stops the actual line at the horizon when no checkpoint settles later', () => {
    const late = [...bars, bar('2027-09-17', 130000), bar('2027-09-20', 131000), bar('2027-10-08', 132000)]
    const path = forecastPath(set, late, '2027-10-10')!
    expect(path.end).toBe('2027-09-17')
    expect(path.actual[path.actual.length - 1].d).toBe('2027-09-17')
    expect(path.today).toBeUndefined()
  })

  it('extends the window to a checkpoint that settles after the horizon, and no further', () => {
    // h12 settles on the first session on or after the anniversary, which a holiday can push past the
    // horizon date; a non-standard horizon can also end before d60 or d120.
    const settledLate = { ...set, marks: [...set.marks, { checkpoint: 'h12' as const, status: 'resolved' as const, bar_date: '2027-09-24', close_fen: 131500, direction: 'agree' as const }] }
    const late = [...bars, bar('2027-09-24', 131500), bar('2027-10-08', 132000)]
    const path = forecastPath(settledLate, late, '2027-10-10')!
    expect(path.to).toBe('2027-09-17')
    expect(path.end).toBe('2027-09-24')
    expect(path.actual[path.actual.length - 1].d).toBe('2027-09-24')
    expect(path.checkpoints[path.checkpoints.length - 1].d).toBe('2027-09-24')
  })

  it('places today only when it falls inside the window', () => {
    expect(forecastPath(set, bars, '2026-10-24')!.today).toBe('2026-10-24')
    expect(forecastPath(set, bars, '2026-09-18')!.today).toBeUndefined()
    expect(forecastPath(set, bars, '2027-09-18')!.today).toBeUndefined()
  })

  it('fits every price it draws inside the scale, with room at both edges', () => {
    const path = forecastPath(set, bars, '2026-10-26')!
    const drawn = [path.s0, path.terminal!, ...path.scenarios.map((s) => s.fen), ...path.actual.map((b) => b.c), ...path.checkpoints.map((c) => c.fen)]
    expect(path.min).toBeLessThan(Math.min(...drawn))
    expect(path.max).toBeGreaterThan(Math.max(...drawn))
  })

  it('still gives a usable scale when every price is the same', () => {
    const flat = forecastPath({ ...set, claims: [{ key: 't', kind: 'terminal_value', price_fen: 120000 }], marks: [] }, [], '2026-10-24')!
    expect(flat.max).toBeGreaterThan(flat.min)
  })
})

describe('forecastWindow', () => {
  it('spans the recorded start to the later of the horizon and the last settled checkpoint', () => {
    expect(forecastWindow(set)).toEqual({ from: '2026-09-18', end: '2027-09-17' })
    expect(forecastWindow({ ...set, marks: [{ checkpoint: 'h12', status: 'resolved', bar_date: '2027-09-24', close_fen: 1 }] })).toEqual({ from: '2026-09-18', end: '2027-09-24' })
    expect(forecastWindow({ ...set, s0_date: undefined })).toBeUndefined()
    expect(forecastWindow({ ...set, horizon_end: undefined })).toBeUndefined()
  })
})

describe('dayIndex', () => {
  it('counts whole calendar days, independent of the local time zone', () => {
    expect(dayIndex('2026-09-19') - dayIndex('2026-09-18')).toBe(1)
    expect(dayIndex('2027-09-17') - dayIndex('2026-09-18')).toBe(364)
  })
})

describe('priceTicks', () => {
  it('puts round prices inside the scale, a few of them', () => {
    expect(priceTicks(90000, 155000)).toEqual([100000, 120000, 140000])
    expect(priceTicks(1015, 1180)).toEqual([1050, 1100, 1150])
  })

  it('never returns a tick outside the scale or an empty list for a usable scale', () => {
    for (const [lo, hi] of [[1, 2], [99, 101], [12345, 67890], [100000, 100050]]) {
      const ticks = priceTicks(lo, hi)
      expect(ticks.length).toBeGreaterThan(0)
      for (const t of ticks) {
        expect(t).toBeGreaterThanOrEqual(lo)
        expect(t).toBeLessThanOrEqual(hi)
      }
    }
  })
})
