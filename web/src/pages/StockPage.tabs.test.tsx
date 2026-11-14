import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import StockPage from './StockPage'
import { forgetPrefetched } from '../lib/prefetch'

// Switching between a day's report tabs. Measured on a seeded 10-tab stock (4x CPU, 120 ms RTT),
// a switch took ~3 s with no sign anything was happening: the tab did not move until the next
// report had arrived AND been rendered, nothing said the body was loading, and every click spent
// up to three report downloads. These tests hold the page to what a switch should do instead.

vi.mock('../appAccess', async (orig) => ({
  ...(await orig<typeof import('../appAccess')>()),
  useAppAccess: () => ({ ready: true, isOpen: () => true }),
}))
// No forecast access: the forecast requests are not what this suite is about.
vi.mock('../auth', () => ({
  useAuth: () => ({ user: 'alice', admin: false, forecast: { scope: 'none', external: false }, can: () => true }),
}))
// A failed switch is reported through the shared notification stack, as every failed action is.
const notified = vi.hoisted(() => [] as unknown[])
vi.mock('../lib/notify', async (orig) => ({
  ...(await orig<typeof import('../lib/notify')>()),
  notifyError: (title: unknown) => { notified.push(title); return 'n' },
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ fontSize: 15, fontWeight: 400, wide: false }) }))
vi.mock('../components/Markdown', () => ({ default: ({ md }: { md?: string }) => <div data-testid="body">{md}</div> }))
vi.mock('../components/TimelinePanel', () => ({ default: () => <div>timeline</div> }))
vi.mock('../components/ReaderControls', () => ({ default: () => <div>controls</div> }))
vi.mock('../components/EditReportButton', () => ({ default: () => null }))
vi.mock('../components/FavoriteButton', () => ({ FavoriteButton: () => null }))
vi.mock('../components/PriceChart', () => ({ default: () => <div>chart</div> }))
vi.mock('../components/ExportButtons', () => ({ ExportMenu: () => <div>export-menu</div> }))

const SUBTABS = [
  { id: 1, label: 'Decision', rtype: 'Decision', title: 'Decision' },
  { id: 2, label: 'Sentiment', rtype: 'Sentiment', title: 'Sentiment' },
  { id: 3, label: 'Valuation', rtype: 'Valuation', title: 'Valuation' },
]
const TIMELINE = [
  { date: '2026-11-12', n: 3 },
  { date: '2026-11-11', n: 3 },
  { date: '2026-11-10', n: 3 },
]
const stockFor = (id: number, date = '2026-11-11') => ({
  symbol: '001389',
  market: 'sz',
  name: 'Test Co',
  selDate: date,
  selKind: 'Research',
  selId: id,
  kinds: ['Research'],
  subtabs: SUBTABS,
  timeline: TIMELINE,
  rep: { id, name: 'Test Co', title: `T${id}`, displayTitle: `Report ${id}`, date, time: '', kind: 'Research', rtype: 'x', source: 'x', html: '', md: `body ${id}` },
})

// Every /api/stock request is held until the test answers it, so the in-between state can be read.
type Pending = { url: string; resolve: (v: unknown) => void; reject?: (e: unknown) => void }
const stockCalls: Pending[] = []
vi.mock('../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url.startsWith('/api/stock/')) return new Promise((resolve, reject) => stockCalls.push({ url, resolve, reject }))
      if (url.startsWith('/api/quote/')) return new Promise(() => {})
      return Promise.resolve({})
    },
  },
  qs: (o: Record<string, string>) => {
    const p = new URLSearchParams(Object.entries(o).filter(([, v]) => v)).toString()
    return p ? `?${p}` : ''
  },
  ApiError: class extends Error {},
  errText: (e: unknown) => String((e as Error)?.message ?? e),
}))

// The warm-up requests go through fetch, not the api client; they are counted and held the same way.
const fetchCalls: Pending[] = []
// Every warm-up ever started, for the teardown; `fetchCalls` is cleared by tests that count.
const allFetches: Pending[] = []
const answerFetch = (p: Pending, body: unknown) =>
  p.resolve({ ok: true, text: () => Promise.resolve(JSON.stringify(body)) })

beforeEach(() => {
  stockCalls.length = 0
  fetchCalls.length = 0
  notified.length = 0
  forgetPrefetched()
  vi.stubGlobal('fetch', (url: string) => new Promise((resolve) => {
    fetchCalls.push({ url, resolve })
    allFetches.push({ url, resolve })
  }))
})
// A warm-up left unanswered stays in flight inside the prefetch module, and the next test asking
// for the same URL would join it instead of starting its own.
afterEach(async () => {
  for (const c of allFetches.splice(0)) c.resolve({ ok: false, text: () => Promise.resolve('') })
  await act(async () => {})
  vi.unstubAllGlobals()
})

