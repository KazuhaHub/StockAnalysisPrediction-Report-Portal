import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { theme } from 'antd'
import { MemoryRouter } from 'react-router'
import type { ForecastSettings } from '../../api/types'
import { forecastLinkColor } from '../../lib/forecastPresentation'
import ForecastEvaluatorPill from './ForecastEvaluatorPill'

// The administrator's evaluator pill on the record page: the status at a glance, the detail in a
// drawer, the full console on the operations page. It reads the one admin route that carries the
// heartbeat, and its failure is its own: the record around it never depends on it.

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))

vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  api: apiMock,
  errText: (e: unknown) => (e instanceof Error ? e.message : 'failed'),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString()

function settings(over: Partial<ForecastSettings> = {}, tick = 1, pass: Record<string, unknown> = {}): ForecastSettings {
  return {
    enabled: true, mode: '1', bench: 'sh000852', bench_codes: [], gate_codes: [], population: { set: true, candidates: [] },
    due_sets: 0, pending: 0, refused: 0, unsettled_baseline: 0, readiness: [{ key: 'ledger_chain', ok: true }],
    dry_gate: {}, alerts: { email_to: '', has_slack: false },
    heartbeat: {
      mode: '1', last_tick_at: minutesAgo(tick), bench: {},
      pass: {
        kind: 'tick', started_at: minutesAgo(tick), finished_at: minutesAgo(tick), sets: 412, fetches: {}, resolved: 6, halted: 0,
        refused: {}, refused_by_class: {}, finalised: 0, healed: 0, baselines: {}, awaiting_session: 0, due_unsettled: 0,
        oldest_due_unsettled_minutes: 0, stale_valve_tripped: false, errors: 0, panics: 0, ...pass,
      },
    },
    ...over,
  } as ForecastSettings
}

const show = () => render(<MemoryRouter><ForecastEvaluatorPill /></MemoryRouter>)

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.get.mockResolvedValue(settings())
})
afterEach(() => {
  vi.useRealTimers()
})

