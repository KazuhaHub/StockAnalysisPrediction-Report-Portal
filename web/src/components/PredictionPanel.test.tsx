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
  it('renders nothing when the report has no accepted set', () => {
    const { container } = show([{ ...accepted, state: 'absent' }])
    expect(container.firstChild).toBeNull()
  })

  it('shows the claims that were written, with the entry labelled derived', () => {
    show([accepted])
    expect(screen.getByText('forecast.panel.terminal')).toBeTruthy()
    expect(screen.getByText(/1,497\.75/)).toBeTruthy()
    expect(screen.getByText(/forecast\.panel\.entry_derived/)).toBeTruthy()
    expect(screen.getByText('forecast.panel.counted')).toBeTruthy()
  })

  it('labels a test run and shows no checkpoint table', () => {
    show([{ ...accepted, state: 'test', marks: [] }])
    expect(screen.getByText('forecast.panel.test_run')).toBeTruthy()
    expect(screen.queryByText('forecast.panel.checkpoints')).toBeNull()
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
