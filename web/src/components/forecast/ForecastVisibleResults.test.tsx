import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import ForecastVisibleResults from './ForecastVisibleResults'
import type { ForecastMember } from '../../api/types'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
const members: ForecastMember[] = [{ set_id: 1, report_id: 10, symbol: '600519', name: 'Example stock',
  report_date: '2026-10-19', made_at: '2026-10-19T01:00:00Z', made_on: '2026-10-19',
  stance: 'long', status: 'resolved', ret_bp: 200, bench_ret_bp: -100, excess_bp: 300,
  s0_fen: 10000, close_fen: 10200, upside: 0.2, direction: 'agree' }]

describe('visible outcome comparisons', () => {
  it('never renders outcomes for a non-mature cell, even if the payload contains them', () => {
    const { container } = render(<MemoryRouter><ForecastVisibleResults members={members} mature={false} complete /></MemoryRouter>)
    expect(container.textContent).toBe('')
  })
  it('shows the report and its own stock/benchmark returns without fetching prices', () => {
    render(<MemoryRouter><ForecastVisibleResults members={members} mature complete /></MemoryRouter>)
    expect(screen.getByText('Example stock')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'forecast.readable.open_report' }).getAttribute('href')).toContain('r=10')
    expect(screen.getByRole('img', { name: 'forecast.readable.scatter' })).toBeTruthy()
  })
  it('withholds subset aggregates until all visible pages have arrived', () => {
    render(<MemoryRouter><ForecastVisibleResults members={members} mature complete={false} /></MemoryRouter>)
    expect(screen.getByText('forecast.readable.partial')).toBeTruthy()
    expect(screen.queryByText('forecast.readable.group_returns')).toBeNull()
    expect(screen.queryByText('forecast.readable.up_verdict')).toBeNull()
  })
  it('keeps external-all counts private and never constructs a population scatter', () => {
    render(<MemoryRouter><ForecastVisibleResults members={members} mature complete hideCounts /></MemoryRouter>)
    expect(screen.getByText('Example stock')).toBeTruthy()
    expect(screen.queryByRole('img', { name: 'forecast.readable.scatter' })).toBeNull()
    expect(screen.queryByText('forecast.readable.group_returns')).toBeNull()
    expect(screen.queryByText('forecast.readable.up_verdict')).toBeNull()
  })
})
