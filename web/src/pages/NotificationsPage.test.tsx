import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import NotificationsPage from './NotificationsPage'
const mock = vi.hoisted(() => ({ list: vi.fn(), snapshot: vi.fn(), readAll: vi.fn(), update: vi.fn(), preferences: vi.fn(), savePreferences: vi.fn() }))
vi.mock('../api/notifications', () => ({ notificationInbox: mock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
describe('Notification inbox', () => {
  beforeEach(() => { vi.clearAllMocks(); mock.list.mockResolvedValue({ items: [{ id: 'one', subject: 'Example', text: '<script>unsafe</script>', occurred_at: '2026-10-21T00:00:00Z', read_at: '' }], unread: 1, next_cursor: '' }); mock.snapshot.mockResolvedValue({ token: 'frozen-snapshot', count: 1 }); mock.readAll.mockResolvedValue({}); Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })
  it('renders content as text and confirms the exact read-all snapshot', async () => {
    render(<App><NotificationsPage /></App>)
    expect(await screen.findByText('<script>unsafe</script>')).toBeTruthy()
    expect(document.querySelector('script')).toBeNull()
    fireEvent.click(screen.getByText('inbox.readAll'))
    await screen.findByText('inbox.snapshotHelp')
    expect(mock.readAll).not.toHaveBeenCalled()
    fireEvent.click(screen.getByText('OK'))
    await waitFor(() => expect(mock.readAll).toHaveBeenCalledWith('frozen-snapshot'))
  })
})
