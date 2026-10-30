import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import RecurringConsole from './RecurringConsole'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: true }) }))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })
const task = { id: 7, name: 'Close review', target_id: 1, target_name: 'Daily Review', concurrency: 1, priority: '', max_retries: 2, freq: 'daily', at_time: '09:30', weekday: 1, monthday: 1, enabled: true, created_by: 'alice', created_at: '', last_fired: '', row_count: 3, next_run: '2026-07-14 09:30:00' }

describe('RecurringConsole while the connection is down', () => {
  beforeEach(() => { apiMock.get.mockReset(); apiMock.post.mockReset().mockResolvedValue({ ok: true }) })

  it('keeps the list and ends its spinner when the refresh behind an action cannot connect', async () => {
    let recurring = 0
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/batch/targets')) return Promise.resolve({ targets: [] })
      recurring += 1
      return recurring === 1 ? Promise.resolve({ tasks: [task] }) : Promise.reject(lost())
    })
    const { container } = render(<App><RecurringConsole /></App>)
    await screen.findByText('Close review')
    await userEvent.setup().click(screen.getByRole('switch'))
    await waitFor(() => expect(recurring).toBe(2))
    await act(async () => { await Promise.resolve() })
    expect(screen.getByText('Close review')).toBeTruthy()
    expect(container.querySelector('.ant-spin-spinning')).toBeNull()
  })

  it('stays loading on a first load that cannot connect, then loads on reconnect', async () => {
    let down = true
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/batch/targets')) return Promise.resolve({ targets: [] })
      return down ? Promise.reject(lost()) : Promise.resolve({ tasks: [task] })
    })
    render(<App><RecurringConsole /></App>)
    await act(async () => { await Promise.resolve() })
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText('Close review')).toBeNull()
    down = false
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    expect(await screen.findByText('Close review')).toBeTruthy()
  })
})
