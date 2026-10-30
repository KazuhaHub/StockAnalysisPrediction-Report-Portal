import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import AccountSessionsPanel from './AccountSessionsPanel'

const { apiMock } = vi.hoisted(() => ({ apiMock: { get: vi.fn(), post: vi.fn(), del: vi.fn() } }))
vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { resolvedLanguage: 'en-US' } }) }))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })
const sessions = [
  { id: 'current', created_at: 1791100000, last_seen: 1791100000, expires_at: 1791186400, ip: '198.51.100.1', current: true, client: { browser: 'Chrome' } },
  { id: 'other', created_at: 1791100000, last_seen: 1791100000, expires_at: 1791186400, ip: '198.51.100.2', current: false, client: { browser: 'Firefox' } },
]
const mount = () => render(<App><AccountSessionsPanel /></App>)

describe('AccountSessionsPanel while the connection is down', () => {
  beforeEach(() => {
    apiMock.get.mockReset().mockResolvedValue({ items: sessions, timezone: 'UTC' })
    apiMock.post.mockReset()
    apiMock.del.mockReset()
  })

  it('renders no load-failure alert, then loads once the connection is restored', async () => {
    apiMock.get.mockRejectedValueOnce(lost())
    mount()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(1))
    await act(async () => { await Promise.resolve() })
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText('sessions.loadFailed')).toBeNull()
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    expect(await screen.findByText('sessions.current')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledTimes(2)
  })

  it('still reports a load failure that is not the connection', async () => {
    apiMock.get.mockRejectedValueOnce(Object.assign(new Error('boom'), { code: 'internal' }))
    mount()
    expect(await screen.findByText('sessions.loadFailed')).toBeTruthy()
  })

  it('does not reload on reconnect when the load never failed on the connection', async () => {
    mount()
    await screen.findByText('sessions.current')
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    expect(apiMock.get).toHaveBeenCalledTimes(1)
  })

  it('still toasts a connection failure of a user action', async () => {
    apiMock.del.mockRejectedValue(lost())
    mount()
    await screen.findByText('sessions.current')
    fireEvent.click(screen.getByRole('button', { name: 'sessions.revoke' }))
    fireEvent.click(await screen.findByRole('button', { name: 'OK' }))
    expect(await screen.findByText('Request failed')).toBeTruthy()
  })
})
