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

// How often the record page's pill re-reads the operations settings. The read is not cheap (it counts
// rows in the ledger and the reports table), so it is made rarely: the heartbeat's age is computed on
// this page's clock, and what the read has to guarantee is only that a running evaluator, which writes
// a heartbeat every minute and may spend two more on a pass, never looks stopped between two reads.
/** Between two reads while the tab is open and visible. Under half the stall threshold, with room for a pass. */
export const REFRESH_MS = 5 * 60_000
/** A tab that becomes visible again re-reads at once, unless it has read this recently. */
export const REREAD_MIN_MS = 60_000

export type EvaluatorStatus = 'off' | 'dry' | 'healthy' | 'stale' | 'lagging' | 'error'

export function evaluatorStatus(s: ForecastSettings, nowMs: number): EvaluatorStatus {
  if (s.mode === '0') return 'off'
  if (s.readiness.some((item) => item.key === 'ledger_chain' && !item.ok)) return 'error'
  const hb = s.heartbeat
  // Dry has no background loop: its ticker deliberately does no vendor work, so only a manual pass
  // writes a heartbeat. "Stale" would therefore be the ordinary state of the mode minutes after the
  // operator's own pass, which is a red light saying the design works. Dry is judged on what the
  // last manual pass said, and on the ledger signals below, which do not expire.
  if (!hb) return s.mode === 'dry' ? 'dry' : 'stale'
  if (s.mode !== 'dry' && nowMs - Date.parse(hb.last_tick_at) > STALLED_MS) return 'stale'
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
