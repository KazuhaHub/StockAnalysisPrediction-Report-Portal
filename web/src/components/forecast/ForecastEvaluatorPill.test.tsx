import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import type { ForecastSettings } from '../../api/types'
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

  it('says what an off or dry evaluator means for the readers of the record', async () => {
    apiMock.get.mockResolvedValue(settings({ mode: '0', heartbeat: null }))
    show()
    fireEvent.click(await screen.findByRole('button', { name: /forecast\.eval\.pill:/ }))
    const drawer = await screen.findByRole('dialog')
    expect(within(drawer).getByText('forecast.eval.offHint')).toBeTruthy()
    expect(within(drawer).getByText('forecast.eval.never')).toBeTruthy()
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
})
