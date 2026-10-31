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

// Reason keys echo their values, so a test can see which benchmark a failure names. A reason this
// page has no wording for answers with the bare key, as i18next does for a missing key.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, v?: Record<string, unknown>) =>
      v && k.startsWith('forecast.eval.readiness.reason.') && !k.endsWith('.not_yet_known') ? `${k} ${JSON.stringify(v)}` : k,
  }),
}))

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
    { key: 'ledger_chain', ok: true },
  ],
  dry_gate: {},
  alerts: { email_to: '', has_slack: false },
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
      bodies: 1,
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

  it('goes lagging when the stale-data valve tripped', () => {
    show({
      mode: '1',
      heartbeat: { ...base.heartbeat!, pass: { ...base.heartbeat!.pass, stale_valve_tripped: true } },
    })
    expect(screen.getByText('forecast.eval.status.lagging')).toBeTruthy()
  })

  it('shows an error state when the last pass failed', () => {
    show({
      mode: '1',
      heartbeat: { ...base.heartbeat!, pass: { ...base.heartbeat!.pass, errors: 2, last_error: 'boom' } },
    })
    expect(screen.getByText('forecast.eval.status.error')).toBeTruthy()
    expect(screen.getByText('boom')).toBeTruthy()
  })

  it.each(['dry', '1'] as const)('warns about a removed settlement primary in mode %s', (mode) => {
    show({ mode, readiness: base.readiness.map((item) => item.key === 'ledger_chain' ? { ...item, ok: false } : item) })
    expect(screen.getByText('quoteAdmin.ledgerNoPrimary')).toBeTruthy()
    expect(screen.getByText('forecast.eval.status.error')).toBeTruthy()
    expect(screen.queryByText('forecast.eval.status.healthy')).toBeNull()
  })

  it('disables On while a readiness item fails, and names it', () => {
    show({ readiness: base.readiness.map((r) => (r.key === 'dry_gate' ? { ...r, ok: false, detail: 'no check' } : r)) })
    const on = screen.getByRole('radio', { name: 'forecast.eval.mode.on' }) as HTMLInputElement
    expect(on.disabled).toBe(true)
    const alerts = screen.getAllByText(/forecast\.eval\.readiness\.failed/)
    expect(alerts.length).toBeGreaterThan(0)
  })

  it("words a failing item from its reason in the reader's language, naming the benchmark", () => {
    show({
      readiness: base.readiness.map((r) =>
        r.key === 'dry_gate' ? { ...r, ok: false, detail: 'no dry-run check for sh000852', reason: 'gate_missing', codes: ['sh000852'] } : r),
    })
    const row = screen.getByText(/forecast\.eval\.readiness\.reason\.gate_missing/)
    expect(row.textContent).toContain('forecast.bench.sh000852')
    expect(screen.queryByText(/no dry-run check/)).toBeNull()
  })

  it('carries the count a reason refers to', () => {
    show({ readiness: base.readiness.map((r) => (r.key === 'dry_pass' ? { ...r, ok: false, reason: 'dry_pass_errors', count: 3 } : r)) })
    expect(screen.getByText(/forecast\.eval\.readiness\.reason\.dry_pass_errors/).textContent).toContain('"count":3')
  })

  it('falls back to the detail from a server that sends no reason', () => {
    show({ readiness: base.readiness.map((r) => (r.key === 'alerts' ? { ...r, ok: false, detail: 'older server text' } : r)) })
    expect(screen.getByText(/older server text/)).toBeTruthy()
  })

  it('falls back to the detail for a reason this page has no wording for', () => {
    // A page cached from before a newer server added a reason must not show the raw key.
    const unknown = { key: 'alerts' as const, ok: false, detail: 'newer server text', reason: 'not_yet_known' as never }
    show({ readiness: base.readiness.map((r) => (r.key === 'alerts' ? unknown : r)) })
    expect(screen.getByText(/newer server text/)).toBeTruthy()
    expect(screen.queryByText(/reason\.not_yet_known/)).toBeNull()
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

  describe('page shape by mode', () => {
    const on = { mode: '1' as const, heartbeat: { ...base.heartbeat!, mode: '1' as const } }
    const notDry = base.readiness.map((r) => (r.key === 'dry_pass' ? { ...r, ok: false, reason: 'not_dry' as const } : r))

    it('shows only monitoring when on: no checklist, sample or gate form', () => {
      show({ ...on, readiness: notDry, dry_gate: { sh000852: { at: now, by: 'Kazuha', note: 'ok' } } })
      expect(screen.getByText('forecast.eval.summary')).toBeTruthy()
      expect(screen.queryByText('forecast.eval.readiness.title')).toBeNull()
      expect(screen.queryByText(/forecast\.eval\.readiness\.dry_pass/)).toBeNull()
      expect(screen.queryByText('forecast.eval.sample.title')).toBeNull()
      expect(screen.queryByText('forecast.eval.gate.title')).toBeNull()
      expect(screen.queryByText(/forecast\.eval\.fetches/)).toBeNull()
    })

    it('raises a still-relevant failing item as a warning when on', () => {
      show({ ...on, readiness: notDry.map((r) => (r.key === 'alerts' ? { ...r, ok: false, reason: 'alerts_untested' as const } : r)) })
      // The reason says what is wrong on its own; prefixing the item's positive label reads as a contradiction.
      const warning = screen.getByText(/forecast\.eval\.readiness\.reason\.alerts_untested/)
      expect(warning.textContent).not.toContain('forecast.eval.readiness.alerts')
    })

    it('keeps On enabled and selected while on, even though the dry-pass item fails', () => {
      show({ ...on, readiness: notDry })
      const radio = screen.getByRole('radio', { name: 'forecast.eval.mode.on' }) as HTMLInputElement
      expect(radio.disabled).toBe(false)
      expect(radio.checked).toBe(true)
    })

    it('brings the gate form back when a benchmark awaits its check', () => {
      show({ ...on, heartbeat: { ...on.heartbeat, awaiting_gate: ['sh000852'] } })
      expect(screen.getByText('forecast.eval.gate.title')).toBeTruthy()
    })

    it('reminds about a scheduled benchmark with no check, preselecting it', async () => {
      show({ ...on, bench_next: 'sh000906', bench_next_from: '2026-11', dry_gate: { sh000852: { at: now, by: 'Kazuha', note: 'ok' } } })
      expect(screen.getByText('forecast.eval.gate.nextUnchecked')).toBeTruthy()
      fireEvent.change(screen.getByLabelText('forecast.eval.gate.note'), { target: { value: 'checked' } })
      fireEvent.click(screen.getByRole('button', { name: 'forecast.eval.gate.save' }))
      await waitFor(() =>
        expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', { dry_gate: { code: 'sh000906', note: 'checked' } }),
      )
    })

    it('follows a reload that schedules a new benchmark instead of keeping the mount-time selection', async () => {
      const checked = { sh000852: { at: now, by: 'K', note: 'ok' } }
      const view = render(
        <App><MemoryRouter><ForecastEvaluatorStatus settings={{ ...base, ...on, dry_gate: checked }} onChanged={vi.fn()} /></MemoryRouter></App>,
      )
      // The settings modal schedules CSI 800 and the page reloads quietly, keeping this card mounted.
      view.rerender(
        <App><MemoryRouter><ForecastEvaluatorStatus settings={{ ...base, ...on, dry_gate: checked, bench_next: 'sh000906', bench_next_from: '2026-11' }} onChanged={vi.fn()} /></MemoryRouter></App>,
      )
      fireEvent.change(screen.getByLabelText('forecast.eval.gate.note'), { target: { value: 'checked' } })
      fireEvent.click(screen.getByRole('button', { name: 'forecast.eval.gate.save' }))
      await waitFor(() =>
        expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', { dry_gate: { code: 'sh000906', note: 'checked' } }),
      )
    })

    it('warns once, by display name, about a benchmark the evaluator is waiting on', () => {
      show({
        ...on,
        readiness: notDry.map((r) => (r.key === 'dry_gate' ? { ...r, ok: false, reason: 'gate_missing' as const, codes: ['sh000852'] } : r)),
        heartbeat: { ...on.heartbeat, awaiting_gate: ['sh000852'] },
      })
      expect(screen.queryByText(/reason\.gate_missing/)).toBeNull()
      expect(screen.getByText(/forecast\.eval\.awaiting_gate/)).toBeTruthy()
    })

    it('stays quiet about a scheduled benchmark that is already checked', () => {
      show({
        ...on,
        bench_next: 'sh000906',
        bench_next_from: '2026-11',
        dry_gate: { sh000852: { at: now, by: 'K', note: 'ok' }, sh000906: { at: now, by: 'K', note: 'ok' } },
      })
      expect(screen.queryByText('forecast.eval.gate.nextUnchecked')).toBeNull()
      expect(screen.queryByText('forecast.eval.gate.title')).toBeNull()
    })

    it('shows what is needed to switch on, and no dry tools, when off', () => {
      show({ mode: '0', readiness: notDry })
      expect(screen.getByText('forecast.eval.readiness.title')).toBeTruthy()
      expect(screen.queryByText('forecast.eval.sample.title')).toBeNull()
      expect(screen.queryByText('forecast.eval.gate.title')).toBeNull()
    })

    it('keeps the checklist, sample and gate form in dry', () => {
      show()
      expect(screen.getByText('forecast.eval.readiness.title')).toBeTruthy()
      expect(screen.getByText('forecast.eval.sample.title')).toBeTruthy()
      expect(screen.getByText('forecast.eval.gate.title')).toBeTruthy()
    })
  })

  it('disables Evaluate now while the evaluator is off', () => {
    show({ mode: '0' })
    const button = screen.getByRole('button', { name: 'forecast.eval.run_now' }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
  })
})
