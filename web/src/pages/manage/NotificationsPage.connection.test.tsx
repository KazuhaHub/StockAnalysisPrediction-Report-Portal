import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import NotificationsPage from './NotificationsPage'

const mock = vi.hoisted(() => ({ overview: vi.fn(), history: vi.fn() }))
vi.mock('../../api/notifications', () => ({ notifications: mock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })
const overview = { channels: [], routes: [], events: [], principals: [], dispatch_paused: false, review_token: 't', counts: { pending: 0, leased: 0, retry_wait: 0, uncertain: 0 }, retention: { enabled: false, history_days: 30, inbox_days: 90 } }

describe('NotificationsPage history tab while the connection is down', () => {
  it('loads a never-loaded History tab once the connection returns', async () => {
    mock.overview.mockReset().mockResolvedValue(overview)
    mock.history.mockReset().mockRejectedValueOnce(lost()).mockResolvedValue({ deliveries: [{ id: 'd1', state: 'sent', event: 'report.ingested', channel: 'slack', attempts: 1, created_at: '', updated_at: '' }], next_cursor: '' })
    render(<App><NotificationsPage /></App>)
    fireEvent.click(await screen.findByRole('tab', { name: 'notify.history' }))
    await waitFor(() => expect(mock.history).toHaveBeenCalledTimes(1))
    await act(async () => { await Promise.resolve() })
    expect(screen.queryByText('common.loadFailed')).toBeNull()
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    await waitFor(() => expect(mock.history).toHaveBeenCalledTimes(2))
  })
})
