import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { Link, MemoryRouter, Route, Routes } from 'react-router'
import userEvent from '@testing-library/user-event'
import RunPage from './RunPage'
import type { PredictionSet } from '../api/types'

const state = vi.hoisted(() => ({
  scope: 'all' as 'all' | 'own' | 'none', stage: 'full' as 'off' | 'card' | 'full', external: false,
  calls: [] as string[], deferred: null as null | ((value: unknown) => void),
}))
vi.mock('../auth', () => ({ useAuth: () => ({ user: 'reader', admin: false, forecastReader: state.stage, forecast: { scope: state.scope, external: state.external } }) }))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ fontSize: 15, fontWeight: 400, wide: false }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('../components/Markdown', () => ({ default: () => <div>report-body</div> }))
vi.mock('../components/EditReportButton', () => ({ default: () => null }))
vi.mock('../components/ReaderControls', () => ({ default: () => null }))
vi.mock('../components/VersionSwitcher', () => ({ default: () => null }))
vi.mock('../components/ExportButtons', () => ({ ExportPdfButton: () => null }))
const accepted = (id: number): PredictionSet => ({
  id, report_id: id, state: 'accepted', symbol: '600519', rtype: 'Decision', title: 'Report', version: 'internal',
  made_at: '2026-10-20T08:00:00Z', made_on: '2026-10-20', cohort: '2026-10', zone: 'Asia/Shanghai',
  bench_code: 'sh000852', horizon_std: true, baseline_check: 'pending', body_changed: false,
  claims: [{ key: 'stance', kind: 'stance', stance: 'long' }, { key: 'terminal', kind: 'terminal_value', price_fen: id * 111100 }], marks: [],
})
vi.mock('../api/client', () => ({
  api: { get: (url: string) => {
    state.calls.push(url)
    if (url.includes('/predictions')) {
      if (state.deferred) return new Promise((resolve) => { state.deferred = resolve })
      const id = url.includes('/2/') ? 2 : 1
      return Promise.resolve({ ok: true, sets: [accepted(id)], display_set_id: id })
    }
    const id = url.includes('r=2') ? 2 : 1
    return Promise.resolve({ key: 'run', symbol: '600519', name: 'Test', date: '2026-10-20', selId: id, tabs: [],
      rep: { id, title: 'Report', displayTitle: `Report ${id}`, date: '2026-10-20', kind: 'Decision', rtype: 'Decision', md: 'body', html: '' },
      prediction: { has_prediction: true, display_set_id: id, stance: 'long', upside_bp: 1234, body_changed: false },
    })
  } },
  qs: (values: Record<string, string>) => `?${new URLSearchParams(values)}`,
  ApiError: class extends Error {}, errText: () => 'request failed',
}))
function page() {
  return <MemoryRouter initialEntries={['/run/run']}><Link to="/run/run?r=2">next-edition</Link><Routes><Route path="/run/:key" element={<RunPage />} /></Routes></MemoryRouter>
}
beforeEach(() => {
  state.scope = 'all'; state.stage = 'full'; state.external = false; state.calls = []; state.deferred = null
  localStorage.clear()
})
describe('RunPage forecast access and revisions', () => {
  it.each(['none', 'off'] as const)('renders the report without forecast DOM at %s', async (value) => {
    if (value === 'none') state.scope = 'none'
    else state.stage = 'off'
    render(page())
    await screen.findByText('report-body')
    expect(document.querySelector('#forecast')).toBeNull()
    expect(screen.queryByTestId('forecast-summary-chip')).toBeNull()
    expect(state.calls.some((url) => url.includes('/predictions'))).toBe(false)
  })
  it('shows a card at card stage and adds the body summary only at full', async () => {
    state.stage = 'card'
    const view = render(page())
    await screen.findByText(/1,111\.00/)
    expect(screen.queryByTestId('forecast-summary-chip')).toBeNull()
    state.stage = 'full'
    view.rerender(page())
    expect(await screen.findByTestId('forecast-summary-chip')).toBeTruthy()
  })
  it('clears the prior edition before its successor predictions arrive', async () => {
    render(page())
    await screen.findByText(/1,111\.00/)
    state.deferred = () => {}
    await userEvent.click(screen.getByText('next-edition'))
    await screen.findByText('Report 2')
    expect(screen.queryByText(/1,111\.00/)).toBeNull()
    await waitFor(() => expect(state.calls.some((url) => url.includes('/report/2/'))).toBe(true))
    state.deferred!({ ok: true, sets: [accepted(2)], display_set_id: 2 })
    expect(await screen.findByText(/2,222\.00/)).toBeTruthy()
  })
})
