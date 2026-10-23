import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
import { MemoryRouter } from 'react-router'
import { ApiError } from '../api/client'
import type { ForecastCell, ForecastCheckpoint, ForecastGroupsResponse, Me } from '../api/types'
import { forecastLinkColor } from '../lib/forecastPresentation'
import ForecastRecordPage from './ForecastRecordPage'

// The reader's record page. Its contract is mostly about what it must NOT do: ask an admin route,
// retry a refusal, draw a count the reader may not know, or leave a state without a way forward.

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
const csvMock = vi.hoisted(() => ({ downloadCSV: vi.fn() }))
const auth = vi.hoisted(() => ({
  value: { user: 'reader', admin: false, forecast: { scope: 'all', external: false } } as {
    user: string
    admin: boolean
    forecast: Me['forecast']
  },
}))

vi.mock('../api/client', async (original) => ({
  ...(await original<typeof import('../api/client')>()),
  api: apiMock,
  qs: () => '',
  errText: (e: unknown) => (e instanceof Error ? e.message : 'failed'),
}))
vi.mock('../auth', () => ({ useAuth: () => auth.value }))
vi.mock('../lib/csv', async (original) => ({ ...(await original<typeof import('../lib/csv')>()), downloadCSV: csvMock.downloadCSV }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k),
  }),
}))

const mature: ForecastCell = { state: 'mature', n: 42, spread_bp: 310, spread_p: 0.012, spread_q: 0.041 }
const open = { state: 'open' } as ForecastCell
const cells = (d5: ForecastCell) => ({ d5, d20: open, d60: open, d120: open, h12: open })
const coverage = {
  reports: 50, member: 42, duplicate: 5, absent: 1, rejected: 2, backtest: 0, degraded: 0, mismatch: 0,
  pending_baseline: 0, pre_live: 0, test: 0, off_benchmark: 0,
}

function record(extra: Partial<ForecastGroupsResponse> = {}, d5: ForecastCell = mature): ForecastGroupsResponse {
  return {
    ok: true,
    mode: 'month',
    official: true,
    range: { from: '2026-09-01', to: '2026-09-30' },
    checkpoints: ['d5', 'd20', 'd60', 'd120', 'h12'],
    groups: [{ label: '2026-09', start: '2026-09-01', end: '2026-09-30', official: true, bench: 'sh000852', coverage, cells: cells(d5) }],
    ...extra,
  }
}

// An external reader with the all scope: no coverage, no unrecorded, no metadata.
function withheld(d5: ForecastCell = { state: 'mature', min_n_met: true, spread_bp: 310, spread_p: 0.012, spread_q: 0.041 }) {
  const r = record({}, d5)
  for (const g of r.groups) delete g.coverage
  return r
}

const members = { ok: true, label: '2026-09', start: '2026-09-01', end: '2026-09-30', checkpoint: 'd5', state: 'mature', members: [] }

const urls = () => apiMock.get.mock.calls.map(([u]) => String(u))
const lastUrl = () => urls()[urls().length - 1]

function show(initial = '/forecast') {
  return render(
    <MemoryRouter initialEntries={[initial]}>
      <ForecastRecordPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  apiMock.get.mockReset()
  csvMock.downloadCSV.mockReset()
  auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: false } }
  apiMock.get.mockImplementation((url: string) =>
    Promise.resolve(url.startsWith('/api/forecast/groups/members') ? members : record()))
})
afterEach(() => {
  // Restore spies before the real clock: a spy taken on the fake setTimeout would otherwise put the
  // fake back after useRealTimers.
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('ForecastRecordPage requests', () => {
  it('asks only the reader routes, for every view and for the drawer, and never an admin route', async () => {
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.month' }))
    fireEvent.click(await screen.findByText('+3.1 pt'))
    await waitFor(() => expect(urls().some((u) => u.startsWith('/api/forecast/groups/members'))).toBe(true))
    expect(urls().length).toBeGreaterThan(1)
    for (const u of urls()) {
      expect(u.startsWith('/api/forecast/groups')).toBe(true)
      expect(u).not.toContain('/api/admin/')
    }
  })

  it('reads the monthly record by default and sends no range with it', async () => {
    show()
    await screen.findByText('+3.1 pt')
    expect(urls()).toEqual(['/api/forecast/groups?mode=month'])
  })

  it('makes no request for a reader whose scope is none, and says so', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'none', external: false } }
    const { container } = show()
    expect(await screen.findByText('forecast.record.unavailable')).toBeTruthy()
    expect(container.querySelector('.ant-result')).not.toBeNull()
    expect(apiMock.get).not.toHaveBeenCalled()
  })

  it('treats a missing navigation hint as no access rather than guessing', async () => {
    auth.value = { user: 'reader', admin: false, forecast: undefined }
    show()
    expect(await screen.findByText('forecast.record.unavailable')).toBeTruthy()
    expect(apiMock.get).not.toHaveBeenCalled()
  })

  it('lets an administrator in on the reader route even without a hint', async () => {
    auth.value = { user: 'root', admin: true, forecast: undefined }
    show()
    await screen.findByText('+3.1 pt')
    // The record itself comes from the reader route. The only other route is the evaluator pill's.
    expect(urls().filter((u) => !u.startsWith('/api/admin/forecast'))).toEqual(['/api/forecast/groups?mode=month'])
    expect(urls().filter((u) => u.startsWith('/api/admin/'))).toEqual(['/api/admin/forecast'])
  })
})

