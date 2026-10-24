import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Link, MemoryRouter, Route, Routes } from 'react-router'
import StockPage from './StockPage'
import { ApiError } from '../api/client'

const access = vi.hoisted(() => ({ scope: 'all' as 'all' | 'own' | 'none', external: false, legacy: false }))

// The reading page's prediction request (P10).
//
// W1: switching report must not leave the previous report's marks on screen while the new request
// is in flight. W3: a failed request is an error, not "this report made no prediction", and the
// report body always renders.

vi.mock('../auth', () => ({
  useAuth: () => ({ user: 'alice', name: 'Alice', admin: true, forecast: access.legacy ? undefined : { scope: access.scope, external: access.external }, can: () => true, logout: vi.fn() }),
}))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ fontSize: 15, fontWeight: 400, wide: false }) }))
vi.mock('../lib/datetime', () => ({ isInstant: () => false, formatReportDateTime: (s: string) => s }))
vi.mock('../components/Markdown', () => ({ default: () => <div>report-body</div> }))
vi.mock('../components/FavoriteButton', () => ({ FavoriteButton: () => <span>fav</span> }))
vi.mock('../components/TimelinePanel', () => ({ default: () => <div>timeline</div> }))
vi.mock('../components/ReaderControls', () => ({ default: () => <div>controls</div> }))
vi.mock('../components/CompareModal', () => ({ default: () => null }))
vi.mock('../components/PriceChart', () => ({ default: () => <div>chart</div> }))
vi.mock('../components/ExportButtons', () => ({
  ExportPdfButton: () => null,
  ExportDayButton: () => null,
  ExportMenu: () => null,
}))
vi.mock('antd', async (importOriginal) => {
  const actual = await importOriginal<typeof import('antd')>()
  return {
    ...actual,
    Grid: { ...actual.Grid, useBreakpoint: () => ({ xs: true, sm: true, md: true, lg: true, xl: true }) },
  }
})
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const QUOTE = {
  symbol: '001238', name: 'Test Co', market: 'sz', kind: 'stock', currency: 'CNY',
  tz: 'Asia/Shanghai', source: 'tencent',
  snapshot: { last: 3335, prevClose: 3331, open: 3385, high: 3405, low: 3307, change: 4,
    changePct: '0.12', volume: 1, amount: 1, asOf: '2026-09-04T16:14:58+08:00', session: 'close' },
  bars: [{ d: '2026-09-04', o: 3385, h: 3405, l: 3307, c: 3335, v: 1 }],
  barsSource: 'tencent', barsUnavailable: '', adjusted: false, cached: false,
}

const report = (id: number, date: string) => ({
  symbol: '001238', market: 'sz', name: 'Test Co', selDate: date, selKind: 'Research', selId: id,
  kinds: ['Research'], subtabs: [], timeline: [],
  rep: { id, name: 'Test Co', title: 'T', displayTitle: 'Report', date, source: 'x', html: '', md: '# hi', time: '' },
})

const setFor = (reportId: number, terminal: number) => ({
  id: reportId, report_id: reportId, state: 'accepted', symbol: '001238', rtype: '投资决策建议',
  title: '', version: 'V3', made_at: '2026-09-01T13:00:00Z', made_on: '2026-09-01', cohort: '2026-09',
  zone: 'Asia/Shanghai', bench_code: 'sh000852', horizon_std: true, baseline_check: 'match',
  body_changed: false, claims: [{ key: 'expected_terminal', kind: 'terminal_value', price_fen: terminal }],
  marks: [], s0_fen: 100000,
})

let predCalls: string[]
let rejectPred: boolean
let denyForecast: boolean
let aDeferred: { resolve: (v: unknown) => void } | null
let pendingB: boolean

vi.mock('../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url.startsWith('/api/quote/')) return Promise.resolve(QUOTE)
      if (url.includes('/predictions')) {
        predCalls.push(url)
        if (denyForecast) return Promise.reject(new ApiError(403, 'unavailable', 'forecast_unavailable'))
        if (url.startsWith('/api/predictions?')) return Promise.resolve({ ok: true, sets: [] })
        if (url.includes('/api/report/2/')) {
          if (pendingB) return new Promise(() => {}) // the in-flight case
          return Promise.resolve({ ok: true, sets: [setFor(2, 222200)], counted_set_id: 2, settlement_paused: false })
        }
        if (rejectPred) return Promise.reject(new Error('boom'))
        if (aDeferred) {
          return new Promise((resolve) => {
            aDeferred!.resolve = resolve
          })
        }
        return Promise.resolve({ ok: true, sets: [setFor(1, 111100)], counted_set_id: 1, settlement_paused: true })
      }
      if (url.includes('date=2026-07-08')) return Promise.resolve(report(2, '2026-07-08'))
      return Promise.resolve(report(1, '2026-07-07'))
    },
  },
  qs: (o: Record<string, string>) => {
    const p = new URLSearchParams(o).toString()
    return p ? `?${p}` : ''
  },
  errText: () => 'request failed',
  ApiError: class extends Error {
    constructor(public status: number, message: string, public code?: string) { super(message) }
  },
}))

