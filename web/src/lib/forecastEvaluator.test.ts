import { describe, expect, it } from 'vitest'
import type { ForecastSettings } from '../api/types'
import { evaluatorStatus } from './forecastEvaluator'

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
        kind: 'tick', started_at: at(1), finished_at: at(1), sets: 1, fetches: {}, resolved: 0, halted: 0, refused: {},
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

  it('is stale without a heartbeat or once it is older than ten minutes', () => {
    expect(evaluatorStatus(settings({ heartbeat: null }), NOW)).toBe('stale')
    expect(evaluatorStatus(settings({}, {}, 10), NOW)).toBe('healthy')
    expect(evaluatorStatus(settings({}, {}, 11), NOW)).toBe('stale')
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