describe('ForecastRecordPage states', () => {
  it('shows a loading skeleton, marked busy, until the answer arrives', async () => {
    let resolve!: (value: ForecastGroupsResponse) => void
    apiMock.get.mockImplementation(() => new Promise<ForecastGroupsResponse>((done) => { resolve = done }))
    const { container } = show()
    expect(container.querySelector('.ant-skeleton')).not.toBeNull()
    expect(container.querySelector('[aria-busy="true"]')).not.toBeNull()
    expect(screen.queryByText('+3.1 pt')).toBeNull()
    await act(async () => resolve(record()))
    expect(await screen.findByText('+3.1 pt')).toBeTruthy()
    expect(container.querySelector('.ant-skeleton')).toBeNull()
  })

  it('renders a 403 as a Result with no retry, and does not ask again', async () => {
    apiMock.get.mockRejectedValue(new ApiError(403, 'no', 'forecast_unavailable'))
    const { container } = show()
    expect(await screen.findByText('forecast.record.unavailable')).toBeTruthy()
    expect(container.querySelector('.ant-result')).not.toBeNull()
    expect(screen.queryByRole('button', { name: 'common.retry' })).toBeNull()
    await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
    expect(apiMock.get).toHaveBeenCalledTimes(1)
  })

  it('retries a busy answer at most three times, with jitter, then offers a retry button', async () => {
    // Only the retry timer is faked: the component library's own animation frames stay real.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const timers = vi.spyOn(globalThis, 'setTimeout')
    apiMock.get.mockRejectedValue(new ApiError(503, 'busy', 'forecast_busy'))
    show()
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(apiMock.get).toHaveBeenCalledTimes(4)
    expect(timers.mock.calls.map((c) => Number(c[1])).filter((d) => d >= 250)).toEqual([375, 750, 1500])
    expect(screen.getByText('busy')).toBeTruthy()
    expect(screen.getByText('forecast.record.busy')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'common.retry' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(apiMock.get).toHaveBeenCalledTimes(8)
  })

  it('does not retry a rate limit and shows the error with a retry button', async () => {
    apiMock.get.mockRejectedValueOnce(new ApiError(429, 'slow down', 'forecast_rate_limited'))
    show()
    expect(await screen.findByText('slow down')).toBeTruthy()
    await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
    expect(apiMock.get).toHaveBeenCalledTimes(1)
    apiMock.get.mockResolvedValueOnce(record())
    fireEvent.click(screen.getByRole('button', { name: 'common.retry' }))
    expect(await screen.findByText('+3.1 pt')).toBeTruthy()
  })

  it('shows any other failure as an alert with a retry button', async () => {
    apiMock.get.mockRejectedValueOnce(new Error('offline'))
    const { container } = show()
    expect(await screen.findByText('offline')).toBeTruthy()
    expect(container.querySelector('.ant-alert-error')).not.toBeNull()
    apiMock.get.mockResolvedValueOnce(record())
    fireEvent.click(screen.getByRole('button', { name: 'common.retry' }))
    expect(await screen.findByText('+3.1 pt')).toBeTruthy()
  })

  // The record a young portal really has: the open month is a group, so the list is not empty, and
  // every cell in it is still waiting. These are the shapes the server produces; an empty list with
  // a due date is not one of them.
  const summary = { since: '2026-10-19', sets: 3, accepted: 3, stocks: 3, marks_by_phase: {}, next_result_not_before: '2026-10-26' }
  const waiting = (extra: Partial<ForecastGroupsResponse> = {}) => record(extra, open)

  it('says when the first result is due while the matrix still shows the open month', async () => {
    apiMock.get.mockResolvedValue(waiting({ summary }))
    show()
    expect(await screen.findByText('forecast.record.empty_dated:{"date":"2026-10-26"}')).toBeTruthy()
    expect(screen.getAllByText('forecast.cell.open_month').length).toBeGreaterThan(0)
    expect(screen.getByRole('table')).toBeTruthy()
  })

  it('words a projected date as expected', async () => {
    apiMock.get.mockResolvedValue(waiting({ summary: { ...summary, expected: true } }))
    show()
    expect(await screen.findByText('forecast.record.empty_expected:{"date":"2026-10-26"}')).toBeTruthy()
  })

  it('says there is no result yet, without a date, when the open month carries no metadata', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: true } }
    const withheldOpen = withheld({ state: 'open' } as ForecastCell)
    apiMock.get.mockResolvedValue(withheldOpen)
    show()
    expect(await screen.findByText('forecast.record.empty')).toBeTruthy()
    expect(document.body.textContent).not.toContain('forecast.record.empty_dated')
    expect(document.body.textContent).not.toContain('forecast.record.empty_expected')
    expect(screen.getAllByText('forecast.cell.open_month').length).toBeGreaterThan(0)
  })

  it.each([
    ['pending', { state: 'pending', total: 50, resolved: 20 } as ForecastCell],
    ['small', { state: 'small', n: 12 } as ForecastCell],
    ['purged', { state: 'purged' } as ForecastCell],
  ])('treats a %s cell as no result yet', async (_name, cell) => {
    apiMock.get.mockResolvedValue(record({ summary }, cell))
    show()
    // Every other checkpoint is open, so no cell in the matrix has a result.
    expect(await screen.findByText('forecast.record.empty_dated:{"date":"2026-10-26"}')).toBeTruthy()
    expect(screen.getByRole('status').textContent).toBe('forecast.record.status_empty')
  })

  it('does not call the record empty once any cell has a result', async () => {
    apiMock.get.mockResolvedValue(record({ summary }))
    show()
    await screen.findByText('+3.1 pt')
    expect(document.body.textContent).not.toContain('forecast.record.empty')
    expect(screen.getByRole('status').textContent).toBe('forecast.record.status_ready')
  })

  it('gives an empty group list the message without a date, and no matrix', async () => {
    apiMock.get.mockResolvedValue(record({ groups: [], summary }))
    show()
    expect(await screen.findByText('forecast.record.empty_dated:{"date":"2026-10-26"}')).toBeTruthy()
    expect(screen.queryByRole('table')).toBeNull()
  })

  it('gives the empty state no date when the server withheld its metadata', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: true } }
    apiMock.get.mockResolvedValue({ ...withheld(), groups: [] })
    show()
    expect(await screen.findByText('forecast.record.empty')).toBeTruthy()
    expect(document.body.textContent).not.toContain('forecast.record.empty_dated')
    expect(document.body.textContent).not.toContain('forecast.record.empty_expected')
  })

  it('does not take a group list that is null for a crash', async () => {
    apiMock.get.mockResolvedValue({ ...record(), groups: null })
    show()
    expect(await screen.findByText('forecast.record.empty')).toBeTruthy()
  })
})

describe('ForecastRecordPage header', () => {
  it('names the population, and carries the disclaimer, for an all reader', async () => {
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.record.population_all')).toBeTruthy()
    expect(screen.getAllByText('forecast.disclaimer').length).toBeGreaterThan(0)
    expect(screen.getByRole('heading', { name: 'forecast.record.title' })).toBeTruthy()
  })

  it('names the reader\'s own unit for an own reader', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: false, ou: 'Research Desk' } }
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.record.population_own:{"ou":"Research Desk"}')).toBeTruthy()
  })

  it('does not invent a unit name when the server gave none', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: false } }
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.record.population_own_unnamed')).toBeTruthy()
    expect(document.body.textContent).not.toContain('population_own:')
  })

  it('says all reports for an administrator whatever the hint says', async () => {
    auth.value = { user: 'root', admin: true, forecast: { scope: 'all', external: false } }
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.record.population_all')).toBeTruthy()
  })
})

// The navigation hint is read once, when the session starts. The numbers are not: each answer was
// computed for the population the server decided on at that request. The header and the export
// therefore name the population the answer states, and the hint only until an answer arrives.
describe('ForecastRecordPage population', () => {
  it('names the population the answer states when the hint has gone stale', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: false, ou: 'Old Desk' } }
    apiMock.get.mockResolvedValue(record({ scope: 'all' }))
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.record.population_all')).toBeTruthy()
    expect(document.body.textContent).not.toContain('population_own')
  })

  it('follows an answer that narrows the population, and names the unit it was rooted at', async () => {
    apiMock.get.mockResolvedValue(record({ scope: 'own', scope_ou: 'Desk' }))
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.record.population_own:{"ou":"Desk"}')).toBeTruthy()
    expect(screen.queryByText('forecast.record.population_all')).toBeNull()
  })

  it('does not borrow a stale unit name when the answer is own but names none', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: false, ou: 'Old Desk' } }
    apiMock.get.mockResolvedValue(record({ scope: 'own' }))
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.record.population_own_unnamed')).toBeTruthy()
    expect(document.body.textContent).not.toContain('Old Desk')
  })

  it('labels the export with the population of the answer, not of the hint', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: false, ou: 'Old Desk' } }
    apiMock.get.mockResolvedValue(record({ scope: 'all' }))
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('button', { name: /forecast.export.button/ }))
    const [, text] = csvMock.downloadCSV.mock.calls[0] as [string, string]
    expect(text).toContain('# population=forecast.record.population_all')
    expect(text).not.toContain('Old Desk')
  })
})

