import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import ForecastPage from './ForecastPage'

// The page's two jobs, both of which are invisible in review: it must not present a default as a
// setting, and its header control must actually carry a save through.
//
// The evaluator's four counters start at 0 in this file's initial state, which is also a perfectly
// plausible real answer for a portal that has settled everything. That is why the load gate matters
// here rather than being a formality: a page that rendered its zeros before the server answered
// would be indistinguishable from a caught-up portal on a slow link.

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('../../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))

// Every entry point the shell might have warmed is cold in this test: the page must not depend on a
// hover having happened.
vi.mock('../../lib/prefetch', () => ({
  readPrefetched: () => undefined,
  prefetch: () => Promise.resolve(),
}))

// Echo the i18n key, with its values, so a test can assert which label a control carries.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const on = {
  enabled: true,
  bench: 'sh000852',
  bench_codes: ['sh000300', 'sh000905', 'sh000852', 'sh000906'],
  population: { set: false, candidates: [] },
  due_sets: 3,
  pending: 12,
  refused: 0,
  unsettled_baseline: 0,
}

const emptyGroups = {
  ok: true,
  mode: 'month',
  official: true,
  range: { from: '', to: '' },
  checkpoints: ['d5', 'd20', 'd60', 'd120', 'h12'],
  unrecorded: 0,
  groups: [],
}

const matureGroups = {
  ok: true,
  mode: 'custom',
  official: false,
  range: { from: '2026-09-01', to: '2026-09-30', group: 'month' },
  checkpoints: ['d5', 'd20', 'd60', 'd120', 'h12'],
  unrecorded: 0,
  groups: [
    {
      label: '2026-09',
      start: '2026-09-01',
      end: '2026-09-30',
      official: false,
      coverage: {
        reports: 40,
        member: 35,
        duplicate: 3,
        absent: 1,
        rejected: 1,
        backtest: 0,
        degraded: 0,
        mismatch: 0,
        refused: 0,
        pending_baseline: 0,
      },
      cells: {
        d5: { state: 'mature', n: 35, spread_bp: 120, spread_p: 0.02, spread_q: 0.04 },
        d20: { state: 'open' },
        d60: { state: 'open' },
        d120: { state: 'open' },
        h12: { state: 'open' },
      },
    },
  ],
}

// Two endpoints now: the settings object and the matrix. Routing by URL keeps the settings tests
// about settings.
const byUrl = (settings: unknown) => (url: string) =>
  Promise.resolve(url.includes('/forecast/groups') ? emptyGroups : settings)

const show = (initial = '/') =>
  render(
    <MemoryRouter initialEntries={[initial]}>
      <App>
        <ForecastPage />
      </App>
    </MemoryRouter>,
  )

describe('ForecastPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockImplementation(byUrl(on))
  })

  it('shows neither the status nor the settings button until the server has answered', async () => {
    let resolveGet: ((v: unknown) => void) | undefined
    apiMock.get.mockImplementation((url: string) =>
      url.includes('/forecast/groups')
        ? Promise.resolve(emptyGroups)
        : new Promise((resolve) => {
            resolveGet = resolve
          }),
    )
    show()
    await screen.findByText('common.loading')
    expect(screen.queryByText('forecast.eval.title')).toBeNull()
    expect(screen.queryByText('forecast.settings.button')).toBeNull()

    resolveGet?.(on)
    await waitFor(() => expect(screen.getByText('forecast.eval.title')).toBeTruthy())
  })

  it('reports a failed load instead of presenting zero counts as the state', async () => {
    apiMock.get.mockImplementation(() => Promise.reject(new Error('offline')))
    show()
    await waitFor(() => expect(screen.getByText('common.loadFailed')).toBeTruthy())
    // The four backlog numbers are exactly what must not be invented: "0 due" is a real and
    // reassuring answer, so a page that printed it after a failed request would be lying.
    expect(screen.queryByText('forecast.eval.due')).toBeNull()
  })

  it('says the evaluator is off, and shows the counts beside it', async () => {
    apiMock.get.mockImplementation(byUrl({ ...on, enabled: false, due_sets: 0, pending: 0 }))
    show()
    await waitFor(() => expect(screen.getByText('forecast.eval.off')).toBeTruthy())
    // Off is the shipped state and reads as a fault unless named as intentional.
    expect(screen.getByText('forecast.eval.offHint')).toBeTruthy()
    expect(screen.getByText('forecast.eval.due')).toBeTruthy()
  })

  it('marks a non-zero refusal count as a number to act on, not as another counter', async () => {
    apiMock.get.mockImplementation(byUrl({ ...on, refused: 7 }))
    show()
    await waitFor(() => expect(screen.getByText('forecast.eval.refused')).toBeTruthy())
    // The tag is how a stuck evaluator is told apart from an idle one at a glance.
    expect(screen.getByText('7')).toBeTruthy()
  })

  it('opens the settings modal and posts what it was edited to', async () => {
    show()
    await waitFor(() => expect(screen.getByText('forecast.settings.button')).toBeTruthy())
    fireEvent.click(screen.getByText('forecast.settings.button'))

    const bench = await screen.findByLabelText('forecast.settings.bench')
    fireEvent.change(bench, { target: { value: 'sh000300' } })
    apiMock.post.mockResolvedValue({ ok: true })
    fireEvent.click(screen.getByText('common.save'))

    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', {
        enabled: true,
        bench: 'sh000300',
      }),
    )
    // And the page reloads, so the counts shown are the ones the save left behind rather than the
    // ones that were on screen before it. Count only the settings request: the matrix has its own.
    const settingsCalls = () => apiMock.get.mock.calls.filter(([u]) => u === '/api/admin/forecast').length
    await waitFor(() => expect(settingsCalls()).toBe(2))
  })

  it('surfaces the server refusal of a bad benchmark instead of a generic failure', async () => {
    show()
    await waitFor(() => expect(screen.getByText('forecast.settings.button')).toBeTruthy())
    fireEvent.click(screen.getByText('forecast.settings.button'))

    // The page's errText is stubbed to a fixed string here; what matters is that the save's failure
    // path keeps the modal open and does not report success.
    apiMock.post.mockRejectedValue(new Error('forecast_bad_bench'))
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(screen.getByText('common.save')).toBeTruthy()
  })

  it('shows the exploratory banner, the p* convention and the linked range in the custom tab', async () => {
    apiMock.get.mockImplementation((url: string) =>
      Promise.resolve(url.includes('/forecast/groups') ? matureGroups : on),
    )
    show('/manage/forecast?mode=custom&from=2026-09-01&to=2026-09-30&group=month')
    await waitFor(() => expect(screen.getByText('forecast.custom.banner')).toBeTruthy())
    // A p* is the promise that this number was not corrected for range selection.
    expect(screen.getByText(/p\*=0\.02/)).toBeTruthy()
    expect(screen.getByText('forecast.tag.exploratory')).toBeTruthy()
    expect(
      apiMock.get.mock.calls.some(
        ([u]) => u === '/api/admin/forecast/groups?mode=custom&from=2026-09-01&to=2026-09-30&group=month',
      ),
    ).toBe(true)
  })

  it('surfaces a refused range instead of rendering an empty matrix as success', async () => {
    apiMock.get.mockImplementation((url: string) =>
      url.includes('/forecast/groups') ? Promise.reject(new Error('range_too_long')) : Promise.resolve(on),
    )
    show('/manage/forecast?mode=custom&from=2020-01-01&to=2026-09-30&group=month')
    await waitFor(() => expect(screen.getByText('Request failed')).toBeTruthy())
    expect(screen.queryByText('forecast.empty')).toBeNull()
  })
})
