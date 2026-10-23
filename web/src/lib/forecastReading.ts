import type { PredictionSet, QuoteBar, QuoteResp } from '../api/types'

// The evaluator and the reading cursor share the newestBarFinal boundary through JSON fixtures.
export function barIsComplete(date: string, asOf: string, now: Date): boolean {
  const today = new Date(now.getTime() + 8 * 3600_000).toISOString().slice(0, 10)
  if (date > today) return false
  const stamp = /^\d{4}-\d{2}-\d{2}T.*(?:Z|[+-]\d{2}:\d{2})$/.test(asOf) ? Date.parse(asOf) : NaN
  if (!Number.isFinite(stamp)) return date < today
  const chinaStamp = new Date(stamp + 8 * 3600_000).toISOString()
  const stampedDate = chinaStamp.slice(0, 10)
  if (date < stampedDate) return true
  if (date > stampedDate) return false
  return chinaStamp.slice(11, 16) >= '15:00' && now.getTime() >= Date.parse(`${date}T15:30:00+08:00`)
}

export function latestCompletedBar(quote?: QuoteResp | null, now = new Date()): QuoteBar | undefined {
  if (!quote) return undefined
  const completed = quote.bars.filter((bar) => barIsComplete(bar.d, quote.snapshot.asOf, now))
  return completed[completed.length - 1]
}

export function lowInWindow(bars: QuoteBar[], from: string, to: string): number | undefined {
  const lows = bars.filter((bar) => bar.d > from && bar.d <= to).map((bar) => bar.l)
  return lows.length ? Math.min(...lows) : undefined
}

export function readingSet(sets: PredictionSet[], displaySetID?: number): PredictionSet | undefined {
  const accepted = sets.filter((set) => set.state === 'accepted')
  const byID = accepted.find((set) => set.id === displaySetID)
  const newest = (items: PredictionSet[]) => [...items].sort((a, b) => b.made_at.localeCompare(a.made_at) || b.id - a.id)[0]
  if (displaySetID !== undefined) return displaySetID > 0 ? byID : newest(sets.filter((set) => set.state === 'test'))
  return newest(accepted.filter((set) => !set.body_changed)) ?? newest(accepted) ?? newest(sets.filter((set) => set.state === 'test'))
}