// A reader the server turned away has no population to describe and nothing to filter or export, so
// the page says only what it is and why they cannot see it.
describe('ForecastRecordPage without access', () => {
  const absent = [
    'forecast.record.population_all', 'forecast.record.subtitle', 'forecast.disclaimer',
    'forecast.tab.month', 'forecast.tab.custom', 'forecast.metric.spread', 'forecast.export.button',
    'forecast.methodology.title',
  ]
  const onlyTitleAndRefusal = async () => {
    expect(await screen.findByText('forecast.record.unavailable')).toBeTruthy()
    expect(screen.getByRole('heading', { name: 'forecast.record.title' })).toBeTruthy()
    for (const text of absent) expect(screen.queryByText(text), text).toBeNull()
    expect(screen.queryByRole('tab')).toBeNull()
    expect(screen.queryByRole('button', { name: /forecast.export.button/ })).toBeNull()
    expect(screen.queryByRole('radio')).toBeNull()
  }

  it('shows only the title and the refusal to a reader whose scope is none', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'none', external: false } }
    show()
    await onlyTitleAndRefusal()
  })

  it('shows only the title and the refusal when the server refuses a reader the hint allowed', async () => {
    apiMock.get.mockRejectedValue(new ApiError(403, 'no', 'forecast_unavailable'))
    show()
    await onlyTitleAndRefusal()
  })
})

// The matrix is dozens of cell buttons: it must not sit inside a live region, or every metric switch
// reads the whole table aloud. One small status region says what state the page is in instead.
describe('ForecastRecordPage live region', () => {
  it('does not put the matrix inside a live region', async () => {
    show()
    await screen.findByText('+3.1 pt')
    const table = screen.getByRole('table')
    expect(table.closest('[aria-live]')).toBeNull()
    expect(table.closest('[role="status"]')).toBeNull()
    expect(table.closest('[role="alert"]')).toBeNull()
  })

  it('announces the state, and only the state, in one status region', async () => {
    let resolve!: (value: ForecastGroupsResponse) => void
    apiMock.get.mockImplementation(() => new Promise<ForecastGroupsResponse>((done) => { resolve = done }))
    show()
    expect(screen.getByRole('status').textContent).toBe('common.loading')
    await act(async () => resolve(record()))
    await screen.findByText('+3.1 pt')
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.getByRole('status').textContent).toBe('forecast.record.status_ready')
    // Changing the metric rewrites every cell but is not a new state to announce.
    fireEvent.click(screen.getByText('forecast.metric.ic'))
    expect(screen.getByRole('status').textContent).toBe('forecast.record.status_ready')
  })

  it('announces an empty record as empty', async () => {
    apiMock.get.mockResolvedValue(record({ groups: [] }))
    show()
    await screen.findByText('forecast.record.empty')
    expect(screen.getByRole('status').textContent).toBe('forecast.record.status_empty')
  })

  it('announces a record with no result yet as empty although the matrix shows its open months', async () => {
    apiMock.get.mockResolvedValue(record({}, open))
    const { container } = show()
    await screen.findByText('forecast.record.empty')
    expect(screen.getByRole('table')).toBeTruthy()
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.getByRole('status').textContent).toBe('forecast.record.status_empty')
    // The notice is a note, not a second live region: the status line is the one announcement.
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })

  it('leaves a failure to its own alert instead of announcing it twice', async () => {
    apiMock.get.mockRejectedValueOnce(new Error('offline'))
    const { container } = show()
    await screen.findByText('offline')
    expect(container.querySelector('.ant-alert-error')?.getAttribute('role')).toBe('alert')
    expect(screen.getByRole('status').textContent).toBe('')
  })
})

describe('ForecastRecordPage tabs', () => {
  it('offers the exploratory tab to an internal reader and opens it on a prefilled range', async () => {
    vi.useFakeTimers({ toFake: ['Date'], now: new Date('2027-06-20T07:00:00Z') })
    show('/forecast?mode=custom')
    await screen.findByRole('tab', { name: 'forecast.tab.custom' })
    expect(screen.getByRole('tab', { name: 'forecast.tab.month' })).toBeTruthy()
    expect(screen.getByText('forecast.custom.banner')).toBeTruthy()
    // A blank form is not a view: the tab opens on a range (the last six months when the ledger
    // start is not known yet) and asks for it.
    await waitFor(() => expect(urls()).toEqual(['/api/forecast/groups?mode=custom&from=2026-12-20&to=2027-06-20&group=month']))
  })

  it('says what it is waiting for when only one end of the range is chosen, and asks nothing', async () => {
    show('/forecast?mode=custom&from=2026-09-01')
    expect(await screen.findByText('forecast.record.pick_range')).toBeTruthy()
    expect(apiMock.get).not.toHaveBeenCalled()
  })

  it('requests a custom range with its dates and grouping', async () => {
    apiMock.get.mockResolvedValue(record({ mode: 'custom', official: false, range: { from: '2026-09-01', to: '2026-09-30', group: 'month' } }))
    show('/forecast?mode=custom&from=2026-09-01&to=2026-09-30&group=month')
    await screen.findByText('+3.1 pt')
    expect(urls()).toEqual(['/api/forecast/groups?mode=custom&from=2026-09-01&to=2026-09-30&group=month'])
  })

  it('marks an exploratory result with p* and the exploratory tag, which the record never carries', async () => {
    const base = record()
    apiMock.get.mockResolvedValue({
      ...base,
      mode: 'custom',
      official: false,
      range: { from: '2026-09-01', to: '2026-09-30', group: 'month' },
      groups: [{ ...base.groups[0], official: false }],
    })
    show('/forecast?mode=custom&from=2026-09-01&to=2026-09-30&group=month')
    await screen.findByText('+3.1 pt')
    expect(screen.getByText(/p\*=0\.012/)).toBeTruthy()
    expect(screen.getByText('forecast.tag.exploratory')).toBeTruthy()
  })

  it('shows the record without p* or the exploratory tag', async () => {
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.queryByText(/p\*=/)).toBeNull()
    expect(screen.queryByText('forecast.tag.exploratory')).toBeNull()
  })

  it('does not send an inverted range', async () => {
    show('/forecast?mode=custom&from=2026-09-30&to=2026-09-01&group=month')
    expect(await screen.findByText('forecast.err.range_inverted')).toBeTruthy()
    expect(apiMock.get).not.toHaveBeenCalled()
  })

  it('hides the exploratory tab from an external reader and ignores a custom link', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: true } }
    apiMock.get.mockResolvedValue(withheld())
    show('/forecast?mode=custom&from=2026-09-01&to=2026-09-30&group=month')
    await screen.findByText('+3.1 pt')
    expect(screen.queryByRole('tab', { name: 'forecast.tab.custom' })).toBeNull()
    expect(screen.queryByText('forecast.custom.banner')).toBeNull()
    expect(urls()).toEqual(['/api/forecast/groups?mode=month'])
  })

  it('explains a custom refusal from a stale hint and offers the monthly record', async () => {
    apiMock.get.mockRejectedValueOnce(new ApiError(403, 'no custom', 'forecast_custom_not_allowed'))
    show('/forecast?mode=custom&from=2026-09-01&to=2026-09-30&group=month')
    expect(await screen.findByText('err.forecast_custom_not_allowed')).toBeTruthy()
    apiMock.get.mockResolvedValueOnce(record())
    fireEvent.click(screen.getByRole('button', { name: 'forecast.record.show_monthly' }))
    expect(await screen.findByText('+3.1 pt')).toBeTruthy()
  })
})