describe('ForecastEvaluatorPill', () => {
  it('asks the one admin route that carries the heartbeat, once, and names the status', async () => {
    show()
    const pill = await screen.findByRole('button', { name: /forecast\.eval\.pill:/ })
    expect(pill.textContent).toContain('forecast.eval.status.healthy')
    expect(apiMock.get.mock.calls.map(([u]) => String(u))).toEqual(['/api/admin/forecast'])
  })

  it.each([
    ['off', settings({ mode: '0', heartbeat: null }), 'forecast.eval.status.off'],
    ['dry', settings({ mode: 'dry' }), 'forecast.eval.status.dry'],
    ['stale', settings({}, 31), 'forecast.eval.status.stale'],
    ['lagging', settings({}, 1, { oldest_due_unsettled_minutes: 400 }), 'forecast.eval.status.lagging'],
    ['error', settings({}, 1, { errors: 1, last_error: 'ledger write timed out' }), 'forecast.eval.status.error'],
  ])('shows the %s state', async (_name, value, label) => {
    apiMock.get.mockResolvedValue(value)
    show()
    const pill = await screen.findByRole('button', { name: /forecast\.eval\.pill:/ })
    expect(pill.textContent).toContain(label)
  })

  it('opens a drawer with the state, the last run, the explanation and the way to the operations page', async () => {
    apiMock.get.mockResolvedValue(settings({}, 1, { errors: 1, last_error: 'ledger write timed out' }))
    show()
    fireEvent.click(await screen.findByRole('button', { name: /forecast\.eval\.pill:/ }))
    const drawer = await screen.findByRole('dialog')
    expect(within(drawer).getByText('forecast.eval.status.title')).toBeTruthy()
    expect(within(drawer).getByText('ledger write timed out')).toBeTruthy()
    expect(within(drawer).getByText('forecast.eval.mode.on')).toBeTruthy()
    expect(within(drawer).getByRole('link', { name: 'forecast.eval.pill.operations' }).getAttribute('href')).toBe('/manage/forecast')
  })

  it.each([
    ['off', settings({ mode: '0', heartbeat: null }), 'forecast.eval.pill.off_hint', 'forecast.eval.offHint'],
    ['dry', settings({ mode: 'dry' }), 'forecast.eval.pill.dry_hint', 'forecast.eval.dryHint'],
  ])('explains the %s state for the record, in words that fit this drawer', async (_name, value, hint, operationsHint) => {
    // The operations page's hints point at counts "below" and a sample to check; this drawer has neither.
    apiMock.get.mockResolvedValue(value)
    show()
    fireEvent.click(await screen.findByRole('button', { name: /forecast\.eval\.pill:/ }))
    const drawer = await screen.findByRole('dialog')
    expect(within(drawer).getByText(hint)).toBeTruthy()
    expect(within(drawer).queryByText(operationsHint)).toBeNull()
  })

  it('says a pass has never run when there is no heartbeat', async () => {
    apiMock.get.mockResolvedValue(settings({ mode: '0', heartbeat: null }))
    show()
    fireEvent.click(await screen.findByRole('button', { name: /forecast\.eval\.pill:/ }))
    expect(within(await screen.findByRole('dialog')).getByText('forecast.eval.never')).toBeTruthy()
  })

  it('draws the drawer\'s field names in the secondary text colour, not antd\'s faint tertiary one', async () => {
    show()
    fireEvent.click(await screen.findByRole('button', { name: /forecast\.eval\.pill:/ }))
    const drawer = await screen.findByRole('dialog')
    const labels = Array.from(drawer.querySelectorAll<HTMLElement>('.ant-descriptions-item-label'))
    expect(labels.length).toBeGreaterThanOrEqual(4)
    const probe = document.createElement('span')
    probe.style.color = theme.getDesignToken().colorTextSecondary
    for (const label of labels) expect(label.style.color).toBe(probe.style.color)
  })

  it('draws the retry link in the page\'s own link colour, not antd\'s primary', async () => {
    apiMock.get.mockRejectedValueOnce(new Error('boom'))
    show()
    const retry = await screen.findByRole('button', { name: /common\.retry/ })
    const probe = document.createElement('span')
    probe.style.color = forecastLinkColor(theme.getDesignToken())
    expect(retry.style.color).toBe(probe.style.color)
  })

  it('keeps a failed read to itself: a small unavailable note with a retry, no drawer', async () => {
    apiMock.get.mockRejectedValueOnce(new Error('boom'))
    show()
    expect(await screen.findByText('forecast.eval.pill.unavailable')).toBeTruthy()
    expect(screen.queryByRole('dialog')).toBeNull()
    apiMock.get.mockResolvedValueOnce(settings())
    fireEvent.click(screen.getByRole('button', { name: /common\.retry/ }))
    await waitFor(() => expect(screen.getByRole('button', { name: /forecast\.eval\.pill:/ })).toBeTruthy())
    expect(apiMock.get).toHaveBeenCalledTimes(2)
  })

  describe('staying current', () => {
    const tick = async (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
    const pill = () => screen.getByRole('button', { name: /forecast\.eval\.pill:/ })

    // A page left open past the stall threshold must not call a healthy evaluator stale just because
    // the pill never asked again: the age grows on its own clock, so the heartbeat has to be re-read.
    it('reads the heartbeat again on a timer shorter than the stall threshold, so a healthy evaluator stays healthy', async () => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
      apiMock.get.mockImplementation(() => Promise.resolve(settings()))
      show()
      await tick(0)
      expect(pill().textContent).toContain('forecast.eval.status.healthy')
      await tick(11 * 60_000)
      expect(pill().textContent).toContain('forecast.eval.status.healthy')
      expect(apiMock.get.mock.calls.length).toBeGreaterThan(5)
    })

    it('still turns stale when the refreshed heartbeat is really old', async () => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
      const old = settings()
      apiMock.get.mockImplementation(() => Promise.resolve(old))
      show()
      await tick(0)
      expect(pill().textContent).toContain('forecast.eval.status.healthy')
      await tick(11 * 60_000)
      expect(pill().textContent).toContain('forecast.eval.status.stale')
    })

    it('reads it again when the drawer opens, so what it shows is current', async () => {
      show()
      const button = await screen.findByRole('button', { name: /forecast\.eval\.pill:/ })
      expect(apiMock.get).toHaveBeenCalledTimes(1)
      fireEvent.click(button)
      await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(2))
    })

    it('says the status is unavailable when a refresh fails, rather than let the age claim the evaluator stopped', async () => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
      apiMock.get.mockResolvedValueOnce(settings())
      show()
      await tick(0)
      expect(pill().textContent).toContain('forecast.eval.status.healthy')
      apiMock.get.mockRejectedValue(new Error('offline'))
      await tick(60_000)
      expect(screen.getByText('forecast.eval.pill.unavailable')).toBeTruthy()
      expect(screen.queryByRole('button', { name: /forecast\.eval\.pill:/ })).toBeNull()
    })

    it('does not let a slow earlier answer overwrite a newer one', async () => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
      let first!: (value: ForecastSettings) => void
      apiMock.get.mockImplementationOnce(() => new Promise<ForecastSettings>((done) => { first = done }))
      apiMock.get.mockImplementation(() => Promise.resolve(settings({ mode: '0', heartbeat: null })))
      show()
      await tick(60_000)
      expect(pill().textContent).toContain('forecast.eval.status.off')
      await act(async () => first(settings()))
      expect(pill().textContent).toContain('forecast.eval.status.off')
    })

    it('reads it again when the tab becomes visible, since its clock moved while it was away', async () => {
      show()
      await screen.findByRole('button', { name: /forecast\.eval\.pill:/ })
      expect(apiMock.get).toHaveBeenCalledTimes(1)
      document.dispatchEvent(new Event('visibilitychange'))
      await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(2))
    })

    it('stops asking once the page is gone', async () => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
      const view = show()
      await tick(0)
      view.unmount()
      const calls = apiMock.get.mock.calls.length
      await tick(10 * 60_000)
      expect(apiMock.get.mock.calls.length).toBe(calls)
    })
  })
})
