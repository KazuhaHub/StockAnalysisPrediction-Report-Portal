import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import type { ForecastGroupsResponse } from '../../api/types'
import ForecastReadableOverview from './ForecastReadableOverview'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const response = (state: 'open' | 'mature', redacted = false): ForecastGroupsResponse => ({
  ok: true, mode: 'month', official: true, range: { from: '', to: '' }, checkpoints: ['d5'],
  groups: [{ label: '2026-10', start: '2026-10-01', end: '2026-10-31', official: true, bench: 'sh000852',
    cells: { d5: { state, ...(redacted ? { min_n_met: true } : { n: 37 }), hit: 0.75, ic: 0.42, spread_bp: 321 } } as ForecastGroupsResponse['groups'][number]['cells'],
  }],
})

describe('readable record overview', () => {
  it('withholds every metric until the selected cell is mature', () => {
    render(<ForecastReadableOverview response={response('open')} onOpen={vi.fn()} />)
    expect(screen.queryByText('75%')).toBeNull()
    expect(screen.queryByText('+3.2')).toBeNull()
    expect(screen.getByText('forecast.overview.waiting')).toBeTruthy()
  })
  it('shows published results with plain explanations and a drilldown', () => {
    render(<ForecastReadableOverview response={response('mature')} onOpen={vi.fn()} />)
    expect(screen.getByText('75%')).toBeTruthy()
    expect(screen.getByText('0.42')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'forecast.overview.details' })).toBeTruthy()
  })
  it('does not turn a withheld sample count into zero or infer it from metric subsets', () => {
    render(<ForecastReadableOverview response={response('mature', true)} onOpen={vi.fn()} />)
    expect(screen.getByText('forecast.cell.min_n_met')).toBeTruthy()
    expect(screen.queryByText('37')).toBeNull()
    expect(screen.queryByText(/^0$/)).toBeNull()
  })
})