describe('ForecastRecordPage volume', () => {
  it('draws an external all response without coverage, counts or a crash', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: true } }
    apiMock.get.mockResolvedValue(withheld())
    show()
    await screen.findByText('+3.1 pt')
    expect(document.body.textContent).toContain('forecast.cell.min_n_met')
    expect(screen.queryByText('forecast.col.coverage')).toBeNull()
    expect(document.body.textContent).not.toMatch(/forecast\.cell\.n:|forecast\.cov\./)
  })

  it('keeps coverage and counts for an external reader of their own unit', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: true, ou: 'Client A' } }
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.col.coverage')).toBeTruthy()
    expect(document.body.textContent).toContain('forecast.cell.n:{"n":42}')
  })

  it('does not link a reader to the administrator audit log', async () => {
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.queryByRole('link', { name: /forecast.cov.rejected/ })).toBeNull()
    expect(document.body.textContent).toContain('forecast.cov.rejected')
  })

  it('lets an administrator follow rejected sets to the audit log', async () => {
    auth.value = { user: 'root', admin: true, forecast: { scope: 'all', external: false } }
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByRole('link', { name: /forecast.cov.rejected/ }).getAttribute('href')).toBe('/manage/audit?action=predictions.reject')
  })
})

describe('ForecastRecordPage export', () => {
  const exported = () => {
    expect(csvMock.downloadCSV).toHaveBeenCalledTimes(1)
    const [name, text] = csvMock.downloadCSV.mock.calls[0] as [string, string]
    return { name, text, header: text.split('\n').find((l) => l.startsWith('group,'))!.split(',') }
  }

  it('exports the reader view with its disclaimer, population and benchmark, and labels every row', async () => {
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('button', { name: /forecast.export.button/ }))
    const { name, text, header } = exported()
    expect(name).toBe('forecast-month-2026-09-01-2026-09-30.csv')
    expect(text).toContain('# disclaimer=forecast.disclaimer')
    expect(text).toContain('# population=forecast.record.population_all')
    expect(text).toContain('# benchmark=2026-09:sh000852')
    expect(text).not.toContain('# unrecorded')
    expect(header).toContain('n')
    expect(header).toContain('coverage_reports')
    for (const row of text.split('\n').filter((l) => l && !l.startsWith('#') && !l.startsWith('group,'))) {
      expect(row.endsWith(',month,true,sh000852')).toBe(true)
    }
  })

  it('exports no n, coverage or count column for an external all reader', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: true } }
    apiMock.get.mockResolvedValue(withheld())
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('button', { name: /forecast.export.button/ }))
    const { text, header } = exported()
    expect(header).not.toContain('n')
    expect(header.join(',')).not.toMatch(/coverage/)
    expect(header).toContain('publication_gate')
    expect(text).toContain('n >= 30')
    // The sentence that explains the flag is the reader's language, not a literal.
    expect(text).toContain('# publication_gate=forecast.csv.header.publication_gate\n')
  })

  it('exports the sample size and coverage for an external reader of their own unit', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: true, ou: 'Client A' } }
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('button', { name: /forecast.export.button/ }))
    const { text, header } = exported()
    expect(header).toContain('n')
    expect(header).toContain('coverage_member')
    expect(text).toContain('# population=forecast.record.population_own:{"ou":"Client A"}')
  })

  it('has nothing to export while the record is empty', async () => {
    apiMock.get.mockResolvedValue(record({ groups: [] }))
    show()
    await screen.findByText('forecast.record.empty')
    expect(screen.getByRole('button', { name: /forecast.export.button/ }).hasAttribute('disabled')).toBe(true)
  })
})

describe('ForecastRecordPage drawer', () => {
  it('opens the drawer on the reader members route for the clicked cell', async () => {
    show()
    fireEvent.click(await screen.findByText('+3.1 pt'))
    const drawer = await screen.findByRole('dialog')
    expect(within(drawer).getByText(/2026-09/)).toBeTruthy()
    await waitFor(() => expect(urls().some((u) => u.startsWith('/api/forecast/groups/members?'))).toBe(true))
    const request = urls().find((u) => u.startsWith('/api/forecast/groups/members?'))!
    const query = new URLSearchParams(request.split('?')[1])
    expect(query.get('label')).toBe('2026-09')
    expect(query.get('checkpoint')).toBe('d5')
  })
})

describe('ForecastRecordPage accessibility', () => {
  it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('has no axe violations in each theme', async (algorithm) => {
    const { container } = render(
      <ConfigProvider theme={{ algorithm }}>
        <MemoryRouter initialEntries={['/forecast']}>
          <ForecastRecordPage />
        </MemoryRouter>
      </ConfigProvider>,
    )
    await screen.findByText('+3.1 pt')
    // jsdom cannot measure layout or contrast; the palette suite measures contrast separately.
    const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 120)).join(' | ')}`)).toEqual([])
  })

  // The two shapes the default fixture does not cover: the gate hint that flagged cells point at, and
  // the note above a matrix with nothing to show yet.
  it.each([
    ['an external all response', () => withheld(), { scope: 'all', external: true } as Me['forecast'], 'forecast.cell.min_n_met_hint'],
    ['a record with no result yet', () => record({}, open), { scope: 'all', external: false } as Me['forecast'], 'forecast.record.empty'],
  ])('has no axe violations for %s', async (_name, response, hint, expected) => {
    auth.value = { user: 'reader', admin: false, forecast: hint }
    apiMock.get.mockResolvedValue(response())
    const { container } = show()
    await screen.findByText(expected)
    const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 120)).join(' | ')}`)).toEqual([])
  })
})

// ── P19: the cohort page matches the demo ────────────────────────────────────────────────────────

const limits = { max_range_months: 36, max_week_groups: 26, official_slots: 2, explore_slots: 2, wait_sec: 10, custom_per_min: 20, members_per_min: 60 }
const metadata = (extra: Partial<ForecastGroupsResponse> = {}): Partial<ForecastGroupsResponse> => ({
  summary: {
    since: '2026-10-19', sets: 1234, accepted: 1100, stocks: 400,
    marks_by_phase: { resolved: 10, waiting: 30, retrying: 2, overdue: 0 },
    next_result_not_before: '2027-06-26',
  },
  rtypes: ['Decision memo'],
  min_n: 30,
  limits,
  generated_at: '2027-06-20T07:00:00Z',
  ledger_start: '2026-10-19T00:00:00+08:00',
  capabilities: { monthly: true, custom: true, members: true },
  evaluator: { mode: '1' },
  ...extra,
})

const monthsOf = (n: number, bench = (_i: number) => 'sh000852') =>
  Array.from({ length: n }, (_, i) => {
    const month = `${2026 + Math.floor((9 + i) / 12)}-${String(((9 + i) % 12) + 1).padStart(2, '0')}`
    return { label: month, start: `${month}-01`, end: `${month}-28`, official: true, bench: bench(i), coverage, cells: cells(mature) }
  })

const settings = (over: Record<string, unknown> = {}) => ({
  enabled: true, mode: '1', bench: 'sh000852', bench_codes: [], gate_codes: [], population: { set: true, candidates: [] },
  due_sets: 0, pending: 0, refused: 0, unsettled_baseline: 0, readiness: [{ key: 'ledger_chain', ok: true }],
  dry_gate: {}, alerts: { email_to: '', has_slack: false },
  heartbeat: {
    mode: '1', last_tick_at: new Date().toISOString(), bench: {},
    pass: { kind: 'tick', started_at: '', finished_at: '', sets: 1, fetches: {}, resolved: 0, halted: 0, refused: {}, refused_by_class: {},
      finalised: 0, healed: 0, baselines: {}, awaiting_session: 0, due_unsettled: 0, oldest_due_unsettled_minutes: 0,
      stale_valve_tripped: false, errors: 0, panics: 0 },
  },
  ...over,
})

