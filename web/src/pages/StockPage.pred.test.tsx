import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Link, MemoryRouter, Route, Routes } from 'react-router'
import StockPage from './StockPage'
import { ApiError } from '../api/client'

const access = vi.hoisted(() => ({ scope: 'all' as 'all' | 'own' | 'none', external: false, legacy: false, admin: true, stage: 'full' as 'off' | 'card' | 'full', instant: false }))

// The reading page's prediction request (P10).
//
// W1: switching report must not leave the previous report's marks on screen while the new request
// is in flight. W3: a failed request is an error, not "this report made no prediction", and the
// report body always renders.

vi.mock('../auth', () => ({
  useAuth: () => ({ user: 'alice', name: 'Alice', admin: access.admin, forecastReader: access.stage, forecast: access.legacy ? undefined : { scope: access.scope, external: access.external }, can: () => true, logout: vi.fn() }),
}))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ fontSize: 15, fontWeight: 400, wide: false }) }))
vi.mock('../lib/datetime', () => ({ isInstant: () => access.instant, formatReportDateTime: (s: string) => s }))
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

const RECEIVED = '2026-07-07T01:00:00Z'

const report = (id: number, date: string, symbol = '001238') => ({
  symbol, market: 'sz', name: 'Test Co', selDate: date, selKind: 'Research', selId: id,
  kinds: ['Research'], subtabs: [{ id, label: 'V3', title: 'V3', version: 'V3' }], timeline: [],
  rep: { id, name: 'Test Co', title: 'T', displayTitle: 'Report', date, source: 'x', html: '', md: '# hi', time: RECEIVED },
  ...(withSummary ? { prediction: { has_prediction: true, display_set_id: id, stance: 'long', weight_bp: 6500, upside_bp: 1234, body_changed: false } } : {}),
})

const setFor = (reportId: number, terminal: number) => ({
  id: reportId, report_id: reportId, state: 'accepted', symbol: '001238', rtype: '投资决策建议',
  title: '', version: 'V3', made_at: '2026-09-01T13:00:00Z', made_on: '2026-09-01', cohort: '2026-09',
  zone: 'Asia/Shanghai', bench_code: 'sh000852', horizon_std: true, baseline_check: 'match',
  body_changed: false,
  claims: [
    { key: 'expected_terminal', kind: 'terminal_value', price_fen: terminal },
    ...(withScenarios ? [
      { key: 'scenario.bear', kind: 'scenario', label: 'bear', price_fen: 2000 },
      { key: 'scenario.bull', kind: 'scenario', label: 'bull', price_fen: 5000 },
    ] : []),
  ],
  marks: [], s0_fen: 100000,
})

let predCalls: string[]
let rejectPred: boolean
let denyForecast: boolean
let aDeferred: { resolve: (v: unknown) => void } | null
let pendingB: boolean
let withSummary: boolean
let withScenarios: boolean
// Whether the server says the ledger covers the report's type: undefined omits the field.
let coveredFlag: boolean | undefined
let emptyReport: boolean
// The symbol whose quote never answers until the test releases it.
let deferQuoteFor: string | null
let releaseQuote: (() => void) | null
// The symbol whose report never answers until the test releases it, and the quote symbols asked for.
let deferStockFor: string | null
let releaseStock: (() => void) | null
let quoteCalls: string[]

