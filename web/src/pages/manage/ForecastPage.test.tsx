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
  mode: '1' as const,
  readiness: [
    { key: 'binary' as const, ok: true },
    { key: 'population' as const, ok: true },
    { key: 'dry_pass' as const, ok: true },
    { key: 'dry_gate' as const, ok: true },
    { key: 'alerts' as const, ok: true },
  ],
  dry_gate: {},
  gate_codes: ['sh000852'],
  alerts: { email_to: 'ops@example.com', has_slack: true, last: {} },
  heartbeat: {
    mode: '1' as const,
    last_tick_at: new Date().toISOString(),
    bench: {},
    pass: {
      kind: 'tick' as const, started_at: '', finished_at: '', sets: 0, fetches: {},
      resolved: 0, halted: 0, refused: {}, refused_by_class: {}, finalised: 0, healed: 0,
      baselines: {}, awaiting_session: 0, due_unsettled: 0, oldest_due_unsettled_minutes: 0,
      stale_valve_tripped: false, errors: 0, panics: 0,
    },
  },
  bench: 'sh000852',
  bench_codes: ['sh000300', 'sh000905', 'sh000852', 'sh000906'],
  population: { set: false, candidates: [] },
  due_sets: 3,
  pending: 12,
  refused: 0,
  unsettled_baseline: 0,
}

const access = {
  ok: true,
  preview: false,
  affected: 0,
  counts: { all: 2, own: 1, none: 4 },
  groups: [
    { id: 1, forecast_scope: 'none', forecast_scope_effective: 'none', forecast_scope_source: 1, external: false, counts: { all: 0, own: 0, none: 4 } },
    { id: 2, forecast_scope: 'all', forecast_scope_effective: 'all', forecast_scope_source: 2, external: false, counts: { all: 2, own: 1, none: 0 } },
  ],
}
const groupList = { groups: [{ id: 1, name: 'Default', is_default: true }, { id: 2, name: 'Internal' }] }

// Three endpoints: the settings object, the server-resolved access, and the OU names that go with it.
// Routing by URL keeps the settings tests about settings.
const byUrl = (settings: unknown) => (url: string) =>
  Promise.resolve(url === '/api/admin/forecast/access' ? access : url === '/api/admin/groups' ? groupList : settings)

const show = (initial = '/') =>
  render(
    <MemoryRouter initialEntries={[initial]}>
      <App>
        <ForecastPage />
      </App>
    </MemoryRouter>,
  )

