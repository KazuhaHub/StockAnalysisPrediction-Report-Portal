import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import type { PredictionSet } from '../api/types'
import ForecastReadingCard from './ForecastReadingCard'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string, values?: { date?: string }) => values?.date && k.startsWith('forecast.phase.') ? `${k}: ${values.date}` : k }) }))

const accepted: PredictionSet = {
  id: 7,
  report_id: 14088,
  state: 'accepted',
  symbol: '600519',
  rtype: '投资决策建议',
  title: '',
  version: 'V3.17',
  made_at: '2026-10-18T13:00:00Z',
  made_on: '2026-10-18',
  cohort: '2026-10',
  zone: 'Asia/Shanghai',
  bench_code: 'sh000852',
  horizon_std: true,
  baseline_check: 'match',
  body_changed: false,
  claims: [
    { key: 'stance', kind: 'stance', stance: 'long' },
    { key: 'expected_terminal', kind: 'terminal_value', price_fen: 149775, prob_bp: 7500 },
    { key: 'scenario.bear', kind: 'scenario', label: 'bear', price_fen: 100000, prob_bp: 2500 },
    { key: 'scenario.base', kind: 'scenario', label: 'base', price_fen: 154700, prob_bp: 5000 },
    { key: 'scenario.bull', kind: 'scenario', label: 'bull', price_fen: 189700, prob_bp: 2500 },
    { key: 'entry_price', kind: 'entry_price', price_fen: 132900 },
  ],
  s0_fen: 19004,
  s0_date: '2026-09-18',
  marks: [{ checkpoint: 'd5', status: 'resolved', bar_date: '2026-09-28', close_fen: 21128, ret_bp: 1117, bench_ret_bp: -511 }],
}

function show(sets: PredictionSet[], extra: Partial<React.ComponentProps<typeof ForecastReadingCard>> = {}) {
  return render(
    <MemoryRouter>
      <ForecastReadingCard sets={sets} otherSets={[]} symbol="600519" {...extra} />
    </MemoryRouter>,
  )
}

