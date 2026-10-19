import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
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

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const base: ForecastSettings = {
  enabled: false,
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
    <App>
      <MemoryRouter>
        <ForecastSettingsModal open value={{ ...base, ...value }} onClose={onClose} onSaved={onSaved} />
      </MemoryRouter>
    </App>,
  )
  return { onSaved, onClose }
}

const save = () => fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

describe('ForecastSettingsModal', () => {
  beforeEach(() => {
    apiMock.post.mockReset()
    apiMock.post.mockResolvedValue({ ok: true })
  })

  it('posts mode only when the switch changed', async () => {
    show()
    fireEvent.click(screen.getByRole('switch', { name: 'forecast.settings.enabled' }))
    save()
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', { mode: 1 })
  })

  it('posts bench_schedule only when the index changed', async () => {
    show()
    fireEvent.mouseDown(screen.getByRole('combobox'))
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
})