describe('ForecastPage', () => {
  it('opens alert settings from a dashboard deep link without sending a test alert', async () => {
    apiMock.get.mockImplementation(byUrl(on))
    apiMock.post.mockClear()
    show('/manage/forecast?section=alerts')
    expect(await screen.findByLabelText('forecast.alert.email')).toBeTruthy()
    expect(apiMock.post).not.toHaveBeenCalled()
  })
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockImplementation(byUrl(on))
  })

  it('shows neither the status nor the settings button until the server has answered', async () => {
    let resolveGet: ((v: unknown) => void) | undefined
    apiMock.get.mockImplementation((url: string) =>
      url === '/api/admin/forecast'
        ? new Promise((resolve) => {
            resolveGet = resolve
          })
        : byUrl(on)(url),
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
    await waitFor(() => expect(screen.getAllByText('common.loadFailed').length).toBeGreaterThan(0))
    // The four backlog numbers are exactly what must not be invented: "0 due" is a real and
    // reassuring answer, so a page that printed it after a failed request would be lying.
    expect(screen.queryByText('forecast.eval.due')).toBeNull()
  })

  it('says the evaluator is off, and shows the counts beside it', async () => {
    apiMock.get.mockImplementation(byUrl({ ...on, enabled: false, mode: '0', due_sets: 0, pending: 0 }))
    show()
    // Off is the shipped state and reads as a fault unless named as intentional.
    await waitFor(() => expect(screen.getByText('forecast.eval.offHint')).toBeTruthy())
    expect(screen.getByText('forecast.eval.due')).toBeTruthy()
  })

  it('names the ledger benchmark and its scheduled successor instead of a bare code, without repeating the mode', async () => {
    apiMock.get.mockImplementation(byUrl({ ...on, bench: 'sh000852', bench_next: 'sh000906', bench_next_from: '2026-11' }))
    show()
    await waitFor(() => expect(screen.getByText('forecast.eval.title')).toBeTruthy())
    expect(screen.getAllByText(/forecast\.bench\.sh000852/).length).toBeGreaterThan(0)
    expect(screen.getByText(/forecast\.eval\.benchNext/)).toBeTruthy()
    // The mode switch already says whether the evaluator is on; the ledger card does not repeat it.
    expect(screen.queryByText('forecast.eval.on')).toBeNull()
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

    fireEvent.mouseDown(await screen.findByRole('combobox', { name: 'forecast.settings.bench' }))
    fireEvent.click(await screen.findByText('forecast.bench.sh000300'))
    apiMock.post.mockResolvedValue({ ok: true })
    fireEvent.click(screen.getByText('common.save'))

    // Only the changed field is posted: the switch was already true, so no `mode` rides along.
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', {
        bench_schedule: 'sh000300',
      }),
    )
    // And the page reloads, so the counts shown are the ones the save left behind rather than the
    // ones that were on screen before it. Count only the settings request: the access card has its own.
    const settingsCalls = () => apiMock.get.mock.calls.filter(([u]) => u === '/api/admin/forecast').length
    await waitFor(() => expect(settingsCalls()).toBe(2))
  })

  it('surfaces the server refusal of a bad benchmark instead of a generic failure', async () => {
    show()
    await waitFor(() => expect(screen.getByText('forecast.settings.button')).toBeTruthy())
    fireEvent.click(screen.getByText('forecast.settings.button'))

    // The page's errText is stubbed to a fixed string here; what matters is that the save's failure
    // path keeps the modal open and does not report success.
    fireEvent.mouseDown(await screen.findByRole('combobox', { name: 'forecast.settings.bench' }))
    fireEvent.click(await screen.findByText('forecast.bench.sh000300'))
    apiMock.post.mockRejectedValue(new Error('forecast_bad_bench'))
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(screen.getByText('common.save')).toBeTruthy()
  })

  it('hides the unrecorded line when the count and the failures are both zero', async () => {
    apiMock.get.mockImplementation(
      byUrl({ ...on, unrecorded: [{ rtype: '投资决策建议', n: 0, since: '2026-10-19T00:00:00Z', ok: true, store_failed_30d: 0 }] }),
    )
    show()
    await waitFor(() => expect(screen.getByText('forecast.eval.title')).toBeTruthy())
    expect(screen.queryByText(/forecast.cov.unrecorded_since/)).toBeNull()
  })

  it('shows the unrecorded line with a link to the store-failure audit filter', async () => {
    apiMock.get.mockImplementation(
      byUrl({ ...on, unrecorded: [{ rtype: '投资决策建议', n: 3, since: '2026-10-19T00:00:00Z', ok: true, store_failed_30d: 1 }] }),
    )
    show()
    await waitFor(() => expect(screen.getByText(/forecast.cov.unrecorded_since/)).toBeTruthy())
    const link = screen.getByText('forecast.cov.unrecorded_audit').closest('a') as HTMLAnchorElement
    expect(link.getAttribute('href')).toContain('action=predictions.store_failed')
  })

  // The cohort matrix moved to the reader page for everyone, administrators included. This page
  // keeps the evaluator's operations, so it asks for none of the statistics and offers none of their
  // controls.
  it('is operations only: no statistics requests, no matrix controls', async () => {
    show('/manage/forecast?mode=custom&from=2026-09-01&to=2026-09-30')
    await waitFor(() => expect(screen.getByText('forecast.eval.title')).toBeTruthy())
    await screen.findByText('forecast.access.title')
    for (const [url] of apiMock.get.mock.calls) expect(String(url)).not.toContain('/forecast/groups')
    expect(screen.queryByText('forecast.matrix.title')).toBeNull()
    expect(screen.queryByText('forecast.tab.month')).toBeNull()
    expect(screen.queryByText('forecast.tab.custom')).toBeNull()
    expect(screen.queryByText('forecast.export.button')).toBeNull()
    expect(screen.queryByText('forecast.methodology.title')).toBeNull()
    // The statistics live on the reader page, one click away.
    expect(screen.getByRole('link', { name: 'nav.forecast_record' }).getAttribute('href')).toBe('/forecast')
  })

  it('keeps every operation: status, settings, ledger start, alerts and the backlog', async () => {
    show()
    await waitFor(() => expect(screen.getByText('forecast.eval.title')).toBeTruthy())
    expect(screen.getByText('forecast.settings.button')).toBeTruthy()
    expect(screen.getByText('forecast.population.button')).toBeTruthy()
    expect(screen.getByText('forecast.alert.title')).toBeTruthy()
    expect(screen.getByText('forecast.eval.status.title')).toBeTruthy()
  })

  it('shows who has access from the server\'s own resolution, with a way to the OU editor', async () => {
    show()
    expect(await screen.findByText('forecast.access.summary:{"all":2,"own":1,"none":4}')).toBeTruthy()
    fireEvent.click(screen.getByRole('tab', { name: 'manage.operation.access' }))
    expect(apiMock.get.mock.calls.some(([u]) => u === '/api/admin/forecast/access')).toBe(true)
    expect(screen.getByRole('link', { name: 'forecast.access.edit' }).getAttribute('href')).toBe('/manage/forecast?scope=ou')
  })

  it('still shows the operations when the access summary cannot be read', async () => {
    apiMock.get.mockImplementation((url: string) =>
      url === '/api/admin/forecast/access' ? Promise.reject(new Error('offline')) : byUrl(on)(url))
    show()
    await waitFor(() => expect(screen.getByText('forecast.eval.title')).toBeTruthy())
    expect(await screen.findByText('Request failed')).toBeTruthy()
    expect(screen.getByText('forecast.settings.button')).toBeTruthy()
  })
})
