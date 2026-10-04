import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import AccountSessionsPanel from './AccountSessionsPanel'

const { apiMock } = vi.hoisted(() => ({ apiMock: { get: vi.fn(), post: vi.fn(), del: vi.fn() } }))
vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
const sessions = [
  { id: 'current', created_at: 1791100000, last_seen: 1791100000, expires_at: 1791186400, ip: '198.51.100.1', current: true, client: { browser: 'Chrome' } },
  { id: 'other', created_at: 1791100000, last_seen: 1791100000, expires_at: 1791186400, ip: '198.51.100.2', current: false, client: { browser: 'Firefox' } },
]
const mount = () => render(<App><AccountSessionsPanel /></App>)
const confirm = async () => fireEvent.click(await screen.findByRole('button', { name: 'OK' }))

describe('AccountSessionsPanel', () => {
  beforeEach(() => {
    apiMock.get.mockReset().mockResolvedValue({ items: sessions, timezone: 'UTC' })
    apiMock.post.mockReset().mockResolvedValue({ signed_out: false })
    apiMock.del.mockReset().mockResolvedValue({ signed_out: false })
  })
  it('marks the current device and revokes another only after confirmation', async () => {
    mount()
    expect(await screen.findByText('sessions.current')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'sessions.revoke' }))
    expect(apiMock.del).not.toHaveBeenCalled()
    await confirm()
    await waitFor(() => expect(apiMock.del).toHaveBeenCalledWith('/api/me/sessions/other'))
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(2))
  })
  it('sends the others scope and refreshes while keeping the current page', async () => {
    mount()
    await screen.findByText('sessions.current')
    fireEvent.click(screen.getByRole('button', { name: 'sessions.logoutOthers' }))
    await confirm()
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/me/sessions/revoke', { scope: 'others' }))
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(2))
  })
  it('redirects to sign-in after signing out all sessions', async () => {
    const assign = vi.fn()
    const original = window.location
    Object.defineProperty(window, 'location', { configurable: true, value: { assign } })
    try {
      apiMock.post.mockResolvedValue({ signed_out: true })
      mount()
      await screen.findByText('sessions.current')
      fireEvent.click(screen.getByRole('button', { name: 'sessions.logoutAll' }))
      await confirm()
      await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/me/sessions/revoke', { scope: 'all' }))
      expect(assign).toHaveBeenCalledWith('/login')
    } finally { Object.defineProperty(window, 'location', { configurable: true, value: original }) }
  })
  it('keeps the session list and reports a failed revoke', async () => {
    apiMock.del.mockRejectedValue(new Error('offline'))
    mount()
    await screen.findByText('sessions.current')
    fireEvent.click(screen.getByRole('button', { name: 'sessions.revoke' }))
    await confirm()
    expect(await screen.findByText('Request failed')).toBeTruthy()
    expect(screen.getByText('198.51.100.2')).toBeTruthy()
  })
})