function page() {
  return (
    <MemoryRouter initialEntries={['/stock/001238?date=2026-07-07']}>
      <Link to="/stock/001238?date=2026-07-08">next-report</Link>
      <Routes>
        <Route path="/stock/:symbol" element={<StockPage />} />
      </Routes>
    </MemoryRouter>
  )
}

function show() {
  const view = render(page())
  return { ...view, refreshAccess: () => view.rerender(page()) }
}

beforeEach(() => {
  predCalls = []
  rejectPred = false
  denyForecast = false
  access.scope = 'all'
  access.external = false
  access.legacy = false
  aDeferred = null
  pendingB = true
  try {
    window.localStorage.clear()
  } catch {
    /* nothing stored */
  }
})

describe('StockPage predictions', () => {
  it('keeps legacy prediction reads when an older server omits the hint, but hides them on explicit none', async () => {
    access.legacy = true
    const view = show()
    expect(await screen.findByText(/1,111\.00/)).toBeTruthy()
    await waitFor(() => expect(predCalls.length).toBe(2))
    access.legacy = false
    access.scope = 'none'
    view.refreshAccess()
    expect(screen.queryByText(/1,111\.00/)).toBeNull()
    expect(screen.queryByText('forecast.chart.overlay')).toBeNull()
    expect(predCalls.length).toBe(2)
  })

  it.each(['external', 'own'] as const)('drops old-policy data and refetches when the hint changes to %s', async (change) => {
    const view = show()
    expect(await screen.findByText(/1,111\.00/)).toBeTruthy()
    aDeferred = { resolve: () => {} }
    const before = predCalls.length
    if (change === 'external') access.external = true
    else access.scope = 'own'
    view.refreshAccess()
    expect(screen.queryByText(/1,111\.00/)).toBeNull()
    await waitFor(() => expect(predCalls.length).toBe(before + 2))
    aDeferred.resolve({ ok: true, sets: [setFor(1, 333300)] })
    expect(await screen.findByText(/3,333\.00/)).toBeTruthy()
  })

  it('removes loaded predictions immediately when the current hint changes to none', async () => {
    const view = show()
    expect(await screen.findByText(/1,111\.00/)).toBeTruthy()
    const before = predCalls.length
    access.scope = 'none'
    view.refreshAccess()
    expect(screen.queryByText(/1,111\.00/)).toBeNull()
    expect(screen.queryByText('forecast.chart.overlay')).toBeNull()
    expect(predCalls.length).toBe(before)
  })

  it('skips both prediction requests when the navigation hint denies forecasts', async () => {
    access.scope = 'none'
    show()
    expect(await screen.findByText('report-body')).toBeTruthy()
    expect(predCalls).toEqual([])
    expect(screen.queryByText('common.retry')).toBeNull()
    expect(screen.queryByText('forecast.chart.overlay')).toBeNull()
  })

  it('renders no forecast error or retry when current server scope was revoked', async () => {
    denyForecast = true
    show()
    expect(await screen.findByText('report-body')).toBeTruthy()
    await waitFor(() => expect(predCalls.length).toBe(2))
    expect(screen.queryByText('request failed')).toBeNull()
    expect(screen.queryByText('common.retry')).toBeNull()
    expect(screen.queryByText('forecast.chart.overlay')).toBeNull()
  })

  it('clears the previous report marks as soon as the report changes', async () => {
    show()
    expect(await screen.findByText(/1,111\.00/)).toBeTruthy()
    expect(screen.getByText('forecast.phase.settlement_paused')).toBeTruthy()
    await userEvent.click(screen.getByText('next-report'))
    // The new report's predictions request is still in flight; A's panel must already be gone. The
    // chart overlay reads the same derived state, so it is cleared by the same expression.
    await waitFor(() => expect(screen.queryByText(/1,111\.00/)).toBeNull())
    expect(screen.queryByText('forecast.phase.settlement_paused')).toBeNull()
    expect(predCalls.some((u) => u.includes('/api/report/2/'))).toBe(true)
  })

  it('ignores a late answer for the report that was left', async () => {
    pendingB = false
    aDeferred = { resolve: () => {} }
    show()
    await waitFor(() => expect(predCalls.some((u) => u.includes('/api/report/1/'))).toBe(true))
    await userEvent.click(screen.getByText('next-report'))
    expect(await screen.findByText(/2,222\.00/)).toBeTruthy()
    // The abandoned request now answers. It must not replace the current report's marks.
    aDeferred.resolve({ ok: true, sets: [setFor(1, 111100)], counted_set_id: 1, settlement_paused: true })
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.getByText(/2,222\.00/)).toBeTruthy()
    expect(screen.queryByText(/1,111\.00/)).toBeNull()
    expect(screen.queryByText('forecast.phase.settlement_paused')).toBeNull()
  })

  it('renders the body and an inline retry when the predictions request fails', async () => {
    rejectPred = true
    show()
    expect(await screen.findByText('report-body')).toBeTruthy()
    expect(await screen.findByText('request failed')).toBeTruthy()
    expect(screen.getByText('common.retry')).toBeTruthy()
    const before = predCalls.length
    await userEvent.click(screen.getByText('common.retry'))
    await waitFor(() => expect(predCalls.length).toBeGreaterThan(before))
    // The body never depended on the predictions request.
    expect(screen.getByText('report-body')).toBeTruthy()
  })
})
