import { describe, expect, it } from 'vitest'
import type { ForecastSettings } from '../api/types'
import { evaluatorStatus, REFRESH_MS, REREAD_MIN_MS, STALLED_MS } from './forecastEvaluator'

// The evaluator's state is derived from the heartbeat's age and the last pass, never from a flag the
// server sets, so a page left open goes stale by itself. The operations card and the record page's
// pill share this one derivation, so they cannot disagree about the same evaluator.

const NOW = Date.parse('2027-06-20T07:00:00Z')
const at = (minutesAgo: number) => new Date(NOW - minutesAgo * 60_000).toISOString()

function settings(over: Partial<ForecastSettings> = {}, pass: Partial<NonNullable<ForecastSettings['heartbeat']>['pass']> = {}, tickMinutesAgo = 1): ForecastSettings {
  return {
    enabled: true, mode: '1', bench: 'sh000852', bench_codes: [], gate_codes: [], population: { set: true, candidates: [] },
    due_sets: 0, pending: 0, refused: 0, unsettled_baseline: 0,
    readiness: [{ key: 'ledger_chain', ok: true }],
    dry_gate: {}, alerts: { email_to: '', has_slack: false },
    heartbeat: {
      mode: '1', last_tick_at: at(tickMinutesAgo), bench: {},
      pass: {
        kind: 'tick', started_at: at(1), finished_at: at(1), sets: 1, fetches: {}, bodies: 1, resolved: 0, halted: 0, refused: {},
        refused_by_class: {}, finalised: 0, healed: 0, baselines: {}, awaiting_session: 0, due_unsettled: 0,
        oldest_due_unsettled_minutes: 0, stale_valve_tripped: false, errors: 0, panics: 0, ...pass,
      },
    },
    ...over,
  }
}

describe('evaluatorStatus', () => {
  it('is off whenever the mode is off, whatever the last heartbeat says', () => {
    expect(evaluatorStatus(settings({ mode: '0' }), NOW)).toBe('off')
  })

  it('is healthy for a recent clean pass and dry in dry mode', () => {
    expect(evaluatorStatus(settings(), NOW)).toBe('healthy')
    expect(evaluatorStatus(settings({ mode: 'dry' }), NOW)).toBe('dry')
  })

  it('is stale without a heartbeat or once it is older than ten minutes — in mode 1 only', () => {
    expect(evaluatorStatus(settings({ heartbeat: null }), NOW)).toBe('stale')
    expect(evaluatorStatus(settings({}, {}, 10), NOW)).toBe('healthy')
    expect(evaluatorStatus(settings({}, {}, 11), NOW)).toBe('stale')
  })

  // Dry mode's ticker does no vendor work, so the only heartbeat it will ever have is the one a
  // manual pass wrote. Reporting that as stalled an hour later would put a red light on the mode
  // working as designed, and would do it on every deployment that had not pressed "Evaluate now"
  // since the mode was set.
  it('does not call dry mode stale for a heartbeat only a manual pass refreshes', () => {
    expect(evaluatorStatus(settings({ mode: 'dry' }, {}, 600), NOW)).toBe('dry')
    expect(evaluatorStatus(settings({ mode: 'dry', heartbeat: null }), NOW)).toBe('dry')
  })

  it('still reports a failed or panicking dry pass, which is what the operator is there to read', () => {
    expect(evaluatorStatus(settings({ mode: 'dry' }, { errors: 1 }), NOW)).toBe('error')
    expect(evaluatorStatus(settings({ mode: 'dry' }, { panics: 1 }), NOW)).toBe('error')
    expect(evaluatorStatus(settings({ mode: 'dry' }, { oldest_due_unsettled_minutes: 400 }), NOW)).toBe('lagging')
  })

  it('is lagging when the oldest due checkpoint has waited over six hours, a gate is awaited or the valve tripped', () => {
    expect(evaluatorStatus(settings({}, { oldest_due_unsettled_minutes: 361 }), NOW)).toBe('lagging')
    expect(evaluatorStatus(settings({}, { oldest_due_unsettled_minutes: 360 }), NOW)).toBe('healthy')
    expect(evaluatorStatus(settings({}, { stale_valve_tripped: true }), NOW)).toBe('lagging')
  })

  it('is an error when the last pass failed or panicked, or the ledger chain is not ready', () => {
    expect(evaluatorStatus(settings({}, { errors: 1 }), NOW)).toBe('error')
    expect(evaluatorStatus(settings({}, { panics: 1 }), NOW)).toBe('error')
    expect(evaluatorStatus(settings({ readiness: [{ key: 'ledger_chain', ok: false }] }), NOW)).toBe('error')
  })
})

// The pill on the record page re-reads the operations settings, which count rows in the ledger and the
// reports table on every call. It has to ask rarely enough to spare that, and often enough that a
// running evaluator, whose heartbeat ages on this page's clock between reads, never reads as stopped.
describe('how often the record page re-reads the evaluator', () => {
  it('is not every minute, and not so rarely that a running evaluator looks stalled between reads', () => {
    expect(REFRESH_MS).toBeGreaterThanOrEqual(5 * 60_000)
    // A running evaluator writes a heartbeat every minute, and a pass may run for two more.
    expect(REFRESH_MS + 3 * 60_000).toBeLessThan(STALLED_MS)
  })

  it('lets a returning tab re-read after a minute, ahead of the next scheduled read, but not on every flick', () => {
    expect(REREAD_MIN_MS).toBeGreaterThanOrEqual(60_000)
    expect(REREAD_MIN_MS).toBeLessThan(REFRESH_MS)
  })
})

describe('ledger-wide backlog authority', () => {
  it('keeps sleeping overdue work visible after an empty pass', () => {
    const s = settings()
    s.backlog = { as_of: new Date(NOW).toISOString(), due: 1, overdue: 1, retrying: 1, oldest_minutes: 2970, oldest_late_minutes: 2070, items: [], truncated: false }
    expect(evaluatorStatus(s, NOW)).toBe('lagging')
  })
  it('does not keep a stale per-pass backlog after ledger recovery', () => {
    const s = settings({}, { oldest_due_unsettled_minutes: 9999 })
    s.backlog = { as_of: new Date(NOW).toISOString(), due: 0, overdue: 0, retrying: 0, oldest_minutes: 0, oldest_late_minutes: 0, items: [], truncated: false }
    expect(evaluatorStatus(s, NOW)).toBe('healthy')
  })
  it('does not call an evening of work that cannot settle before midnight lagging', () => {
    const s = settings()
    // Due at today's close, seven hours ago, but not settleable until the date rolls over.
    s.backlog = { as_of: new Date(NOW).toISOString(), due: 6, overdue: 0, retrying: 0, oldest_minutes: 420, oldest_late_minutes: 0, items: [], truncated: false }
    expect(evaluatorStatus(s, NOW)).toBe('healthy')
  })
})
