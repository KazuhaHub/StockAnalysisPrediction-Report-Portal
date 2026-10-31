import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App, ConfigProvider } from 'antd'
import { MemoryRouter } from 'react-router'
import ForecastSettingsModal from './ForecastSettingsModal'
import type { ForecastSettings } from '../../api/types'

// The settings modal (P4). The parts that are easy to get wrong and invisible in review:
//   - it posts ONLY what the admin changed, so a switch flip cannot schedule a benchmark;
//   - a pending schedule is shown with its effective cohort and a Cancel action;
//   - Cancel posts bench_cancel, not a reschedule of the active value.

const apiMock = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('../../api/client', () => ({
  api: apiMock,
  errText: (_e: unknown, t: (k: string) => string) => t('err.forecast_bad_bench'),
}))

const pending = vi.hoisted(() => vi.fn())
vi.mock('./AdminDraftBoundary', () => ({ usePendingSettings: (dirty: boolean, saving?: boolean) => pending(dirty, saving) }))
const lastPending = () => pending.mock.calls[pending.mock.calls.length - 1]

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const base: ForecastSettings = {
  enabled: false,
  mode: '0',
  readiness: [],
  dry_gate: {},
  gate_codes: ['sh000852'],
  alerts: { email_to: '', has_slack: false },
  bench: 'sh000852',
  bench_codes: ['sh000300', 'sh000852', 'sh000906'],
  population: { set: false, candidates: [] },
  due_sets: 0,
  pending: 0,
  refused: 0,
  unsettled_baseline: 0,
  unrecorded: [],
}

function show(value: Partial<ForecastSettings> = {}) {
  const onSaved = vi.fn()
  const onClose = vi.fn()
  render(
    <ConfigProvider theme={{ token: { motion: false } }}>
      <App>
        <MemoryRouter>
          <ForecastSettingsModal open value={{ ...base, ...value }} onClose={onClose} onSaved={onSaved} />
        </MemoryRouter>
      </App>
    </ConfigProvider>,
  )
  return { onSaved, onClose }
}

const save = () => fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

describe('ForecastSettingsModal', () => {
  beforeEach(() => {
    apiMock.post.mockReset()
    apiMock.post.mockResolvedValue({ ok: true })
  })

  it('posts bench_schedule only when the index changed', async () => {
    show()
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'forecast.settings.bench' }))
    fireEvent.click(await screen.findByText('forecast.bench.sh000906'))
    save()
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', { bench_schedule: 'sh000906' })
  })

  it('shows a pending schedule with its cohort and cancels it', async () => {
    show({ bench_next: 'sh000906', bench_next_from: '2026-11' })
    expect(screen.getAllByText(/forecast\.settings\.scheduled/).length).toBeGreaterThan(0)
    fireEvent.click(screen.getByRole('button', { name: 'forecast.settings.scheduledCancel' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', { bench_cancel: true }))
  })

  it('posts shadow mode alone without changing evaluation or benchmark settings', async () => {
    show({ fallback: 'off' })
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'forecast.settings.fallback' }))
    fireEvent.click(await screen.findByText('forecast.settings.fallbackShadow'))
    expect(screen.queryByText('forecast.settings.fallbackOn')).toBeNull()
    save()
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', { fallback: 'shadow' }))
  })

  it('does not write unchanged shadow controls', () => {
    const { onClose } = show({ fallback: 'shadow' })
    save()
    expect(apiMock.post).not.toHaveBeenCalled()
    expect(onClose).toHaveBeenCalledOnce()
  })

  describe('unsaved changes', () => {
    beforeEach(() => pending.mockReset())

    it('is not pending after load', () => {
      show()
      expect(lastPending()).toEqual([false, false])
    })

    it('is pending once a field differs from the loaded value, and clean again when put back', async () => {
      show({ fallback: 'off' })
      fireEvent.mouseDown(screen.getByRole('combobox', { name: 'forecast.settings.fallback' }))
      fireEvent.click(await screen.findByText('forecast.settings.fallbackShadow'))
      expect(lastPending()).toEqual([true, false])
      fireEvent.mouseDown(screen.getByRole('combobox', { name: 'forecast.settings.fallback' }))
      fireEvent.click(await screen.findByText('forecast.settings.fallbackOff'))
      expect(lastPending()).toEqual([false, false])
    })

    it('closes without asking when nothing changed', () => {
      const { onClose } = show()
      fireEvent.click(screen.getByRole('button', { name: 'common.cancel' }))
      expect(onClose).toHaveBeenCalledOnce()
      expect(screen.queryAllByText('manage.leaveTitle')).toHaveLength(0)
    })

    it('asks before closing with edits, keeps them on stay and closes on leave', async () => {
      const { onClose } = show({ fallback: 'off' })
      fireEvent.mouseDown(screen.getByRole('combobox', { name: 'forecast.settings.fallback' }))
      fireEvent.click(await screen.findByText('forecast.settings.fallbackShadow'))
      fireEvent.click(screen.getByRole('button', { name: 'common.cancel' }))
      expect((await screen.findAllByText('manage.leaveTitle')).length).toBeGreaterThan(0)
      expect(onClose).not.toHaveBeenCalled()
      fireEvent.click(screen.getByRole('button', { name: 'manage.stay' }))
      await waitFor(() => expect(screen.queryAllByText('manage.leaveTitle')).toHaveLength(0))
      expect(onClose).not.toHaveBeenCalled()
      fireEvent.click(screen.getByRole('button', { name: 'Close' }))
      fireEvent.click(await screen.findByRole('button', { name: 'manage.leave' }))
      await waitFor(() => expect(onClose).toHaveBeenCalledOnce())
    })

    it('is clean again after a successful save refreshes the value', async () => {
      function Harness() {
        const [value, setValue] = useState<ForecastSettings>({ ...base, fallback: 'off' })
        return <ForecastSettingsModal open value={value} onClose={() => {}} onSaved={() => setValue((v) => ({ ...v, fallback: 'shadow' }))} />
      }
      render(<App><MemoryRouter><Harness /></MemoryRouter></App>)
      fireEvent.mouseDown(screen.getByRole('combobox', { name: 'forecast.settings.fallback' }))
      fireEvent.click(await screen.findByText('forecast.settings.fallbackShadow'))
      expect(lastPending()).toEqual([true, false])
      save()
      await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', { fallback: 'shadow' }))
      await waitFor(() => expect(lastPending()).toEqual([false, false]))
    })
  })
})
