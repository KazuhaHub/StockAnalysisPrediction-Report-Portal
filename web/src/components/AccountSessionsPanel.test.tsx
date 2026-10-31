import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App } from 'antd'
import AccountSessionsPanel from './AccountSessionsPanel'
import { findFailure } from '../test/notifications'

const { apiMock } = vi.hoisted(() => ({ apiMock: { get: vi.fn(), post: vi.fn(), del: vi.fn() } }))
vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { resolvedLanguage: 'en-US' } }) }))
const sessions = [
  { id: 'current', created_at: 1791100000, last_seen: 1791100000, expires_at: 1791186400, ip: '198.51.100.1', current: true, client: { browser: 'Chrome' } },
  { id: 'other', created_at: 1791100000, last_seen: 1791100000, expires_at: 1791186400, ip: '198.51.100.2', current: false, client: { browser: 'Firefox' } },
]
const mount = () => render(<App><AccountSessionsPanel /></App>)

// The confirmation's action-named button, found inside the open popover (the opener can share its name).
const confirmBtn = async (name: string) =>
  within(await waitFor(() => {
    const p = document.querySelector('.ant-popover:not(.ant-popover-hidden)')
    if (!p) throw new Error('no confirmation open')
    return p as HTMLElement
  })).getByRole('button', { name })

const confirm = async (name: string) => fireEvent.click(await confirmBtn(name))

describe('AccountSessionsPanel', () => {
  beforeEach(() => {
    apiMock.get.mockReset().mockResolvedValue({ items: sessions, timezone: 'UTC' })
    apiMock.post.mockReset().mockResolvedValue({ signed_out: false })
    apiMock.del.mockReset().mockResolvedValue({ signed_out: false })
  })
  it('shows each session IP with its resolved country, region and city', async () => {
    apiMock.get.mockResolvedValue({ items: [
      { ...sessions[0], geo: { country_code: 'US', country: 'United States', region: 'California', city: 'Los Angeles' } },
      sessions[1],
    ], timezone: 'UTC' })
    mount()
    expect(await screen.findByText('🇺🇸 United States · California · Los Angeles')).toBeTruthy()
    expect(screen.getByText('198.51.100.1')).toBeTruthy()
    expect(screen.getByText('198.51.100.2')).toBeTruthy()
    expect(screen.getByText('loginActivity.locationUnknown')).toBeTruthy()
  })
  it('marks the current device and revokes another only after confirmation', async () => {
    mount()
    expect(await screen.findByText('sessions.current')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'sessions.revoke' }))
    expect(apiMock.del).not.toHaveBeenCalled()
    await confirm('sessions.revokeAction')
    await waitFor(() => expect(apiMock.del).toHaveBeenCalledWith('/api/me/sessions/other'))
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(2))
  })
  it('sends the others scope and refreshes while keeping the current page', async () => {
    mount()
    await screen.findByText('sessions.current')
    fireEvent.click(screen.getByRole('button', { name: 'sessions.logoutOthers' }))
    await confirm('sessions.logoutOthers')
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
      await confirm('sessions.logoutAll')
      await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/me/sessions/revoke', { scope: 'all' }))
      expect(assign).toHaveBeenCalledWith('/login')
    } finally { Object.defineProperty(window, 'location', { configurable: true, value: original }) }
  })
  it('keeps the session list and reports a failed revoke', async () => {
    apiMock.del.mockRejectedValue(new Error('offline'))
    mount()
    await screen.findByText('sessions.current')
    fireEvent.click(screen.getByRole('button', { name: 'sessions.revoke' }))
    await confirm('sessions.revokeAction')
    expect(await findFailure('Request failed')).toBeTruthy()
    expect(screen.getByText('198.51.100.2')).toBeTruthy()
  })
})
