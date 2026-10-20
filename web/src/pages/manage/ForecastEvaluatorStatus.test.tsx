import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import dayjs from 'dayjs'
import ForecastEvaluatorStatus from './ForecastEvaluatorStatus'
import type { ForecastSettings } from '../../api/types'

// The operating card (P9). The parts that are load-bearing:
//   - the state is derived from the heartbeat's age, so a page left open goes stale on its own;
//   - "On" is refused while a readiness item fails, and the failing item is named;
//   - a dry run's sample is what the operator is asked to check, so it must be visible;
//   - the switch and the manual pass both post, and a 409 reaches the user through errText.

const apiMock = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('../../api/client', () => ({
  api: apiMock,
  errText: (e: unknown, t: (k: string) => string) => t('err.forecast_eval_off'),
}))

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const now = dayjs().toISOString()

const base: ForecastSettings = {
  enabled: false,
  mode: 'dry',
  bench: 'sh000852',
  bench_codes: ['sh000852'],
  gate_codes: ['sh000852'],
  population: { set: true, candidates: [] },
  due_sets: 0,
  pending: 0,
  refused: 0,
  unsettled_baseline: 0,
  unrecorded: [],
  readiness: [
    { key: 'binary', ok: true },
    { key: 'population', ok: true },
    { key: 'dry_pass', ok: true },
    { key: 'dry_gate', ok: true },
    { key: 'alerts', ok: true },
  ],
  dry_gate: {},
  heartbeat: {
    mode: 'dry',
    last_tick_at: now,
    bench: {},
    pass: {
      kind: 'tick',
      started_at: now,
      finished_at: now,
      sets: 1,
      fetches: {},
      resolved: 0,
      halted: 0,
      refused: {},
      refused_by_class: {},
      finalised: 0,
      healed: 0,
      baselines: {},
      awaiting_session: 0,
      due_unsettled: 0,
      oldest_due_unsettled_minutes: 0,
      stale_valve_tripped: false,
      errors: 0,
      panics: 0,
    },
  },
}

function show(value: Partial<ForecastSettings> = {}) {
  const onChanged = vi.fn()
  render(
    <App>
      <MemoryRouter>
        <ForecastEvaluatorStatus settings={{ ...base, ...value }} onChanged={onChanged} />
      </MemoryRouter>
    </App>,
  )
  return { onChanged }
}

describe('ForecastEvaluatorStatus', () => {
  beforeEach(() => {
    apiMock.post.mockReset()
    apiMock.post.mockResolvedValue({ ok: true })
  })

  it('names the state from the mode and the heartbeat', () => {
    show()
    expect(screen.getByText('forecast.eval.status.dry')).toBeTruthy()
  })

  it('goes stale when the heartbeat is old, without a reload', () => {
    show({
      mode: '1',
      heartbeat: { ...base.heartbeat!, last_tick_at: dayjs().subtract(11, 'minute').toISOString() },
    })
    expect(screen.getByText('forecast.eval.status.stale')).toBeTruthy()
  })

  it('shows an error state when the last pass failed', () => {
    show({
      mode: '1',
      heartbeat: { ...base.heartbeat!, pass: { ...base.heartbeat!.pass, errors: 2, last_error: 'boom' } },
    })
    expect(screen.getByText('forecast.eval.status.error')).toBeTruthy()
    expect(screen.getByText('boom')).toBeTruthy()
  })

  it('disables On while a readiness item fails, and names it', () => {
    show({ readiness: base.readiness.map((r) => (r.key === 'dry_gate' ? { ...r, ok: false, detail: 'no check' } : r)) })
    const on = screen.getByRole('radio', { name: 'forecast.eval.mode.on' }) as HTMLInputElement
    expect(on.disabled).toBe(true)
    const alerts = screen.getAllByText(/forecast\.eval\.readiness\.failed/)
    expect(alerts.length).toBeGreaterThan(0)
  })

  it('switches to dry without a confirmation', async () => {
    show({ mode: '0' })
    fireEvent.click(screen.getByRole('radio', { name: 'forecast.eval.mode.dry' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', { mode: 'dry' }))
  })

  it('renders the dry sample with its vendor triple', () => {
    show({
      heartbeat: {
        ...base.heartbeat!,
        dry_sample: [{ set_id: 7, mark: 'd5', kind: 'settle', market: 'sh', code: '600160', source: 'tencent' }],
      },
    })
    expect(screen.getByText('sh600160')).toBeTruthy()
    expect(screen.getByText('tencent')).toBeTruthy()
  })

  it('posts a dry-run check with its note', async () => {
    show()
    fireEvent.change(screen.getByLabelText('forecast.eval.gate.note'), { target: { value: 'closes match' } })
    fireEvent.click(screen.getByRole('button', { name: 'forecast.eval.gate.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', {
        dry_gate: { code: 'sh000852', note: 'closes match' },
      }),
    )
  })

  it('disables Evaluate now while the evaluator is off', () => {
    show({ mode: '0' })
    const button = screen.getByRole('button', { name: 'forecast.eval.run_now' }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
  })
})
