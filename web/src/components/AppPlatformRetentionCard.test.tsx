import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App } from 'antd'
import AppPlatformRetentionCard from './AppPlatformRetentionCard'
import { findFailure } from '../test/notifications'

const { apiMock } = vi.hoisted(() => ({ apiMock: { get: vi.fn(), put: vi.fn() } }))
vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
// An interpolated string keeps its arguments, so a test can assert the numbers a line states.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, o?: Record<string, unknown>) => (o ? `${key}:${JSON.stringify(o)}` : key) }),
}))

const confirmBtn = async (name: string) =>
  within(await waitFor(() => {
    const p = document.querySelector('.ant-popover:not(.ant-popover-hidden)')
    if (!p) throw new Error('no confirmation open')
    return p as HTMLElement
  })).getByRole('button', { name })

const policy = {
  operation_days: 90,
  recovery_sets: 2,
  bounds: { operation_days_min: 90, operation_days_max: 365, recovery_sets_min: 1, recovery_sets_max: 2 },
  fixed: { review_plan_minutes: 15, review_plan_grace_hours: 24, task_days: 30, task_rows_per_installation: 10000, runner_log_mib: 10, uninstalled_data: 'until_deleted' },
  last_sweep: null as null | { at: string; counts: Record<string, number> },
  recovery_set_pruning: 'blocked',
}

describe('Application platform retention card', () => {
  beforeEach(() => {
    apiMock.get.mockReset().mockResolvedValue(structuredClone(policy))
    apiMock.put.mockReset().mockResolvedValue({ ok: true })
  })

  it('shows the adjustable values and the fixed policy read-only', async () => {
    render(<App><AppPlatformRetentionCard /></App>)
    const days = await screen.findByRole('spinbutton', { name: 'storage.platform.operationDays' })
    expect((days as HTMLInputElement).value).toBe('90')
    expect((screen.getByRole('spinbutton', { name: 'storage.platform.recoverySets' }) as HTMLInputElement).value).toBe('2')
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/app-platform/retention')
    // The fixed values come from the server, not from this file.
    expect(screen.getByText('storage.platform.fixed.reviewPlans')).toBeTruthy()
    expect(screen.getByText(/"minutes":15,"hours":24/)).toBeTruthy()
    expect(screen.getByText(/"days":30,"rows":10000/)).toBeTruthy()
    expect(screen.getByText(/"mib":10/)).toBeTruthy()
    expect(screen.getByText('storage.platform.fixed.uninstalledValue')).toBeTruthy()
    expect(screen.getByText('storage.platform.neverSwept')).toBeTruthy()
    expect(screen.getByText(/storage\.platform\.pruneBlocked/)).toBeTruthy()
  })

  it('reports the last sweep from the audit row', async () => {
    apiMock.get.mockResolvedValue({
      ...structuredClone(policy),
      last_sweep: { at: '2026-11-15T03:00:00Z', counts: { operations: 4, plans: 2, tombstones: 1, tasks_reduced: 7, tasks_capped: 3, recovery_sets_beyond: 5 } },
    })
    render(<App><AppPlatformRetentionCard /></App>)
    const line = await screen.findByText(/storage\.platform\.lastSweep/)
    expect(line.textContent).toContain('"operations":4')
    expect(line.textContent).toContain('"plans":2')
    expect(line.textContent).toContain('"tombstones":1')
    expect(line.textContent).toContain('"tasks":10')
    expect(screen.getByText(/storage\.platform\.pruneBlocked/).textContent).toContain('"count":5')
  })

  it('saves a confirmed change and nothing else', async () => {
    render(<App><AppPlatformRetentionCard /></App>)
    const days = await screen.findByRole('spinbutton', { name: 'storage.platform.operationDays' })
    const save = screen.getByRole('button', { name: 'common.save' }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
    fireEvent.change(days, { target: { value: '120' } })
    fireEvent.blur(days)
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))
    expect(apiMock.put).not.toHaveBeenCalled()
    fireEvent.click(await confirmBtn('storage.platform.apply'))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/app-platform/retention', { operation_days: 120 }))
    await waitFor(() => expect((screen.getByRole('button', { name: /common.save/ }) as HTMLButtonElement).disabled).toBe(true))
  })

  it('does not let a value outside the bounds be saved', async () => {
    render(<App><AppPlatformRetentionCard /></App>)
    const days = await screen.findByRole('spinbutton', { name: 'storage.platform.operationDays' }) as HTMLInputElement
    expect(days.getAttribute('aria-valuemin')).toBe('90')
    expect(days.getAttribute('aria-valuemax')).toBe('365')
    // The control pulls a typed 50 back to the lower bound on blur, so nothing changed and nothing can be saved.
    fireEvent.change(days, { target: { value: '50' } })
    fireEvent.blur(days)
    await waitFor(() => expect(days.value).toBe('90'))
    expect((screen.getByRole('button', { name: 'common.save' }) as HTMLButtonElement).disabled).toBe(true)
    expect(apiMock.put).not.toHaveBeenCalled()
  })

  it('reports a failed save and keeps what the server last said', async () => {
    apiMock.put.mockRejectedValue(new Error('offline'))
    render(<App><AppPlatformRetentionCard /></App>)
    const sets = await screen.findByRole('spinbutton', { name: 'storage.platform.recoverySets' })
    fireEvent.change(sets, { target: { value: '1' } })
    fireEvent.blur(sets)
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))
    fireEvent.click(await confirmBtn('storage.platform.apply'))
    expect(await findFailure('Request failed')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledTimes(1)
  })

  it('does not offer editable defaults when loading failed', async () => {
    apiMock.get.mockRejectedValueOnce(new Error('offline'))
    render(<App><AppPlatformRetentionCard /></App>)
    expect(await screen.findByText('storage.platform.loadFailed')).toBeTruthy()
    expect(screen.queryByRole('spinbutton')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'common.retry' }))
    expect(await screen.findByRole('spinbutton', { name: 'storage.platform.operationDays' })).toBeTruthy()
  })

  it('treats an answer without the policy as a failed load', async () => {
    apiMock.get.mockResolvedValue({})
    render(<App><AppPlatformRetentionCard /></App>)
    expect(await screen.findByText('storage.platform.loadFailed')).toBeTruthy()
    expect(screen.queryByRole('spinbutton')).toBeNull()
  })
})
