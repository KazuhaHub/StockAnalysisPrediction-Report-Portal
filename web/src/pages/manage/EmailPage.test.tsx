import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import EmailPage from './EmailPage'

const server = vi.hoisted(() => ({ post: vi.fn(), fail: false }))
vi.mock('../../api/client', () => ({
  api: {
    get: () => Promise.resolve({ enabled: true, host: 'smtp.example.org', port: 587, user: 'sender', from: 'sender@example.org', security: 'starttls', has_pass: true }),
    post: server.post,
  },
  errText: () => 'Offline',
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
beforeEach(() => { server.fail = false; server.post.mockReset().mockImplementation(() => server.fail ? Promise.reject(new Error('Offline')) : Promise.resolve({})) })
const mount = () => render(<App><EmailPage /></App>)

describe('SMTP drafts', () => {
  it('retains failed edits and keeps tests tied to saved configuration', async () => {
    mount()
    const host = await screen.findByRole('textbox', { name: 'email.host' })
    fireEvent.change(host, { target: { value: 'new.example.org' } })
    fireEvent.blur(host)
    expect(server.post).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('tab', { name: 'email.testSection' }))
    expect((screen.getByRole('button', { name: 'email.test' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('tab', { name: 'email.connection' }))
    server.fail = true
    fireEvent.click(screen.getByRole('button', { name: /common.save/ }))
    await screen.findByText('Offline')
    expect((screen.getByRole('textbox', { name: 'email.host' }) as HTMLInputElement).value).toBe('new.example.org')
    fireEvent.click(screen.getByRole('button', { name: 'manage.discard' }))
    expect((screen.getByRole('textbox', { name: 'email.host' }) as HTMLInputElement).value).toBe('smtp.example.org')
  })

  it('saves connection fields together and keeps a blank password write-only', async () => {
    mount()
    fireEvent.change(await screen.findByRole('textbox', { name: 'email.host' }), { target: { value: 'new.example.org' } })
    fireEvent.click(screen.getByRole('button', { name: /common.save/ }))
    await waitFor(() => expect(server.post).toHaveBeenCalledWith('/api/admin/email', expect.objectContaining({ host: 'new.example.org', user: 'sender', pass: '', enabled: true })))
  })
})