vi.mock('../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url.startsWith('/api/quote/')) {
        const quoteSymbol = decodeURIComponent(url.slice('/api/quote/'.length).split('?')[0])
        quoteCalls.push(quoteSymbol)
        const quote = { ...QUOTE, symbol: quoteSymbol, bars: [{ ...QUOTE.bars[0], c: quoteSymbol === '001238' ? 3335 : 4400 }] }
        if (quoteSymbol === deferQuoteFor) return new Promise((resolve) => { releaseQuote = () => resolve(quote) })
        return Promise.resolve(quote)
      }
      if (url.includes('/predictions')) {
        predCalls.push(url)
        if (denyForecast) return Promise.reject(new ApiError(403, 'unavailable', 'forecast_unavailable'))
        if (url.startsWith('/api/predictions?')) return Promise.resolve({ ok: true, sets: [] })
        if (url.includes('/api/report/3/')) {
          return Promise.resolve({ ok: true, sets: [setFor(3, 333300)], display_set_id: 3 })
        }
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
        return Promise.resolve({
          ok: true,
          sets: emptyReport ? [] : [setFor(1, 111100)],
          counted_set_id: 1,
          settlement_paused: true,
          ...(coveredFlag === undefined ? {} : { covered: coveredFlag }),
        })
      }
      const stockSymbol = url.startsWith('/api/stock/') ? decodeURIComponent(url.slice('/api/stock/'.length).split('?')[0]) : '001238'
      if (stockSymbol === deferStockFor) return new Promise((resolve) => { releaseStock = () => resolve(report(3, '2026-07-07', stockSymbol)) })
      if (stockSymbol === '600519') return Promise.resolve(report(3, '2026-07-07', stockSymbol))
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
      <Link to="/stock/600519?date=2026-07-07">other-stock</Link>
      {/* A date no earlier test visited, so the prefetch cache cannot answer this one warm. */}
      <Link to="/stock/600519?date=2026-07-09">other-stock-cold</Link>
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
  access.admin = true
  access.stage = 'full'
  access.instant = false
  aDeferred = null
  pendingB = true
  withSummary = false
  withScenarios = false
  coveredFlag = undefined
  emptyReport = false
  deferQuoteFor = null
  releaseQuote = null
  deferStockFor = null
  releaseStock = null
  quoteCalls = []
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

  it('renders no forecast DOM or requests at stage off for a non-admin reader', async () => {
    access.admin = false
    access.stage = 'off'
    show()
    expect(await screen.findByText('report-body')).toBeTruthy()
    expect(predCalls).toEqual([])
    expect(document.querySelector('#forecast')).toBeNull()
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

  it('keeps the reader overlay preference while the toggle stays available on a collapsed chart', async () => {
    window.localStorage.setItem('rp_read_forecast_overlay', '0')
    show()
    const toggle = await screen.findByRole('checkbox')
    expect((toggle as HTMLInputElement).checked).toBe(false)
    expect(screen.getByText('forecast.chart.overlay')).toBeTruthy()
  })

  it('paints the full-stage summary chip with the report body', async () => {
    withSummary = true
    show()
    expect(await screen.findByText('report-body')).toBeTruthy()
    expect(screen.getByTestId('forecast-summary-chip').textContent).toContain('+12.3%')
  })

  describe('the report body header', () => {
    it.each([
      ['scope none', () => { access.scope = 'none' }],
      ['stage off', () => { access.admin = false; access.stage = 'off' }],
    ])('shows no forecast meta line to a reader at %s', async (_name, setup) => {
      setup()
      show()
      expect(await screen.findByText('report-body')).toBeTruthy()
      expect(screen.queryByText('forecast.reading.meta')).toBeNull()
    })

    it('keeps the received clock line behind a parseable instant', async () => {
      access.scope = 'none'
      access.instant = true
      show()
      expect(await screen.findByText(RECEIVED)).toBeTruthy()
      expect(screen.queryByText('forecast.reading.meta')).toBeNull()
    })

    it('shows no clock line when the received time is not an instant', async () => {
      access.scope = 'none'
      show()
      expect(await screen.findByText('report-body')).toBeTruthy()
      expect(screen.queryByText(RECEIVED)).toBeNull()
    })

    it('shows the meta line only inside the card for a reader who has one', async () => {
      show()
      expect(await screen.findByText(/1,111\.00/)).toBeTruthy()
      const lines = screen.getAllByText('forecast.reading.meta')
      expect(lines).toHaveLength(1)
      expect(lines[0].closest('#forecast')).toBeTruthy()
    })
  })

  describe('coverage', () => {
    it('renders no card for a report type the ledger does not cover', async () => {
      coveredFlag = false
      emptyReport = true
      show()
      expect(await screen.findByText('report-body')).toBeTruthy()
      await waitFor(() => expect(predCalls.some((u) => u.includes('/api/report/1/'))).toBe(true))
      await new Promise((r) => setTimeout(r, 20))
      expect(document.querySelector('#forecast')).toBeNull()
      expect(screen.queryByText('forecast.reading.no_forecast')).toBeNull()
    })

    it('keeps the empty card for a covered report that made no forecast', async () => {
      coveredFlag = true
      emptyReport = true
      show()
      expect(await screen.findByText('forecast.reading.no_forecast')).toBeTruthy()
    })
  })

  describe('the quote beside the card', () => {
    it("does not read the previous symbol's quote while the next symbol's quote loads", async () => {
      withScenarios = true
      deferQuoteFor = '600519'
      show()
      expect(await screen.findByText('forecast.panel.latest_close')).toBeTruthy()
      expect(screen.getByTestId('forecast-scenario-cursor')).toBeTruthy()
      await userEvent.click(screen.getByText('other-stock'))
      // The new stock's card is on screen, and its own quote has not answered.
      expect(await screen.findByText(/3,333\.00/)).toBeTruthy()
      await waitFor(() => expect(releaseQuote).not.toBeNull())
      expect(screen.queryByText('forecast.panel.latest_close')).toBeNull()
      expect(screen.queryByTestId('forecast-scenario-cursor')).toBeNull()
      releaseQuote!()
      expect(await screen.findByText('forecast.panel.latest_close')).toBeTruthy()
      // The cursor now sits where the NEW quote's close puts it, not where the first one did.
      expect(screen.getByTestId('forecast-scenario-cursor').getAttribute('data-position')).toBe(String((4400 - 2000) / (5000 - 2000) * 100))
    })

    it("does not price the report still on screen against the next symbol's close", async () => {
      withScenarios = true
      deferStockFor = '600519'
      show()
      expect(await screen.findByText('forecast.panel.latest_close')).toBeTruthy()
      await userEvent.click(screen.getByText('other-stock-cold'))
      // The next symbol's quote answers first; its report is still on its way, so the card on screen
      // is the previous stock's.
      await waitFor(() => expect(quoteCalls).toContain('600519'))
      await waitFor(() => expect(releaseStock).not.toBeNull())
      await new Promise((r) => setTimeout(r, 20))
      expect(screen.getByText(/1,111\.00/)).toBeTruthy()
      expect(screen.queryByText('forecast.panel.latest_close')).toBeNull()
      expect(screen.queryByTestId('forecast-scenario-cursor')).toBeNull()
      releaseStock!()
      // Once the next stock's own report is on screen, its card is priced against its own close.
      expect(await screen.findByText(/3,333\.00/)).toBeTruthy()
      expect(await screen.findByText('forecast.panel.latest_close')).toBeTruthy()
      expect(screen.getByTestId('forecast-scenario-cursor').getAttribute('data-position')).toBe(String((4400 - 2000) / (5000 - 2000) * 100))
    })
  })
})
