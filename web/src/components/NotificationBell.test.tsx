import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, act, fireEvent, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import NotificationBell, { INBOX_CHANGED, PREVIEW_SIZE } from './NotificationBell'
const mock = vi.hoisted(() => ({ unread: vi.fn(), list: vi.fn(), update: vi.fn(), snapshot: vi.fn(), readAll: vi.fn() }))
vi.mock('../api/notifications', () => ({ notificationInbox: mock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { language: 'en-US' } }) }))

const item = (n: number, read = false) => ({ id: `n${n}`, event_id: `e${n}`, event_type: 'batch.done', subject: `Subject ${n}`, text: `Body ${n}`, occurred_at: '2026-11-20T10:00:00Z', read_at: read ? '2026-11-20T11:00:00Z' : '' })

function Where() { return <span data-testid="where">{useLocation().pathname}</span> }
const renderBell = () => render(<MemoryRouter initialEntries={['/reports']}><NotificationBell user="alice" /><Routes><Route path="*" element={<Where />} /></Routes></MemoryRouter>)

describe('NotificationBell', () => {
  beforeEach(() => {
    for (const f of Object.values(mock)) f.mockReset()
    mock.unread.mockResolvedValue({ unread: 3 })
    mock.list.mockResolvedValue({ items: [item(1), item(2, true), ...Array.from({ length: 6 }, (_, i) => item(i + 3))], unread: 7, next_cursor: 'c' })
    mock.update.mockResolvedValue({})
    mock.snapshot.mockResolvedValue({ token: 'tok', count: 7 })
    mock.readAll.mockResolvedValue({})
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  })

  it('refreshes counts only while visible and removes listeners on unmount', async () => {
    const view = renderBell()
    await waitFor(() => expect(mock.unread).toHaveBeenCalledTimes(1))
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

  it('uses the shared header icon size', async () => {
    renderBell()
    await waitFor(() => expect(mock.unread).toHaveBeenCalled())
    expect(screen.getByRole('button', { name: 'inbox.title' }).classList.contains('rp-header-icon')).toBe(true)
  })

  it('opens a preview of the latest notifications in place instead of leaving the page', async () => {
    renderBell()
    const bell = screen.getByRole('button', { name: 'inbox.title' })
    expect(bell.closest('a')).toBeNull()
    fireEvent.click(bell)
    const panel = await screen.findByRole('dialog', { name: 'inbox.title' })
    await within(panel).findByText('Subject 1')
    expect(screen.getByTestId('where').textContent).toBe('/reports')
    expect(bell.getAttribute('aria-expanded')).toBe('true')
    expect(within(panel).getAllByRole('listitem')).toHaveLength(PREVIEW_SIZE)
    expect(within(panel).queryByText(`Subject ${PREVIEW_SIZE + 1}`)).toBeNull()
    expect(within(panel).getByText('Body 1')).toBeTruthy()
  })

  it('marks one notification read from the preview and refreshes the badge', async () => {
    renderBell()
    fireEvent.click(screen.getByRole('button', { name: 'inbox.title' }))
    const panel = await screen.findByRole('dialog', { name: 'inbox.title' })
    await within(panel).findByText('Subject 1')
    const changed = vi.fn()
    window.addEventListener(INBOX_CHANGED, changed)
    // Only unread rows offer the action: item 2 is already read.
    expect(within(panel).getAllByRole('button', { name: 'inbox.read' })).toHaveLength(PREVIEW_SIZE - 1)
    fireEvent.click(within(within(panel).getAllByRole('listitem')[0]).getByRole('button', { name: 'inbox.read' }))
    await waitFor(() => expect(mock.update).toHaveBeenCalledWith('n1', 'read'))
    await waitFor(() => expect(changed).toHaveBeenCalled())
    expect(mock.list).toHaveBeenCalledTimes(2)
    window.removeEventListener(INBOX_CHANGED, changed)
  })

  it('marks everything read through the snapshot, so later arrivals stay unread', async () => {
    renderBell()
    fireEvent.click(screen.getByRole('button', { name: 'inbox.title' }))
    const panel = await screen.findByRole('dialog', { name: 'inbox.title' })
    await within(panel).findByText('Subject 1')
    fireEvent.click(within(panel).getByRole('button', { name: 'inbox.readAll' }))
    await waitFor(() => expect(mock.readAll).toHaveBeenCalledWith('tok'))
    expect(mock.snapshot).toHaveBeenCalledTimes(1)
  })

  it('links to the full page and closes the preview', async () => {
    renderBell()
    const bell = screen.getByRole('button', { name: 'inbox.title' })
    fireEvent.click(bell)
    const panel = await screen.findByRole('dialog', { name: 'inbox.title' })
    fireEvent.click(within(panel).getByRole('link', { name: 'inbox.viewAll' }))
    expect(screen.getByTestId('where').textContent).toBe('/notifications')
    await waitFor(() => expect(bell.getAttribute('aria-expanded')).toBe('false'))
  })

  it('closes and reopens from the bell itself', async () => {
    renderBell()
    const bell = screen.getByRole('button', { name: 'inbox.title' })
    fireEvent.click(bell)
    await waitFor(() => expect(bell.getAttribute('aria-expanded')).toBe('true'))
    fireEvent.click(bell)
    await waitFor(() => expect(bell.getAttribute('aria-expanded')).toBe('false'))
    fireEvent.mouseEnter(bell)
    fireEvent.click(bell)
    await waitFor(() => expect(bell.getAttribute('aria-expanded')).toBe('true'))
  })

  it('offers mark-all while older unread items are beyond the preview', async () => {
    mock.list.mockResolvedValue({ items: [item(1, true), item(2, true)], unread: 3, next_cursor: '' })
    renderBell()
    fireEvent.click(screen.getByRole('button', { name: 'inbox.title' }))
    const panel = await screen.findByRole('dialog', { name: 'inbox.title' })
    expect(await within(panel).findByRole('button', { name: 'inbox.readAll' })).toBeTruthy()
  })

  it('says so when there is nothing to show', async () => {
    mock.list.mockResolvedValue({ items: [], unread: 0, next_cursor: '' })
    renderBell()
    fireEvent.click(screen.getByRole('button', { name: 'inbox.title' }))
    const panel = await screen.findByRole('dialog', { name: 'inbox.title' })
    expect(await within(panel).findByText('inbox.empty')).toBeTruthy()
    expect(within(panel).queryByRole('button', { name: 'inbox.readAll' })).toBeNull()
  })
})
