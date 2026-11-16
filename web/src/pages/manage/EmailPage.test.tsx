import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import EmailPage from './EmailPage'
import { findFailure } from '../../test/notifications'

const server = vi.hoisted(() => ({ post: vi.fn(), fail: false }))
vi.mock('../../api/client', () => ({
  api: {
    get: () => Promise.resolve({ enabled: true, host: 'smtp.example.org', port: 587, user: 'sender', from: 'sender@example.org', security: 'starttls', has_pass: true }),
    post: server.post,
  },
  // Mirrors the real errText: a server code renders through err.<code>, anything else is the message.
  errText: (e: { code?: string }, t: (key: string) => string) => e?.code ? t(`err.${e.code}`) : 'Offline',
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
    await findFailure('Offline')
    expect((screen.getByRole('textbox', { name: 'email.host' }) as HTMLInputElement).value).toBe('new.example.org')
    fireEvent.click(screen.getByRole('button', { name: 'manage.discard' }))
    expect((screen.getByRole('textbox', { name: 'email.host' }) as HTMLInputElement).value).toBe('smtp.example.org')
  })

  it('sends a test with a blank recipient so the server addresses it to the signed-in admin', async () => {
    mount()
    await screen.findByRole('textbox', { name: 'email.host' })
    fireEvent.click(screen.getByRole('tab', { name: 'email.testSection' }))
    const send = screen.getByRole('button', { name: 'email.test' }) as HTMLButtonElement
    expect((screen.getByRole('textbox', { name: 'email.testTo' }) as HTMLInputElement).value).toBe('')
    expect(send.disabled).toBe(false)
    fireEvent.click(send)
    await waitFor(() => expect(server.post).toHaveBeenCalledWith('/api/admin/email/test', { to: '' }))
  })

  it('explains a blank recipient the server cannot resolve in the reader\'s language', async () => {
    server.post.mockImplementation(() => Promise.reject(Object.assign(new Error('no recipient'), { code: 'no_recipient' })))
    mount()
    await screen.findByRole('textbox', { name: 'email.host' })
    fireEvent.click(screen.getByRole('tab', { name: 'email.testSection' }))
    fireEvent.click(screen.getByRole('button', { name: 'email.test' }))
    await findFailure('err.no_recipient')
  })

  it('saves connection fields together and keeps a blank password write-only', async () => {
    mount()
    fireEvent.change(await screen.findByRole('textbox', { name: 'email.host' }), { target: { value: 'new.example.org' } })
    fireEvent.click(screen.getByRole('button', { name: /common.save/ }))
    await waitFor(() => expect(server.post).toHaveBeenCalledWith('/api/admin/email', expect.objectContaining({ host: 'new.example.org', user: 'sender', pass: '', enabled: true })))
  })
})
