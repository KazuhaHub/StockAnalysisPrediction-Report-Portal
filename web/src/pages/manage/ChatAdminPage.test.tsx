import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import ChatAdminPage from './ChatAdminPage'

const server = vi.hoisted(() => ({ poll: null as null | (() => unknown), post: vi.fn(), config: { max_concurrent: 4, stream: false, turn_timeout_minutes: 30, turns: [] } }))
vi.mock('../../api/client', () => ({ api: {
  get: (url: string) => Promise.resolve(url.endsWith('/live') ? server.config : { conversations: [] }), post: server.post,
}, errText: () => 'Offline' }))
vi.mock('../../lib/visiblePoll', () => ({ startVisiblePoll: (poll: () => unknown) => { server.poll = poll; void poll(); return () => {} } }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
beforeEach(() => { server.post.mockReset().mockResolvedValue({}); server.poll = null })
const mount = () => render(<App><ChatAdminPage /></App>)

describe('Assistant settings and monitoring', () => {
  it('keeps monitoring refreshes from replacing edits and discards to the acknowledged settings', async () => {
    mount()
    const limit = await screen.findByRole('spinbutton', { name: 'chatAdmin.limit' })
    fireEvent.change(limit, { target: { value: '7' } })
    await server.poll?.()
    expect((limit as HTMLInputElement).value).toBe('7')
    fireEvent.click(screen.getByRole('button', { name: 'manage.discard' }))
    expect((limit as HTMLInputElement).value).toBe('4')
  })
  it('retains a failed save and sends the complete configuration on retry', async () => {
    server.post.mockRejectedValueOnce(new Error('Offline'))
    mount()
    fireEvent.change(await screen.findByRole('spinbutton', { name: 'chatAdmin.limit' }), { target: { value: '7' } })
    fireEvent.click(screen.getByRole('button', { name: /common.save/ }))
    await screen.findByText('Offline')
    expect((screen.getByRole('spinbutton', { name: 'chatAdmin.limit' }) as HTMLInputElement).value).toBe('7')
    fireEvent.click(screen.getByRole('button', { name: /common.save/ }))
    await waitFor(() => expect(server.post).toHaveBeenCalledTimes(2))
    expect(server.post).toHaveBeenLastCalledWith('/api/admin/chat/config', { max_concurrent: 7, stream: false, turn_timeout_minutes: 30 })
  })
})