function mount() {
  return render(
    <MemoryRouter initialEntries={['/stock/001389?date=2026-11-11']}>
      <Routes>
        <Route path="/stock/:symbol" element={<StockPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

async function loaded() {
  mount()
  await waitFor(() => expect(stockCalls).toHaveLength(1))
  await act(async () => stockCalls[0].resolve(stockFor(1)))
  expect(await screen.findByTestId('body')).toHaveProperty('textContent', 'body 1')
}

const item = (label: string) => screen.getByText(label).closest('.ant-segmented-item') as HTMLElement
const selected = (label: string) => item(label).classList.contains('ant-segmented-item-selected')
// The report card's loading state: antd's Spin over the card, the pattern a refreshing view uses,
// which marks the region it covers busy for assistive technology too.
const busy = (el: Element | null | undefined) => el?.closest('[aria-busy="true"]') != null
const reportBusy = () => busy(document.querySelector('.rp-doc-card'))

describe('StockPage report tabs', () => {
  it('moves the selection at once and shows the report loading until the next one arrives', async () => {
    await loaded()
    expect(reportBusy()).toBe(false)

    fireEvent.click(screen.getByText('Sentiment'))

    // Before the server has answered: the clicked tab is the selected one, and the report says it is loading.
    await waitFor(() => expect(selected('Sentiment')).toBe(true))
    expect(selected('Decision')).toBe(false)
    expect(reportBusy()).toBe(true)
    // A report is far taller than the screen, and antd centres its ring in the whole block: 8,000 px
    // down for an ordinary report, which is why the old page-wide spinner was never seen. The shared
    // tall-content rule holds the ring in view.
    expect(document.querySelector('.rp-doc-card')?.closest('.ant-spin')?.classList.contains('rp-spin-tall')).toBe(true)
    // Only the report waits. The tabs and the quote describe the stock and stay as they were, usable.
    expect(busy(screen.getByText('Sentiment'))).toBe(false)
    expect(busy(screen.getByText('quote.title'))).toBe(false)

    const call = stockCalls.find((c) => c.url.includes('r=2'))
    expect(call).toBeTruthy()
    await act(async () => call!.resolve(stockFor(2)))

    await waitFor(() => expect(screen.getByTestId('body').textContent).toBe('body 2'))
    expect(reportBusy()).toBe(false)
    expect(selected('Sentiment')).toBe(true)
  })

  it('waits for the warm-up already on the wire instead of downloading the report a second time', async () => {
    await loaded()
    fetchCalls.length = 0
    stockCalls.length = 0

    // Pointing at the tab starts the warm-up; the click lands while it is still on the wire.
    fireEvent.mouseEnter(screen.getByText('Valuation'))
    const warm = fetchCalls.find((c) => c.url.includes('r=3'))
    expect(warm).toBeTruthy()
    fireEvent.click(screen.getByText('Valuation'))

    await act(async () => answerFetch(warm!, stockFor(3)))
    await waitFor(() => expect(screen.getByTestId('body').textContent).toBe('body 3'))
    expect(stockCalls.filter((c) => c.url.includes('r=3'))).toHaveLength(0)
  })

  it('does not download the neighbouring days again on every tab switch', async () => {
    await loaded()
    // The page warms the day before and the day after the one being read.
    await waitFor(() => expect(fetchCalls.map((c) => c.url).sort()).toEqual([
      '/api/stock/001389?date=2026-11-10',
      '/api/stock/001389?date=2026-11-12',
    ]))
    for (const c of fetchCalls) answerFetch(c, stockFor(1, c.url.slice(-10)))
    await act(async () => {})
    fetchCalls.length = 0

    fireEvent.click(screen.getByText('Sentiment'))
    const call = stockCalls.find((c) => c.url.includes('r=2'))!
    await act(async () => call.resolve(stockFor(2)))
    await waitFor(() => expect(screen.getByTestId('body').textContent).toBe('body 2'))

    // Same day, same neighbours, already warm: nothing to fetch.
    expect(fetchCalls.filter((c) => c.url.includes('date=2026-11-1') && !c.url.includes('r='))).toHaveLength(0)
  })

  // The server resolves a link by its report id, whatever day the URL names. A stale link answered
  // with another day is still an answer: the page shows it as loaded, not as waiting for ever.
  it('settles when the server answers a stale link with another day', async () => {
    render(
      <MemoryRouter initialEntries={['/stock/001389?date=2026-11-10&r=2']}>
        <Routes>
          <Route path="/stock/:symbol" element={<StockPage />} />
        </Routes>
      </MemoryRouter>,
    )
    await waitFor(() => expect(stockCalls).toHaveLength(1))
    await act(async () => stockCalls[0].resolve(stockFor(2, '2026-11-11')))
    await waitFor(() => expect(screen.getByTestId('body').textContent).toBe('body 2'))
    expect(reportBusy()).toBe(false)
    expect(selected('Sentiment')).toBe(true)
  })

  // A server error is an answer too. The card must not stay dimmed under a tab that never loaded:
  // the selection goes back to the report on screen, and the reader is told the switch failed.
  it('puts the selection back and says so when the next report fails to load', async () => {
    await loaded()
    fireEvent.click(screen.getByText('Sentiment'))
    await waitFor(() => expect(selected('Sentiment')).toBe(true))
    const call = stockCalls.find((c) => c.url.includes('r=2'))!
    await act(async () => call.reject!(Object.assign(new Error('boom'), { status: 500 })))

    await waitFor(() => expect(reportBusy()).toBe(false))
    expect(selected('Decision')).toBe(true)
    expect(screen.getByTestId('body').textContent).toBe('body 1')
    expect(notified).toHaveLength(1)
  })
})
