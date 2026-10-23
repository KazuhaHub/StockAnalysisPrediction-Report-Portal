import { render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import ForecastDiagnostics from './ForecastDiagnostics'
import type { ForecastDiagnostics as Diagnostics } from '../../api/types'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string, options?: unknown) => options ? `${key} ${JSON.stringify(options)}` : key }) }))

const ready: Diagnostics = {
  status: 'ready', max_reads: 2, max_http_attempts: 4,
  shadow: {
    updated_at: '2026-10-22T16:00:00Z',
    days: [{ date: '2026-10-22', selected: 2, reads: 1, http_attempts: 4, matches: 0, mismatches: 1, unavailable: 1 }],
    recent: [{ at: '2026-10-22T16:00:00Z', target: { market: 'sh', code: '601899' }, primary: 'tencent', candidate: 'sina', anchors: ['2026-10-19', '2026-10-20'], status: 'mismatch', difference_count: 40, truncated: true, comparison: { match: false, compared_dates: 2, differences: [{ date: '2026-10-19', field: 'close', anchor: true, primary: 1234, candidate: 1235 }] } }],
  },
  calls: { updated_at: '2026-10-22T16:00:00Z', days: [{ date: '2026-10-22', sources: { sina: { calls: 1, failures: 0, refusals: { shadow_budget: 2 }, shadow_http_attempts: 4 } } }] },
}

describe('ForecastDiagnostics', () => {
  it('does not turn missing or corrupt telemetry into healthy zero tables', () => {
    const view = render(<ForecastDiagnostics />)
    expect(screen.getByText('forecast.diagnostics.unavailable')).toBeTruthy()
    expect(screen.queryByRole('table')).toBeNull()
    view.rerender(<ForecastDiagnostics value={{ status: 'empty', max_reads: 2, max_http_attempts: 4 }} />)
    expect(screen.getByText('forecast.diagnostics.empty')).toBeTruthy()
    expect(screen.queryByRole('table')).toBeNull()
    view.rerender(<ForecastDiagnostics value={{ ...ready, status: 'unavailable' }} />)
    expect(screen.queryByRole('table')).toBeNull()
    view.rerender(<ForecastDiagnostics value={{ ...ready, calls: undefined }} />)
    expect(screen.getByText('forecast.diagnostics.unavailable')).toBeTruthy()
    expect(screen.queryByRole('table')).toBeNull()
  })

  it('shows daily counters, exact-fen differences, anchors and truncation without claiming admission', () => {
    render(<ForecastDiagnostics value={ready} />)
    expect(screen.getByText(/forecast.diagnostics.hint/)).toBeTruthy()
    expect(screen.getByText('shadow_budget: 2')).toBeTruthy()
    expect(screen.getByText(/forecast.diagnostics.truncated/).textContent).toContain('40')
    const differences = screen.getByRole('table', { name: 'forecast.diagnostics.differences' })
    expect(within(differences).getByText('forecast.diagnostics.barDate')).toBeTruthy()
    expect(within(differences).getByText('1234')).toBeTruthy()
    expect(within(differences).getByText('1235')).toBeTruthy()
    expect(within(differences).getAllByText('forecast.diagnostics.anchor')).toHaveLength(2)
    expect(screen.getByText(/2026-10-19.*2026-10-20/)).toBeTruthy()
    expect(screen.getByText('forecast.diagnostics.mismatch')).toBeTruthy()
  })

  it('does not confuse a recorded zero value with missing data', () => {
    const value = structuredClone(ready)
    value.shadow!.recent[0].comparison!.differences[0].primary = 0
    render(<ForecastDiagnostics value={value} />)
    const differences = screen.getByRole('table', { name: 'forecast.diagnostics.differences' })
    expect(within(differences).getByText('0')).toBeTruthy()
  })
})
