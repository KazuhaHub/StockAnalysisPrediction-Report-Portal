import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import NotificationBell, { INBOX_CHANGED } from './NotificationBell'
const mock = vi.hoisted(() => ({ unread: vi.fn() }))
vi.mock('../api/notifications', () => ({ notificationInbox: mock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
describe('NotificationBell', () => {
  beforeEach(() => { mock.unread.mockReset(); mock.unread.mockResolvedValue({ unread: 3 }); Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })
  it('refreshes counts only while visible and removes listeners on unmount', async () => {
    const view = render(<MemoryRouter><NotificationBell user="alice" /></MemoryRouter>)
    await waitFor(() => expect(mock.unread).toHaveBeenCalledTimes(1))
    expect((screen.getByRole('link') as HTMLAnchorElement).getAttribute('href')).toBe('/notifications')
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    act(() => { window.dispatchEvent(new Event(INBOX_CHANGED)) })
    expect(mock.unread).toHaveBeenCalledTimes(1)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    act(() => { document.dispatchEvent(new Event('visibilitychange')) })
    await waitFor(() => expect(mock.unread).toHaveBeenCalledTimes(2))
    view.unmount()
    act(() => { window.dispatchEvent(new Event(INBOX_CHANGED)); document.dispatchEvent(new Event('visibilitychange')) })
    expect(mock.unread).toHaveBeenCalledTimes(2)
  })
})