describe('ForecastReadingCard', () => {
  it('shows settlement paused separately from a mark phase', () => {
    show([{ ...accepted, marks: [{ checkpoint: 'd5', status: 'pending', phase: 'settling' }] }], { settlementPaused: true })
    expect(screen.getByText('forecast.phase.settlement_paused')).toBeTruthy()
    expect(screen.getByText('forecast.phase.settling')).toBeTruthy()
    expect(screen.queryByText('forecast.phase.retrying')).toBeNull()
  })
  it('qualifies only calendar-backed projections as expected', () => {
    show([{ ...accepted, marks: [
      { checkpoint: 'd20', status: 'pending', phase: 'waiting', not_before: '2026-10-26', expected: true },
      { checkpoint: 'd120', status: 'pending', phase: 'waiting', not_before: '2027-03-15' },
    ] }])
    expect(screen.getAllByText(/forecast.phase.expected/)).toHaveLength(1)
    expect(screen.getAllByText(/forecast.phase.on_or_after/)).toHaveLength(1)
    expect(screen.getByText('forecast.phase.expected: 2026-10-26')).toBeTruthy()
  })
  it('distinguishes a report without a set from unavailable forecast access', () => {
    show([{ ...accepted, state: 'absent' }])
    expect(screen.getByText('forecast.reading.no_forecast')).toBeTruthy()
  })

  it('shows the claims that were written, with the entry labelled derived', () => {
    show([accepted])
    expect(screen.getByText('forecast.panel.terminal')).toBeTruthy()
    expect(screen.getByText(/1,497\.75/)).toBeTruthy()
    expect(screen.getByText(/forecast\.panel\.entry_derived/)).toBeTruthy()
    // The counted/not-counted badge is gone until real membership exists: every set used to claim
    // "not counted", which was not a fact about that set.
    expect(screen.queryByText('forecast.panel.counted')).toBeNull()
    expect(screen.queryByText('forecast.panel.not_counted')).toBeNull()
  })

  it('translates the decision quality', () => {
    show([{ ...accepted, decision_quality: 'degraded' }])
    expect(screen.getByText('forecast.quality.degraded')).toBeTruthy()
    expect(screen.queryByText(/forecast\.panel\.quality/)).toBeNull()
  })

  it('says a pending mark is waiting, never that fetching failed', () => {
    show([
      {
        ...accepted,
        marks: [
          { checkpoint: 'd5', status: 'pending', phase: 'waiting', not_before: '2026-10-26' },
          { checkpoint: 'd20', status: 'refused', phase: 'waiting', not_before: '2026-11-16' },
        ],
      },
    ])
    expect(screen.getAllByText(/forecast\.phase\.on_or_after/).length).toBe(2)
    expect(screen.queryByText(/forecast\.mark\.refused/)).toBeNull()
  })

  it('labels a test run and shows no checkpoint table', () => {
    show([{ ...accepted, state: 'test', marks: [] }])
    expect(screen.getByText('forecast.panel.test_run')).toBeTruthy()
    expect(screen.queryByText('forecast.panel.checkpoints')).toBeNull()
    // A test run is never "counted", even if a stale payload says it was.
    expect(screen.queryByText('forecast.panel.counted')).toBeNull()
  })

  it('shows the accepted set rather than a newer test set for the same report', () => {
    show([accepted, { ...accepted, id: 8, state: 'test', marks: [] }], {
      countedSetID: accepted.id,
    })
    expect(screen.getByText('forecast.panel.checkpoints')).toBeTruthy()
    expect(screen.queryByText('forecast.panel.test_run')).toBeNull()
  })

  it('warns when the report body changed after the set was written', () => {
    show([{ ...accepted, body_changed: true }])
    expect(screen.getByText('forecast.panel.body_changed')).toBeTruthy()
  })

  it('labels the latest close as not scored', () => {
    show([accepted], { latestClose: 17101, latestDate: '2026-10-16' })
    const line = screen.getByText(/forecast\.panel\.latest_close/)
    expect(line.textContent).toContain('forecast.panel.latest_close')
    // The wording is the promise: a price printed beside the marks must not read as one of them.
    expect(line.textContent).not.toContain('forecast.panel.ret')
  })

  it('lists other sets for the same stock and links only the live ones', () => {
    show([accepted], {
      otherSets: [
        accepted,
        { ...accepted, id: 9, report_id: 14001, made_on: '2026-09-17' },
        { ...accepted, id: 10, report_id: undefined, made_on: '2026-08-15' },
      ],
    })
    expect(screen.getByText('forecast.panel.others')).toBeTruthy()
    expect(screen.getAllByRole('link').some((a) => a.getAttribute('href') === '/stock/600519?date=2026-09-17&kind=&r=14001#forecast')).toBe(true)
  })

  it('shows membership, baseline detail and the disclaimer', () => {
    show([{ ...accepted, membership: 'member', baseline_emitted_fen: 19004, baseline_as_of: '2026-09-18' }])
    expect(screen.getByText('forecast.reading.counted')).toBeTruthy()
    expect(screen.getByText('forecast.reading.reported_close')).toBeTruthy()
    expect(screen.getByText('forecast.disclaimer')).toBeTruthy()
  })

  it('shows the display revision even when another revision counted', () => {
    show([accepted, { ...accepted, id: 8, claims: [{ key: 'expected_terminal', kind: 'terminal_value', price_fen: 222200 }], made_at: '2026-10-19T13:00:00Z' }], { displaySetID: 8, countedSetID: 7 })
    expect(screen.getByText(/2,222\.00/)).toBeTruthy()
    expect(screen.queryByText(/1,497\.75/)).toBeNull()
  })

  it('hides membership, orphaned rows and payload for a protected reader', () => {
    show([{ ...accepted, membership: 'member', payload: 'private-payload' }], {
      allowMembership: false, allowPayload: false, allowOrphans: false,
      otherSets: [{ ...accepted, id: 9, report_id: undefined, report_date: '2025-01-01' }],
    })
    expect(screen.queryByText('forecast.reading.counted')).toBeNull()
    expect(screen.queryByText('private-payload')).toBeNull()
    expect(screen.queryByText('2025-01-01')).toBeNull()
  })

  it('does not expose outcome numbers on a pending mark carrying stale metric fields', () => {
    show([{ ...accepted, marks: [{ checkpoint: 'd5', status: 'pending', phase: 'waiting', ret_bp: 1234, close_fen: 888888 }] }])
    expect(screen.queryByText('+12.34%')).toBeNull()
    expect(screen.queryByText('8,888.88')).toBeNull()
  })
})
