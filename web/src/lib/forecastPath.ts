import type { ForecastCheckpoint, PredictionMark, PredictionSet, QuoteBar } from '../api/types'

// The forecast-and-actual chart's data, kept apart from the drawing so the edge cases are testable.
//
// A report states where the price should be at the horizon (three scenarios and an expected terminal
// value), not where it should be on each day in between. The chart therefore draws each scenario as a
// straight line from the starting close to its horizon price: a reference path, labelled as one. Only
// the checkpoints are scored, and they are drawn from the ledger's recorded closes, not from the quote
// feed that draws the actual line.

export interface ForecastPathCheckpoint {
  checkpoint: ForecastCheckpoint
  d: string
  fen: number
  direction?: PredictionMark['direction']
}

export interface ForecastPath {
  from: string
  /** The horizon date: where the scenario lines end. */
  to: string
  /** The window's last date: the horizon, or a checkpoint that settled after it. */
  end: string
  s0: number
  scenarios: Array<{ label: 'bear' | 'base' | 'bull'; fen: number }>
  terminal?: number
  actual: QuoteBar[]
  checkpoints: ForecastPathCheckpoint[]
  /** Set only when today falls strictly inside the window. */
  today?: string
  min: number
  max: number
}

/** Whole calendar days since the epoch for a YYYY-MM-DD date, read as UTC so no zone shifts it. */
export function dayIndex(iso: string): number {
  return Math.round(Date.parse(`${iso}T00:00:00Z`) / 86_400_000)
}

const SCENARIO_ORDER = ['bear', 'base', 'bull'] as const

/**
 * The dates a set's chart spans: its recorded start to the later of its horizon and its last settled
 * checkpoint. h12 settles on the first session on or after the anniversary, which a holiday can push
 * past the horizon date, and a non-standard horizon can end before d60 or d120. Undefined until the
 * set has a recorded start and a horizon after it.
 */
export function forecastWindow(set: PredictionSet): { from: string; end: string } | undefined {
  const from = set.s0_date
  const to = set.horizon_end
  if (!from || !to || dayIndex(to) <= dayIndex(from)) return undefined
  let end = to
  for (const m of set.marks) {
    const d = m.bar_date || m.session_date
    if (m.status === 'resolved' && d && d > end) end = d
  }
  return { from, end }
}

/**
 * The chart's data for one set, or undefined when there is nothing honest to draw: no recorded start
 * (S0 is written by the evaluator), no horizon, or no price to aim at. `bars` must already be
 * completed daily bars; `today` is the civil date the reader is in.
 */
export function forecastPath(set: PredictionSet, bars: QuoteBar[], today: string): ForecastPath | undefined {
  const window = forecastWindow(set)
  const to = set.horizon_end
  const s0 = set.s0_fen
  if (!window || !to || s0 === undefined) return undefined
  const { from, end } = window

  const scenarios = SCENARIO_ORDER.flatMap((label) => {
    const fen = set.claims.find((c) => c.kind === 'scenario' && c.label === label)?.price_fen
    return fen === undefined ? [] : [{ label, fen }]
  })
  const terminal = set.claims.find((c) => c.kind === 'terminal_value')?.price_fen
  if (scenarios.length === 0 && terminal === undefined) return undefined

  // Nothing past the window's end: the chart's frame stops there.
  const actual = bars.filter((b) => b.d >= from && b.d <= today && b.d <= end)
  const checkpoints = set.marks.flatMap((m): ForecastPathCheckpoint[] => {
    const d = m.bar_date || m.session_date
    return m.status === 'resolved' && d && m.close_fen !== undefined
      ? [{ checkpoint: m.checkpoint, d, fen: m.close_fen, direction: m.direction }]
      : []
  })

  const drawn = [s0, ...scenarios.map((s) => s.fen), ...(terminal === undefined ? [] : [terminal]), ...actual.map((b) => b.c), ...checkpoints.map((c) => c.fen)]
  const lo = Math.min(...drawn)
  const hi = Math.max(...drawn)
  // Room at both edges so no line runs along the frame; a flat set still gets a visible band.
  const pad = Math.max((hi - lo) * 0.06, Math.abs(hi) * 0.01, 1)

  return {
    from,
    to,
    end,
    s0,
    scenarios,
    terminal,
    actual,
    checkpoints,
    today: today > from && today < end ? today : undefined,
    min: lo - pad,
    max: hi + pad,
  }
}

/** The civil date in China, where every A-share session and checkpoint date is dated. */
export function chinaToday(now = new Date()): string {
  return new Date(now.getTime() + 8 * 3600_000).toISOString().slice(0, 10)
}

/**
 * Round prices (in 分) to label the chart's price scale: about four of them, on a 1/2/5 step, every
 * one inside [min, max]. Prices are whole 分, so the step never goes below one.
 */
export function priceTicks(min: number, max: number, count = 4): number[] {
  const raw = (max - min) / count
  if (!(raw > 0)) return []
  const power = 10 ** Math.floor(Math.log10(raw))
  const fraction = raw / power
  const step = Math.max(1, (fraction <= 1 ? 1 : fraction <= 2 ? 2 : fraction <= 5 ? 5 : 10) * power)
  const ticks: number[] = []
  for (let v = Math.ceil(min / step) * step; v <= max; v += step) ticks.push(Math.round(v))
  return ticks
}
