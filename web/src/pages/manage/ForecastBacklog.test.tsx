import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import ForecastBacklog from './ForecastBacklog'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => o ? `${k}:${JSON.stringify(o)}` : k }) }))

describe('ForecastBacklog', () => {
  it('does not present unavailable ledger counts as zero', () => {
    render(<ForecastBacklog backlog={undefined} />)
    expect(screen.getByText('forecast.backlog.unavailable')).toBeTruthy()
    expect(screen.queryByText('forecast.backlog.due')).toBeNull()
  })
  it('shows sleeping overdue work with failure stage and next retry', () => {
    render(<ForecastBacklog backlog={{ as_of: '2026-10-29T09:40:00Z', due: 4, overdue: 1, retrying: 4, oldest_minutes: 2970, oldest_late_minutes: 2070, truncated: false, items: [{ set_id: 12, symbol: '688469', checkpoint: 'd5', due: '2026-10-27', minutes: 2970, refusal: 'fetch_failed', next_attempt_at: '2026-10-29T10:12:00Z', failure: { stage: 'adjustment', source: 'tencent', at: '2026-10-29T09:12:00Z', from: '2026-10-20', to: '2026-10-27', detail: 'missing adjusted rows' } }] }} />)
    expect(screen.getByText('forecast.backlog.warning:{"n":1}')).toBeTruthy()
    expect(screen.getByText(/688469 · #12/)).toBeTruthy()
    expect(screen.getByText('missing adjusted rows')).toBeTruthy()
    expect(screen.getByText(/forecast.backlog.stage_adjustment/)).toBeTruthy()
    expect(screen.queryByText('forecast.backlog.no_detail')).toBeNull()
    // The delay is measured on the alert's clock, not from the close.
    expect(screen.getByText('forecast.backlog.minutes:{"n":2070}')).toBeTruthy()
  })
  it('says there is nothing to settle in one line instead of a grid of zeros', () => {
    render(<ForecastBacklog backlog={{ as_of: '2026-10-29T09:40:00Z', due: 0, overdue: 0, retrying: 0, oldest_minutes: 0, oldest_late_minutes: 0, truncated: false, items: [] }} />)
    expect(screen.getByText(/forecast.backlog.none/)).toBeTruthy()
    expect(screen.queryByText('forecast.backlog.due')).toBeNull()
    expect(screen.queryByText('forecast.backlog.oldest')).toBeNull()
  })
})
