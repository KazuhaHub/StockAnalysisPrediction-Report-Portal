import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
import { MemoryRouter } from 'react-router'
import { ApiError } from '../api/client'
import type { ForecastCell, ForecastGroupsResponse, Me } from '../api/types'
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
    await screen.findByText('3.1 pt')
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.custom' }))
    fireEvent.click(screen.getByRole('tab', { name: 'forecast.tab.month' }))
    fireEvent.click(await screen.findByText('3.1 pt'))
    await waitFor(() => expect(urls().some((u) => u.startsWith('/api/forecast/groups/members'))).toBe(true))
    expect(urls().length).toBeGreaterThan(1)
    for (const u of urls()) {
      expect(u.startsWith('/api/forecast/groups')).toBe(true)
      expect(u).not.toContain('/api/admin/')
    }
  })

  it('reads the monthly record by default and sends no range with it', async () => {
    show()
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
    expect(urls()).toEqual(['/api/forecast/groups?mode=month'])
  })
})

describe('ForecastRecordPage states', () => {
  it('shows a loading skeleton, marked busy, until the answer arrives', async () => {
    let resolve!: (value: ForecastGroupsResponse) => void
    apiMock.get.mockImplementation(() => new Promise<ForecastGroupsResponse>((done) => { resolve = done }))
    const { container } = show()
    expect(container.querySelector('.ant-skeleton')).not.toBeNull()
    expect(container.querySelector('[aria-busy="true"]')).not.toBeNull()
    expect(screen.queryByText('3.1 pt')).toBeNull()
    await act(async () => resolve(record()))
    expect(await screen.findByText('3.1 pt')).toBeTruthy()
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
    expect(await screen.findByText('3.1 pt')).toBeTruthy()
  })

  it('shows any other failure as an alert with a retry button', async () => {
    apiMock.get.mockRejectedValueOnce(new Error('offline'))
    const { container } = show()
    expect(await screen.findByText('offline')).toBeTruthy()
    expect(container.querySelector('.ant-alert-error')).not.toBeNull()
    apiMock.get.mockResolvedValueOnce(record())
    fireEvent.click(screen.getByRole('button', { name: 'common.retry' }))
    expect(await screen.findByText('3.1 pt')).toBeTruthy()
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
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
    expect(screen.getByText('forecast.record.population_all')).toBeTruthy()
    expect(screen.getAllByText('forecast.disclaimer').length).toBeGreaterThan(0)
    expect(screen.getByRole('heading', { name: 'forecast.record.title' })).toBeTruthy()
  })

  it('names the reader\'s own unit for an own reader', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: false, ou: 'Research Desk' } }
    show()
    await screen.findByText('3.1 pt')
    expect(screen.getByText('forecast.record.population_own:{"ou":"Research Desk"}')).toBeTruthy()
  })

  it('does not invent a unit name when the server gave none', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: false } }
    show()
    await screen.findByText('3.1 pt')
    expect(screen.getByText('forecast.record.population_own_unnamed')).toBeTruthy()
    expect(document.body.textContent).not.toContain('population_own:')
  })

  it('says all reports for an administrator whatever the hint says', async () => {
    auth.value = { user: 'root', admin: true, forecast: { scope: 'all', external: false } }
    show()
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
    expect(screen.getByText('forecast.record.population_all')).toBeTruthy()
    expect(document.body.textContent).not.toContain('population_own')
  })

  it('follows an answer that narrows the population, and names the unit it was rooted at', async () => {
    apiMock.get.mockResolvedValue(record({ scope: 'own', scope_ou: 'Desk' }))
    show()
    await screen.findByText('3.1 pt')
    expect(screen.getByText('forecast.record.population_own:{"ou":"Desk"}')).toBeTruthy()
    expect(screen.queryByText('forecast.record.population_all')).toBeNull()
  })

  it('does not borrow a stale unit name when the answer is own but names none', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: false, ou: 'Old Desk' } }
    apiMock.get.mockResolvedValue(record({ scope: 'own' }))
    show()
    await screen.findByText('3.1 pt')
    expect(screen.getByText('forecast.record.population_own_unnamed')).toBeTruthy()
    expect(document.body.textContent).not.toContain('Old Desk')
  })

  it('labels the export with the population of the answer, not of the hint', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: false, ou: 'Old Desk' } }
    apiMock.get.mockResolvedValue(record({ scope: 'all' }))
    show()
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
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
  it('offers the exploratory tab to an internal reader and loads it only once a range is chosen', async () => {
    show('/forecast?mode=custom')
    await screen.findByRole('tab', { name: 'forecast.tab.custom' })
    expect(screen.getByRole('tab', { name: 'forecast.tab.month' })).toBeTruthy()
    // No range yet: nothing is asked, and the page says what it is waiting for.
    expect(screen.getByText('forecast.custom.banner')).toBeTruthy()
    expect(screen.getByText('forecast.record.pick_range')).toBeTruthy()
    expect(apiMock.get).not.toHaveBeenCalled()
  })

  it('requests a custom range with its dates and grouping', async () => {
    apiMock.get.mockResolvedValue(record({ mode: 'custom', official: false, range: { from: '2026-09-01', to: '2026-09-30', group: 'month' } }))
    show('/forecast?mode=custom&from=2026-09-01&to=2026-09-30&group=month')
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
    expect(screen.getByText(/p\*=0\.012/)).toBeTruthy()
    expect(screen.getByText('forecast.tag.exploratory')).toBeTruthy()
  })

  it('shows the record without p* or the exploratory tag', async () => {
    show()
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
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
    expect(await screen.findByText('3.1 pt')).toBeTruthy()
  })
})

describe('ForecastRecordPage volume', () => {
  it('draws an external all response without coverage, counts or a crash', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'all', external: true } }
    apiMock.get.mockResolvedValue(withheld())
    show()
    await screen.findByText('3.1 pt')
    expect(document.body.textContent).toContain('forecast.cell.min_n_met')
    expect(screen.queryByText('forecast.col.coverage')).toBeNull()
    expect(document.body.textContent).not.toMatch(/forecast\.cell\.n:|forecast\.cov\./)
  })

  it('keeps coverage and counts for an external reader of their own unit', async () => {
    auth.value = { user: 'reader', admin: false, forecast: { scope: 'own', external: true, ou: 'Client A' } }
    show()
    await screen.findByText('3.1 pt')
    expect(screen.getByText('forecast.col.coverage')).toBeTruthy()
    expect(document.body.textContent).toContain('forecast.cell.n:{"n":42}')
  })

  it('does not link a reader to the administrator audit log', async () => {
    show()
    await screen.findByText('3.1 pt')
    expect(screen.queryByRole('link', { name: /forecast.cov.rejected/ })).toBeNull()
    expect(document.body.textContent).toContain('forecast.cov.rejected')
  })

  it('lets an administrator follow rejected sets to the audit log', async () => {
    auth.value = { user: 'root', admin: true, forecast: { scope: 'all', external: false } }
    show()
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
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
    await screen.findByText('3.1 pt')
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
    fireEvent.click(await screen.findByText('3.1 pt'))
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
    await screen.findByText('3.1 pt')
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
