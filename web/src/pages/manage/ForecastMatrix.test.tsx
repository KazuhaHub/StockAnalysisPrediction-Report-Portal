import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import type { ForecastCell, ForecastGroupsResponse } from '../../api/types'
import ForecastMatrix from './ForecastMatrix'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const mature: ForecastCell = {
  state: 'mature',
  n: 42,
  spread_bp: 310,
  spread_p: 0.012,
  spread_q: 0.041,
}

function resp(cells: Record<string, ForecastCell>): ForecastGroupsResponse {
  return {
    ok: true,
    mode: 'month',
    official: true,
    range: { from: '2026-09-01', to: '2026-09-30' },
    checkpoints: ['d5', 'd20', 'd60', 'd120', 'h12'],
    unrecorded: 0,
    groups: [
      {
        label: '2026-09',
        start: '2026-09-01',
        end: '2026-09-30',
        official: true,
        coverage: {
          reports: 50,
          member: 42,
          duplicate: 5,
          absent: 1,
          rejected: 2,
          backtest: 0,
          degraded: 0,
          mismatch: 0,
          refused: 0,
          pending_baseline: 0,
          pre_live: 0,
          test: 0,
        },
        cells,
      },
    ],
  }
}

const empty = { d5: { state: 'open' }, d20: { state: 'open' }, d60: { state: 'open' }, d120: { state: 'open' }, h12: { state: 'open' } } as Record<string, ForecastCell>

function show(r: ForecastGroupsResponse, mode: 'month' | 'custom' = 'month') {
  return render(
    <MemoryRouter>
      <ForecastMatrix resp={r} metric="spread" mode={mode} onOpen={() => undefined} />
    </MemoryRouter>,
  )
}

describe('ForecastMatrix', () => {
  it('renders a mature cell with its value, q and n', () => {
    show(resp({ ...empty, d5: mature }))
    expect(screen.getByText('3.1 pt')).toBeTruthy()
    expect(screen.getByText(/q=0.041/)).toBeTruthy()
    expect(document.body.textContent).toContain('forecast.cov.summary:')
  })

  it.each([
    ['pending', { state: 'pending', resolved: 31, total: 42, spread_bp: 999 } as ForecastCell],
    ['small', { state: 'small', n: 12, spread_bp: 999 } as ForecastCell],
    ['open', { state: 'open', spread_bp: 999 } as ForecastCell],
  ])('shows no number on a %s cell even when the payload carries one', (_state, cell) => {
    show(resp({ ...empty, d5: cell }))
    expect(screen.queryByText('10.0 pt')).toBeNull()
    expect(screen.queryByText('9.99 pt')).toBeNull()
    expect(screen.queryByText(/pt$/)).toBeNull()
  })

  it('shows the progress of a pending checkpoint', () => {
    show(resp({ ...empty, d5: { state: 'pending', resolved: 31, total: 42 } }))
    expect(screen.getByText('forecast.cell.pending:{"done":31,"total":42}')).toBeTruthy()
  })

  it('marks an exploratory group and keeps the p* convention', () => {
    show(
      {
        ...resp({ ...empty, d5: mature }),
        mode: 'custom',
        official: false,
        range: { from: '2026-09-01', to: '2026-09-30', group: 'month' },
        groups: [{ ...resp({ ...empty, d5: mature }).groups[0], official: false }],
      },
      'custom',
    )
    expect(screen.getByText('forecast.tag.exploratory')).toBeTruthy()
    expect(screen.getByText(/p\*=0.012/)).toBeTruthy()
  })

  it('highlights a rejected count and links it to the audit log', () => {
    show(resp({ ...empty, d5: mature }))
    const link = screen.getByRole('link', { name: /forecast.cov.rejected/ })
    expect(link.getAttribute('href')).toBe('/manage/audit?action=predictions.reject')
  })
})
