import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import type { PredictionSet } from '../api/types'
import PredictionPanel from './PredictionPanel'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

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
  counted_in_group: '2026-10',
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

function show(sets: PredictionSet[], extra: Partial<React.ComponentProps<typeof PredictionPanel>> = {}) {
  return render(
    <MemoryRouter>
      <PredictionPanel sets={sets} otherSets={[]} symbol="600519" {...extra} />
    </MemoryRouter>,
  )
}

describe('PredictionPanel', () => {
  it('qualifies only calendar-backed projections as expected', () => {
    show([{ ...accepted, marks: [
      { checkpoint: 'd20', status: 'pending', phase: 'waiting', not_before: '2026-10-26', expected: true },
      { checkpoint: 'd120', status: 'pending', phase: 'waiting', not_before: '2027-03-15' },
    ] }])
    expect(screen.getAllByText('forecast.phase.expected')).toHaveLength(1)
    expect(screen.getAllByText('forecast.phase.on_or_after')).toHaveLength(1)
  })
  it('renders nothing when the report has no accepted set', () => {
    const { container } = show([{ ...accepted, state: 'absent' }])
    expect(container.firstChild).toBeNull()
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
    show([{ ...accepted, state: 'test', marks: [], counted_in_group: '' }])
    expect(screen.getByText('forecast.panel.test_run')).toBeTruthy()
    expect(screen.queryByText('forecast.panel.checkpoints')).toBeNull()
    // A test run is never "counted", even if a stale payload says it was.
    expect(screen.queryByText('forecast.panel.counted')).toBeNull()
  })

  it('shows the accepted set rather than a newer test set for the same report', () => {
    show([accepted, { ...accepted, id: 8, state: 'test', marks: [], counted_in_group: '' }], {
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
        { ...accepted, id: 9, report_id: 14001, made_on: '2026-09-17', counted_in_group: undefined },
        { ...accepted, id: 10, report_id: undefined, made_on: '2026-08-15', counted_in_group: undefined },
      ],
    })
    expect(screen.getByText('forecast.panel.others')).toBeTruthy()
    expect(screen.getAllByRole('link').some((a) => a.getAttribute('href')?.includes('date=2026-09-17'))).toBe(true)
  })
})
