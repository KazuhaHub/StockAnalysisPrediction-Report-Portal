import type { ForecastSettings } from '../api/types'

// The evaluator's state, derived from what it last did.
//
// Staleness is computed from the heartbeat's age, not from a server flag: a page left open must go
// stale on its own, and a heartbeat that stopped is exactly the failure an operator has to see. The
// operations card and the record page's pill both read it from here, so they cannot disagree.

/** A heartbeat older than this means the loop has stopped. */
export const STALLED_MS = 10 * 60_000
/** The oldest due, unsettled checkpoint may wait this long before the evaluator counts as lagging. */
export const LAGGING_MINUTES = 360

export type EvaluatorStatus = 'off' | 'dry' | 'healthy' | 'stale' | 'lagging' | 'error'

export function evaluatorStatus(s: ForecastSettings, nowMs: number): EvaluatorStatus {
  if (s.mode === '0') return 'off'
  if (s.readiness.some((item) => item.key === 'ledger_chain' && !item.ok)) return 'error'
  const hb = s.heartbeat
  if (!hb) return 'stale'
  if (nowMs - Date.parse(hb.last_tick_at) > STALLED_MS) return 'stale'
  if (hb.pass.errors > 0 || hb.pass.panics > 0) return 'error'
  if (
    (hb.awaiting_gate?.length ?? 0) > 0 ||
    hb.pass.stale_valve_tripped ||
    hb.pass.oldest_due_unsettled_minutes > LAGGING_MINUTES
  )
    return 'lagging'
  return s.mode === 'dry' ? 'dry' : 'healthy'
}

/** antd Tag colour presets for each state, for the operations card. */
export const EVALUATOR_TAG_COLOR: Record<EvaluatorStatus, string> = {
  off: 'default',
  dry: 'blue',
  healthy: 'green',
  stale: 'orange',
  lagging: 'orange',
  error: 'red',
}
