import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { App } from 'antd'
import NotificationsPage from './NotificationsPage'

const mock = vi.hoisted(() => ({ overview: vi.fn(), saveChannel: vi.fn(), history: vi.fn(), dispatch: vi.fn(), preview: vi.fn(), retention: vi.fn() }))
vi.mock('../../api/notifications', () => ({ notifications: mock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
const overview = () => ({ channels: [{ id: 'slack', label: 'Operations Slack', adapter: 'slack', legacy_ref: '', enabled: true, paused: false, revision: 7, recipients: [], url_host: 'https://hooks.slack.com', has_url: true, has_signing_secret: false, available: true, created_at: '' }], routes: [], events: ['report.ingested'], principals: [], dispatch_paused: true, review_token: 'current-review', counts: { pending: 2, leased: 0, retry_wait: 0, uncertain: 1 }, retention: { enabled: false, history_days: 30, inbox_days: 90 } })
const renderPage = () => render(<App><NotificationsPage /></App>)

describe('NotificationsPage', () => {
  beforeEach(() => { vi.clearAllMocks(); mock.overview.mockResolvedValue(overview()); mock.saveChannel.mockResolvedValue({}); mock.history.mockResolvedValue({ deliveries: [], next_cursor: '' }); mock.dispatch.mockResolvedValue({}) })

  it('does not offer editable defaults after a failed initial load', async () => {
    mock.overview.mockRejectedValue(new Error('offline'))
    renderPage()
    expect(await screen.findByText('common.loadFailed')).toBeTruthy()
    expect(screen.queryByText('notify.addChannel')).toBeNull()
  })

  it('keeps write-only input blank and saves with the loaded revision', async () => {
    renderPage()
    await screen.findByText('Operations Slack')
    fireEvent.click(screen.getByText('common.edit'))
    const name = screen.getByLabelText('notify.label')
    fireEvent.change(name, { target: { value: 'Renamed Slack' } })
    const url = await screen.findByLabelText('notify.url')
    expect((url as HTMLInputElement).value).toBe('')
    fireEvent.click(screen.getByText('OK'))
    await waitFor(() => expect(mock.saveChannel).toHaveBeenCalledWith(expect.objectContaining({ label: 'Renamed Slack', revision: 7, url: '' }), 'slack'))
    expect(mock.saveChannel.mock.calls[0][0]).not.toHaveProperty('recipients')
  })

  it('requires a visible queue review before resuming with its exact token', async () => {
    renderPage()
    fireEvent.click(await screen.findByText('notify.resume'))
    expect(mock.dispatch).not.toHaveBeenCalled()
    expect(screen.getByText('notify.resumeHelp')).toBeTruthy()
    fireEvent.click(screen.getByText('OK'))
    await waitFor(() => expect(mock.dispatch).toHaveBeenCalledWith(false, 'current-review'))
  })
  it('loads retention values and does not enable cleanup by opening its editor', async () => {
    mock.retention.mockResolvedValue({})
    renderPage()
    fireEvent.click(await screen.findByText('notify.retention'))
    const panel = screen.getByRole('tabpanel', { name: 'notify.retention' })
    fireEvent.click(within(panel).getByText('common.edit'))
    const days = await screen.findByLabelText('notify.historyDays')
    expect((days as HTMLInputElement).value).toBe('30')
    expect(mock.retention).not.toHaveBeenCalled()
    fireEvent.click(screen.getByText('OK'))
    await waitFor(() => expect(mock.retention).toHaveBeenCalledWith({ enabled: false, history_days: 30, inbox_days: 90 }))
  })

})
