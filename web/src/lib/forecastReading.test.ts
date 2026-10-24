import { describe, expect, it } from 'vitest'
import type { QuoteResp } from '../api/types'
import { barIsComplete, latestCompletedBar, lowInWindow } from './forecastReading'
import fixture from '../../../internal/app/testdata/forecast/reading_boundaries.json'

const quote = (session: QuoteResp['snapshot']['session']): QuoteResp => ({
  symbol: '600519', name: 'Test', market: 'sh', kind: 'stock', currency: 'CNY', source: 'tencent', tz: 'Asia/Shanghai', barsSource: 'tencent', barsUnavailable: '', adjusted: false, cached: false,
  snapshot: { last: 103, prevClose: 100, open: 101, high: 104, low: 99, change: 3, changePct: '3.00', volume: 1, amount: 1, asOf: '2026-10-23T14:57:00+08:00', session },
  bars: [
    { d: '2026-10-22', o: 99, h: 101, l: 98, c: 100, v: 1 },
    { d: '2026-10-23', o: 101, h: 104, l: 99, c: 103, v: 1 },
  ],
})

describe('latestCompletedBar', () => {
  it('keeps an open-session bar out of the scenario cursor', () => {
    expect(latestCompletedBar(quote('open'), new Date('2026-10-23T15:31:00+08:00'))?.d).toBe('2026-10-22')
  })

  it('uses today once the vendor marks the session closed', () => {
    const closed = quote('close')
    closed.snapshot.asOf = '2026-10-23T15:00:01+08:00'
    expect(latestCompletedBar(closed, new Date('2026-10-23T15:31:00+08:00'))?.d).toBe('2026-10-23')
  })

  it('shares completeness and low-window boundaries with Go settlement', () => {
    for (const row of fixture.completeness) expect(barIsComplete(row.date, row.as_of, new Date(row.now)), JSON.stringify(row)).toBe(row.complete)
    for (const row of fixture.windows) {
      const bars = row.bars.map((b) => ({ ...b, o: b.l, h: b.l, c: b.l, v: 1 }))
      expect(lowInWindow(bars, row.from, row.to) ?? null).toBe(row.low)
    }
  })
})
