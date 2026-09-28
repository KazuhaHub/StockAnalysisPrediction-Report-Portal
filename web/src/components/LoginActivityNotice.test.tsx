import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import LoginActivityNotice from './LoginActivityNotice'

const apiGet = vi.hoisted(() => vi.fn())

vi.mock('../api/client', () => ({ api: { get: apiGet } }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

describe('LoginActivityNotice', () => {
  beforeEach(() => {
    localStorage.clear()
    apiGet.mockReset().mockResolvedValue({
      items: [{ id: 42, at: '2026-09-27T19:00:00Z', ip: '198.51.100.7', method: 'password' }],
      total: 1,
      timezone: 'America/Los_Angeles',
    })
  })

  it('shows the latest activity once and records the event id per account', async () => {
    const first = render(
      <MemoryRouter><App><LoginActivityNotice user="alice" /></App></MemoryRouter>,
    )
    expect(await screen.findByText('loginActivity.recentNotice')).toBeTruthy()
    expect(screen.getByText(/198\.51\.100\.•••/)).toBeTruthy()
    await waitFor(() => expect(localStorage.getItem('rp:login-activity-seen:alice')).toBe('42'))
    first.unmount()

    render(<MemoryRouter><App><LoginActivityNotice user="alice" /></App></MemoryRouter>)
    await waitFor(() => expect(apiGet).toHaveBeenCalled())
    expect(screen.queryByText('loginActivity.recentNotice')).toBeNull()
  })

  it('labels a later event as a new login', async () => {
    localStorage.setItem('rp:login-activity-seen:alice', '41')
    render(<MemoryRouter><App><LoginActivityNotice user="alice" /></App></MemoryRouter>)
    expect(await screen.findByText('loginActivity.newNotice')).toBeTruthy()
  })
})