describe('ForecastRecordPage custom range accessibility', () => {
  it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('has no axe violations on the custom range with its dates, quick ranges and groups', async (algorithm) => {
    apiMock.get.mockResolvedValue(record(metadata()))
    const { container } = render(
      <ConfigProvider theme={{ algorithm }}>
        <MemoryRouter initialEntries={['/forecast?mode=custom&from=2026-10-19&to=2027-06-20&group=month']}>
          <ForecastRecordPage />
        </MemoryRouter>
      </ConfigProvider>,
    )
    await screen.findByRole('group', { name: 'forecast.range.presets' })
    // jsdom cannot measure contrast; it is checked in a browser and by the palette suite.
    const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 120)).join(' | ')}`)).toEqual([])
  })
})

describe('ForecastRecordPage shell', () => {
  it('puts the header, the tabs, the toolbar and the matrix in one card, and the methodology in another', async () => {
    show()
    await screen.findByText('+3.1 pt')
    const card = screen.getByRole('table').closest('.ant-card') as HTMLElement
    expect(within(card).getByRole('heading', { name: 'forecast.record.title' })).toBeTruthy()
    expect(within(card).getByRole('tablist')).toBeTruthy()
    expect(within(card).getByRole('button', { name: /forecast.export.button/ })).toBeTruthy()
    expect(within(card).getByText('forecast.record.population_all')).toBeTruthy()
    expect(within(card).getByText('forecast.metric.spread')).toBeTruthy()
    expect(within(card).queryByText('forecast.methodology.title')).toBeNull()
    expect(screen.getByText('forecast.methodology.title').closest('.ant-card')).not.toBe(card)
  })

  // antd draws the selected tab's label in its primary colour, 4.1:1 on white and 3.55:1 on the dark
  // surface; the page's own link colour is the step that clears 4.5:1 in each theme.
  it('draws the selected tab\'s label in the page\'s own link colour, and the others in the text colour', async () => {
    show()
    await screen.findByText('+3.1 pt')
    const probe = document.createElement('span')
    probe.style.color = forecastLinkColor(theme.getDesignToken())
    const label = (name: string) => screen.getByRole('tab', { name }).querySelector('span') as HTMLElement
    expect(label('forecast.tab.month').style.color).toBe(probe.style.color)
    expect(label('forecast.tab.custom').style.color).toBe('')
  })

  it('lets the header, the toolbar and the filters wrap instead of overflowing a narrow screen', async () => {
    show()
    await screen.findByText('+3.1 pt')
    const card = screen.getByRole('table').closest('.ant-card') as HTMLElement
    for (const part of ['header', 'toolbar']) {
      const el = card.querySelector(`[data-part="${part}"]`) as HTMLElement
      expect(el, part).not.toBeNull()
      expect(el.style.flexWrap).toBe('wrap')
    }
    // The matrix scrolls inside the card, never the page.
    expect((screen.getByRole('table').parentElement as HTMLElement).style.overflowX).toBe('auto')
  })
})

describe('ForecastRecordPage summary strip', () => {
  it('states the span, the volume and what the checkpoints are doing, and when the next result is due', async () => {
    apiMock.get.mockResolvedValue(record(metadata()))
    show()
    await screen.findByText('+3.1 pt')
    const strip = document.querySelector('[data-part="summary"]') as HTMLElement
    const text = strip.textContent!
    expect(text).toContain('forecast.summary.since:{"date":"2026-10-19"}')
    expect(text).toContain('forecast.summary.sets:{"n":"1,234"}')
    expect(text).toContain('forecast.summary.accepted:{"n":"1,100"}')
    expect(text).toContain('forecast.summary.stocks:{"n":"400"}')
    expect(text).toContain('forecast.summary.phase:{"phase":"forecast.phase.resolved","n":"10"}')
    expect(text).toContain('forecast.summary.phase:{"phase":"forecast.phase.waiting","n":"30"}')
    expect(text).toContain('forecast.summary.phase:{"phase":"forecast.phase.retrying","n":"2"}')
    expect(text).toContain('forecast.summary.next:{"date":"2027-06-26"}')
    // Phases with nothing in them are not listed.
    expect(text).not.toContain('forecast.phase.overdue')
  })

  it('words a calendar-backed next date as expected', async () => {
    apiMock.get.mockResolvedValue(record(metadata({ summary: { ...metadata().summary!, expected: true } })))
    show()
    await screen.findByText('+3.1 pt')
    expect(document.querySelector('[data-part="summary"]')!.textContent).toContain('forecast.summary.next_expected:{"date":"2027-06-26"}')
  })

  it('has no strip for a reader whose response carries none', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: true } }
    apiMock.get.mockResolvedValue(withheld())
    show()
    await screen.findByText('+3.1 pt')
    expect(document.querySelector('[data-part="summary"]')).toBeNull()
    expect(document.body.textContent).not.toContain('forecast.summary.')
  })

  it('does not announce a next result that was due before today: that is a checkpoint running late, not news', async () => {
    const lapsed = { next_result_not_before: '2026-06-09', expected: true }
    apiMock.get.mockResolvedValue(record(metadata({ summary: { ...metadata().summary!, ...lapsed } })))
    show()
    await screen.findByText('+3.1 pt')
    const text = document.querySelector('[data-part="summary"]')!.textContent!
    expect(text).not.toContain('forecast.summary.next')
    // What is late is still said, by the phase counts beside it.
    expect(text).toContain('forecast.summary.phase:{"phase":"forecast.phase.retrying","n":"2"}')
  })

  it('gives the empty-state note no date either when the only date it has has passed', async () => {
    const lapsed = { ...metadata().summary!, next_result_not_before: '2026-06-09' }
    apiMock.get.mockResolvedValue(record(metadata({ summary: lapsed }), open))
    show()
    expect(await screen.findByText('forecast.record.empty')).toBeTruthy()
    expect(document.body.textContent).not.toContain('forecast.record.empty_dated')
    expect(document.body.textContent).not.toContain('forecast.record.empty_expected')
  })

  it('still dates the empty-state note when the date is ahead', async () => {
    apiMock.get.mockResolvedValue(record(metadata(), open))
    show()
    expect(await screen.findByText('forecast.record.empty_dated:{"date":"2027-06-26"}')).toBeTruthy()
  })
})

describe('ForecastRecordPage evaluator pill', () => {
  beforeEach(() => {
    apiMock.get.mockImplementation((url: string) => {
      if (url.startsWith('/api/admin/forecast')) return Promise.resolve(settings())
      return Promise.resolve(url.startsWith('/api/forecast/groups/members') ? members : record())
    })
  })

  it('shows an administrator the evaluator state beside the export button', async () => {
    auth.value = { user: 'root', admin: true, forecast: { scope: 'all', external: false } }
    show()
    expect(await screen.findByRole('button', { name: /forecast\.eval\.pill:/ })).toBeTruthy()
  })

  it('shows a reader nothing about the evaluator and never asks for it', async () => {
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.queryByRole('button', { name: /forecast\.eval\.pill/ })).toBeNull()
    expect(urls().some((u) => u.startsWith('/api/admin/'))).toBe(false)
  })

  it('still draws the matrix when the settings cannot be read', async () => {
    auth.value = { user: 'root', admin: true, forecast: { scope: 'all', external: false } }
    apiMock.get.mockImplementation((url: string) =>
      url.startsWith('/api/admin/forecast') ? Promise.reject(new Error('settings down')) : Promise.resolve(record()))
    show()
    expect(await screen.findByText('+3.1 pt')).toBeTruthy()
    expect(await screen.findByText('forecast.eval.pill.unavailable')).toBeTruthy()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('still draws the matrix when the settings are slow', async () => {
    auth.value = { user: 'root', admin: true, forecast: { scope: 'all', external: false } }
    apiMock.get.mockImplementation((url: string) =>
      url.startsWith('/api/admin/forecast') ? new Promise(() => undefined) : Promise.resolve(record()))
    show()
    expect(await screen.findByText('+3.1 pt')).toBeTruthy()
  })

  it('does not fall over a settings payload it cannot read', async () => {
    auth.value = { user: 'root', admin: true, forecast: { scope: 'all', external: false } }
    apiMock.get.mockImplementation((url: string) => Promise.resolve(url.startsWith('/api/admin/forecast') ? { surprise: true } : record()))
    show()
    expect(await screen.findByText('+3.1 pt')).toBeTruthy()
    expect(await screen.findByText('forecast.eval.pill.unavailable')).toBeTruthy()
  })
})

describe('ForecastRecordPage report type', () => {
  const open = async (types: string[]) => {
    apiMock.get.mockResolvedValue(record(metadata({ rtypes: types })))
    show()
    await screen.findByText('+3.1 pt')
  }

  it('names the one covered type as a tag, not a control', async () => {
    await open(['Decision memo'])
    expect(screen.getByText('Decision memo')).toBeTruthy()
    expect(screen.queryByRole('combobox')).toBeNull()
    expect(urls()).toEqual(['/api/forecast/groups?mode=month'])
  })

  it('offers a select when more than one type is covered, and asks for the one chosen', async () => {
    apiMock.get.mockResolvedValue(record(metadata({ rtypes: ['Decision memo', 'Sector note'] })))
    show('/forecast?rtype=Decision+memo')
    await screen.findByText('+3.1 pt')
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'forecast.toolbar.rtype' }))
    fireEvent.click(await screen.findByTitle('Sector note'))
    await waitFor(() => expect(lastUrl()).toBe('/api/forecast/groups?mode=month&rtype=Sector+note'))
  })

  it('names a type of its own when several are covered and the link names none, instead of guessing which the server drew', async () => {
    apiMock.get.mockResolvedValue(record(metadata({ rtypes: ['Decision memo', 'Sector note'] })))
    show()
    await screen.findByText('+3.1 pt')
    await waitFor(() => expect(lastUrl()).toBe('/api/forecast/groups?mode=month&rtype=Decision+memo'))
  })

  it('sends the type from the link with the drawer\'s request too', async () => {
    apiMock.get.mockImplementation((url: string) =>
      Promise.resolve(url.startsWith('/api/forecast/groups/members') ? members : record(metadata({ rtypes: ['Decision memo', 'Sector note'] }))))
    show('/forecast?rtype=Sector+note')
    fireEvent.click(await screen.findByText('+3.1 pt'))
    await waitFor(() => expect(urls().some((u) => u.startsWith('/api/forecast/groups/members?'))).toBe(true))
    const request = urls().find((u) => u.startsWith('/api/forecast/groups/members?'))!
    expect(new URLSearchParams(request.split('?')[1]).get('rtype')).toBe('Sector note')
  })

  it('offers no type control to a reader whose response names none', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: true } }
    apiMock.get.mockResolvedValue(withheld())
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.queryByRole('combobox')).toBeNull()
    expect(document.querySelector('[data-part="rtype"]')).toBeNull()
  })

  it('gives a way back from a type the ledger does not cover', async () => {
    apiMock.get.mockRejectedValueOnce(new ApiError(400, 'bad type', 'bad_rtype'))
    show('/forecast?rtype=Nonsense')
    expect(await screen.findByText('bad type')).toBeTruthy()
    apiMock.get.mockResolvedValueOnce(record(metadata()))
    fireEvent.click(screen.getByRole('button', { name: 'forecast.record.default_rtype' }))
    expect(await screen.findByText('+3.1 pt')).toBeTruthy()
    expect(lastUrl()).toBe('/api/forecast/groups?mode=month')
  })

  it('names the type in the export header', async () => {
    await open(['Decision memo'])
    fireEvent.click(screen.getByRole('button', { name: /forecast.export.button/ }))
    expect((csvMock.downloadCSV.mock.calls[0] as [string, string])[1]).toContain('# rtype=Decision memo')
  })
})

describe('ForecastRecordPage benchmark', () => {
  it('shows that the benchmark is frozen per cohort and where it changed', async () => {
    apiMock.get.mockResolvedValue(record({ ...metadata(), groups: monthsOf(4, (i) => (i < 2 ? 'sh000852' : 'sh000906')) }))
    show()
    await screen.findAllByText('+3.1 pt')
    const bench = document.querySelector('[data-part="bench"]') as HTMLElement
    expect(bench.textContent).toContain('forecast.toolbar.bench_frozen')
    expect(bench.textContent).toContain('forecast.toolbar.bench_run:{"span":"2026-10 – 2026-11","bench":"forecast.bench.sh000852"}')
    expect(bench.textContent).toContain('forecast.toolbar.bench_run:{"span":"2026-12 – 2027-01","bench":"forecast.bench.sh000906"}')
  })

  it('draws a single benchmark as one run', async () => {
    show()
    await screen.findByText('+3.1 pt')
    const bench = document.querySelector('[data-part="bench"]') as HTMLElement
    expect(bench.textContent).toContain('forecast.toolbar.bench_run:{"span":"2026-09","bench":"forecast.bench.sh000852"}')
  })
})

describe('ForecastRecordPage monthly range', () => {
  const rowsDrawn = () => screen.getAllByRole('row').length - 1

  it('opens on the newest twelve months and offers the older ones', async () => {
    apiMock.get.mockResolvedValue(record({ ...metadata(), groups: monthsOf(15) }))
    show()
    await screen.findAllByText('+3.1 pt')
    expect(rowsDrawn()).toBe(12)
    expect(screen.getByText('2027-12')).toBeTruthy()
    expect(screen.queryByText('2026-10')).toBeNull()
    expect(screen.getByText('forecast.range.last_n:{"n":12}')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'forecast.range.show_older:{"n":3}' }))
    expect(rowsDrawn()).toBe(15)
    expect(screen.getByText('2026-10')).toBeTruthy()
    expect(screen.getByText('forecast.range.all')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'forecast.range.show_recent' }))
    expect(rowsDrawn()).toBe(12)
  })

  it('offers nothing to fold for a short record', async () => {
    apiMock.get.mockResolvedValue(record({ ...metadata(), groups: monthsOf(5) }))
    show()
    await screen.findAllByText('+3.1 pt')
    expect(rowsDrawn()).toBe(5)
    expect(screen.queryByRole('button', { name: /forecast.range.show_older/ })).toBeNull()
    expect(screen.getByText('forecast.range.all')).toBeTruthy()
  })

  it('exports the whole record, not just the months on screen', async () => {
    apiMock.get.mockResolvedValue(record({ ...metadata(), groups: monthsOf(15) }))
    show()
    await screen.findAllByText('+3.1 pt')
    fireEvent.click(screen.getByRole('button', { name: /forecast.export.button/ }))
    const text = (csvMock.downloadCSV.mock.calls[0] as [string, string])[1]
    expect(text).toContain('# benchmark=2026-10:')
    expect(text).toContain('# benchmark=2027-12:')
  })
})

describe('ForecastRecordPage custom range', () => {
  const inputs = () => screen.getAllByRole('textbox').filter((el) => /^\d{4}-\d{2}-\d{2}$/.test((el as HTMLInputElement).value)) as HTMLInputElement[]

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'], now: new Date('2027-06-20T07:00:00Z') })
  })

  it('opens the tab on the record since the ledger began, using the server\'s clock and limits', async () => {
    apiMock.get.mockResolvedValue(record(metadata()))
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    await waitFor(() => expect(lastUrl()).toBe('/api/forecast/groups?mode=custom&from=2026-10-19&to=2027-06-20&group=month'))
    expect(inputs().map((el) => el.value)).toEqual(['2026-10-19', '2027-06-20'])
  })

  it('clamps that default to what the server allows', async () => {
    apiMock.get.mockResolvedValue(record(metadata({ limits: { ...limits, max_range_months: 3 } })))
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    await waitFor(() => expect(lastUrl()).toBe('/api/forecast/groups?mode=custom&from=2027-03-20&to=2027-06-20&group=month'))
  })

  it('states the limits beside the picker', async () => {
    apiMock.get.mockResolvedValue(record(metadata()))
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    expect(await screen.findByText('forecast.range.limits:{"months":36,"weeks":26}')).toBeTruthy()
  })

  // The range was one picker whose popup held two month panels and a column of presets: 748 px, which
  // a phone cannot show and a tablet cannot fit without scrolling the page sideways. Two single-date
  // pickers open a 288 px popup, and the presets are buttons the page lays out like any other control.
  it('picks the dates with two single-date pickers, so no popup is wider than a phone', async () => {
    apiMock.get.mockResolvedValue(record(metadata()))
    const { container } = show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    await waitFor(() => expect(lastUrl()).toContain('mode=custom'))
    expect(container.querySelector('.ant-picker-range')).toBeNull()
    expect(container.querySelectorAll('[data-part="toolbar"] .ant-picker')).toHaveLength(2)
  })

  it('names each date input and groups them under the visible label', async () => {
    apiMock.get.mockResolvedValue(record(metadata()))
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    const group = await screen.findByRole('group', { name: 'forecast.range.custom' })
    expect((within(group).getByRole('textbox', { name: 'forecast.range.from' }) as HTMLInputElement).value).toBe('2026-10-19')
    expect((within(group).getByRole('textbox', { name: 'forecast.range.to' }) as HTMLInputElement).value).toBe('2027-06-20')
  })

  it('offers the quick ranges as buttons, marks the one in force, and a click sets both dates', async () => {
    apiMock.get.mockResolvedValue(record(metadata()))
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    const presets = await screen.findByRole('group', { name: 'forecast.range.presets' })
    const names = within(presets).getAllByRole('button').map((b) => [b.textContent, b.getAttribute('aria-pressed')])
    expect(names).toEqual([
      ['forecast.range.preset.last3', 'false'],
      ['forecast.range.preset.last6', 'false'],
      ['forecast.range.preset.last12', 'false'],
      ['forecast.range.preset.since_ledger', 'true'],
    ])
    fireEvent.click(within(presets).getByRole('button', { name: 'forecast.range.preset.last3' }))
    await waitFor(() => expect(lastUrl()).toBe('/api/forecast/groups?mode=custom&from=2027-03-20&to=2027-06-20&group=month'))
    expect(inputs().map((el) => el.value)).toEqual(['2027-03-20', '2027-06-20'])
    expect(within(presets).getByRole('button', { name: 'forecast.range.preset.last3' }).getAttribute('aria-pressed')).toBe('true')
  })

  it('does not offer a quick range longer than the server accepts', async () => {
    apiMock.get.mockResolvedValue(record(metadata({ limits: { ...limits, max_range_months: 6 } })))
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    const presets = await screen.findByRole('group', { name: 'forecast.range.presets' })
    expect(within(presets).getAllByRole('button').map((b) => b.textContent)).toEqual([
      'forecast.range.preset.last3',
      'forecast.range.preset.last6',
      'forecast.range.preset.since_ledger',
    ])
  })

  it('shows an inverted range as the error it is and asks nothing while it is inverted', async () => {
    apiMock.get.mockResolvedValue(record(metadata()))
    show('/forecast?mode=custom&from=2027-06-20&to=2027-01-01&group=month')
    expect(await screen.findByText('forecast.err.range_inverted')).toBeTruthy()
    expect(urls().filter((u) => u.includes('mode=custom'))).toEqual([])
  })

  it('labels the metric and grouping controls with their visible names', async () => {
    apiMock.get.mockResolvedValue(record(metadata()))
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByRole('radiogroup', { name: 'forecast.toolbar.metric' })).toBeTruthy()
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    expect(await screen.findByRole('radiogroup', { name: 'forecast.range.group' })).toBeTruthy()
  })

  // jsdom has no layout, so a 375/768/1280 px scroll width cannot be measured here (the real-browser
  // check is recorded with the PR). What can be pinned is the cause of the one overflow found: a
  // control with a fixed width that a phone cannot hold.
  it('gives no toolbar control a fixed width a 375 px screen cannot hold', async () => {
    apiMock.get.mockResolvedValue(record(metadata({ rtypes: ['Decision memo', 'Thematic note'] })))
    show('/forecast?mode=custom&from=2026-10-19&to=2027-06-20&group=month&rtype=Decision+memo')
    await screen.findByText('+3.1 pt')
    const toolbar = document.querySelector('[data-part="toolbar"]') as HTMLElement
    const fixed = [toolbar, ...Array.from(toolbar.querySelectorAll<HTMLElement>('*'))]
      .flatMap((el) => [el.style.width, el.style.minWidth])
      .filter((w) => /^\d+(\.\d+)?px$/.test(w))
      .map((w) => parseFloat(w))
    expect(fixed.length).toBeGreaterThan(0)
    // The page gutter and the card padding take 16 + 24 px from each side of the screen.
    for (const w of fixed) expect(w).toBeLessThanOrEqual(375 - 2 * (16 + 24))
  })

  it('refuses a week grouping the range is too long for, naming the limit, and asks nothing', async () => {
    apiMock.get.mockResolvedValue(record(metadata()))
    show()
    await screen.findByText('+3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    await waitFor(() => expect(lastUrl()).toContain('mode=custom'))
    const before = apiMock.get.mock.calls.length
    fireEvent.click(screen.getByText('forecast.group.week'))
    expect(await screen.findByText('forecast.err.week_range_too_long:{"weeks":26}')).toBeTruthy()
    expect(apiMock.get.mock.calls.length).toBe(before)
  })

  it('keeps the exploratory banner and p* on the custom view', async () => {
    apiMock.get.mockResolvedValue(record({ ...metadata(), mode: 'custom', official: false, range: { from: '2026-10-19', to: '2027-06-20', group: 'month' } }))
    show('/forecast?mode=custom&from=2026-10-19&to=2027-06-20&group=month')
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.custom.banner')).toBeTruthy()
  })
})

describe('ForecastRecordPage notices', () => {
  it.each(['0', 'dry'] as const)('tells every reader that settlement is paused while the evaluator is %s', async (mode) => {
    apiMock.get.mockResolvedValue(record(metadata({ evaluator: { mode } })))
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.getByText('forecast.phase.settlement_paused')).toBeTruthy()
  })

  it('says nothing while the evaluator is on, or when the response does not say', async () => {
    apiMock.get.mockResolvedValue(record(metadata({ evaluator: { mode: '1' } })))
    const view = show()
    await screen.findByText('+3.1 pt')
    expect(screen.queryByText('forecast.phase.settlement_paused')).toBeNull()
    view.unmount()
    apiMock.get.mockResolvedValue(record())
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.queryByText('forecast.phase.settlement_paused')).toBeNull()
  })

  it('explains that a record held back by the 30-name gate is the gate and not a fault', async () => {
    apiMock.get.mockResolvedValue(record({}, { state: 'small', n: 12 } as ForecastCell))
    show()
    expect(await screen.findByText('forecast.record.not_enough')).toBeTruthy()
  })

  it('does not call a finished record held back when it has results', async () => {
    show()
    await screen.findByText('+3.1 pt')
    expect(screen.queryByText('forecast.record.not_enough')).toBeNull()
  })
})

describe('ForecastRecordPage drawer modes', () => {
  it('opens the cohort\'s members, not a metrics view, for a cell that is still waiting', async () => {
    apiMock.get.mockImplementation((url: string) =>
      Promise.resolve(url.startsWith('/api/forecast/groups/members') ? { ...members, population: 'all', state: 'pending' } : record({}, { state: 'pending', resolved: 3, total: 5 } as ForecastCell)))
    show()
    fireEvent.click(await screen.findByText('forecast.cell.pending:{"done":3,"total":5}'))
    const drawer = await screen.findByRole('dialog')
    expect(await within(drawer).findByText('forecast.drawer.membership_note')).toBeTruthy()
    const request = urls().find((u) => u.startsWith('/api/forecast/groups/members?'))!
    expect(new URLSearchParams(request.split('?')[1]).get('population')).toBe('all')
  })

  it('keeps the drawer open across a metric switch, because the cell it states is the same one', async () => {
    show()
    fireEvent.click(await screen.findByText('+3.1 pt'))
    expect(await screen.findByRole('dialog')).toBeTruthy()
    fireEvent.click(screen.getByText('forecast.metric.ic'))
    // A metric switch redraws the same cells: the drawer, which states the cell, stays.
    expect(screen.getByRole('dialog')).toBeTruthy()
  })
})

// An external reader with the all scope gets states and flags and nothing countable. Every state the
// matrix can hold has to survive that shape without a crash, a zero or a count.
describe('ForecastRecordPage for an external reader with the all scope', () => {
  const everyState = () => {
    const r = withheld()
    r.groups = ['2026-06', '2026-07', '2026-08', '2026-09', '2026-10'].map((label, i) => ({
      label, start: `${label}-01`, end: `${label}-28`, official: true, bench: 'sh000852',
      cells: {
        d5: { state: 'mature', min_n_met: true, spread_bp: 310, spread_p: 0.01, spread_q: 0.04 },
        d20: { state: 'pending' },
        d60: { state: 'small' },
        d120: { state: i === 4 ? 'open' : 'purged' },
        h12: { state: 'pending' },
      } as Record<ForecastCheckpoint, ForecastCell>,
    }))
    return r
  }

  beforeEach(() => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: true } }
  })

  it('draws every state with no coverage, no strip, no count and no pending detail', async () => {
    apiMock.get.mockResolvedValue(everyState())
    show()
    await screen.findAllByText('+3.1 pt')
    expect(document.querySelector('[data-part="summary"]')).toBeNull()
    expect(document.querySelector('[data-seg]')).toBeNull()
    expect(document.querySelector('[data-part="progress"]')).toBeNull()
    expect(document.body.textContent).not.toMatch(/forecast\.cell\.(n|pending|reason|cap|not_before|expected|from_month|small):|forecast\.cov\.|forecast\.summary\./)
    expect(screen.getAllByText('forecast.cell.pending_plain').length).toBeGreaterThan(0)
    expect(screen.getAllByText('forecast.cell.small_nocount').length).toBeGreaterThan(0)
  })

  it('lists readable members of a waiting cell without a total, a count or an outcome', async () => {
    apiMock.get.mockImplementation((url: string) =>
      Promise.resolve(url.startsWith('/api/forecast/groups/members')
        ? { ...members, state: 'pending', population: 'all', members: [{ set_id: 1, symbol: '600001', report_date: '2026-09-10', made_at: '', made_on: '2026-09-10', stance: 'long', status: 'pending', excluded_reason: 'pending' }] }
        : everyState()))
    show()
    fireEvent.click((await screen.findAllByText('forecast.cell.pending_plain'))[0])
    const drawer = await screen.findByRole('dialog')
    expect(await within(drawer).findByText('600001')).toBeTruthy()
    expect(drawer.textContent).not.toMatch(/forecast\.drawer\.headline|"done"|"total"|"members"/)
    expect(within(drawer).getByText('forecast.drawer.reason.pending')).toBeTruthy()
  })

  it('opens a mature cell\'s drawer with the flag and no headline count', async () => {
    apiMock.get.mockImplementation((url: string) =>
      Promise.resolve(url.startsWith('/api/forecast/groups/members') ? members : everyState()))
    show()
    fireEvent.click((await screen.findAllByText('+3.1 pt'))[0])
    const drawer = await screen.findByRole('dialog')
    await within(drawer).findAllByText('forecast.cell.min_n_met')
    expect(drawer.textContent).not.toContain('forecast.drawer.headline')
  })
})


describe('ForecastRecordPage accessibility of the full page', () => {
  const fullRecord = () =>
    record({
      ...metadata({ evaluator: { mode: '0' } }),
      groups: [
        { label: '2026-08', start: '2026-08-01', end: '2026-08-28', official: true, bench: 'sh000852', coverage: { ...coverage, pre_live: 3, test: 2, off_benchmark: 1, pending_baseline: 1 },
          cells: { d5: mature, d20: { state: 'pending', resolved: 3, total: 9, not_before: '2026-10-26', pending_reasons: { waiting: 6 } }, d60: { state: 'small', n: 12 }, d120: { state: 'purged' }, h12: open } },
        { label: '2026-09', start: '2026-09-01', end: '2026-09-28', official: true, bench: 'sh000906', coverage, cells: cells(mature) },
      ],
    })

  it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('has no axe violations for an administrator\'s full page in each theme', async (algorithm) => {
    auth.value = { user: 'root', admin: true, forecast: { scope: 'all', external: false } }
    apiMock.get.mockImplementation((url: string) =>
      Promise.resolve(url.startsWith('/api/admin/forecast') ? settings() : url.startsWith('/api/forecast/groups/members') ? members : fullRecord()))
    const { container } = render(
      <ConfigProvider theme={{ algorithm }}>
        <MemoryRouter initialEntries={['/forecast']}>
          <ForecastRecordPage />
        </MemoryRouter>
      </ConfigProvider>,
    )
    await screen.findByRole('button', { name: /forecast\.eval\.pill:/ })
    await screen.findAllByText('+3.1 pt')
    const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 120)).join(' | ')}`)).toEqual([])
  })

  it('has no axe violations on the exploratory tab', async () => {
    vi.useFakeTimers({ toFake: ['Date'], now: new Date('2027-06-20T07:00:00Z') })
    apiMock.get.mockResolvedValue({ ...fullRecord(), mode: 'custom', official: false })
    const { container } = show('/forecast?mode=custom&from=2026-08-01&to=2026-09-30&group=month')
    await screen.findAllByText('+3.1 pt')
    const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 120)).join(' | ')}`)).toEqual([])
  })

  it('has no axe violations with the membership drawer open', async () => {
    apiMock.get.mockImplementation((url: string) =>
      Promise.resolve(url.startsWith('/api/forecast/groups/members') ? { ...members, state: 'pending', population: 'all' } : fullRecord()))
    show()
    fireEvent.click(await screen.findByText('forecast.cell.pending:{"done":3,"total":9}'))
    const drawer = await screen.findByRole('dialog')
    await within(drawer).findByText('forecast.drawer.membership_note')
    // The drawer is portalled out of the page, so it is audited where it stands; the landmark rule is
    // about whole documents and has nothing to say about a fragment.
    const result = await axe.run(drawer, { rules: { 'color-contrast': { enabled: false }, region: { enabled: false } } })
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 120)).join(' | ')}`)).toEqual([])
  })
})

