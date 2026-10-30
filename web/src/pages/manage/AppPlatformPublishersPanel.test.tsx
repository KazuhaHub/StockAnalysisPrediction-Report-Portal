import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render as renderPage, screen, waitFor, within } from '@testing-library/react'
import { ConfigProvider } from 'antd'
import userEvent from '@testing-library/user-event'
import AppPlatformPublishersPanel from './AppPlatformPublishersPanel'

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), user: 'admin', t: (key: string) => key }))
vi.mock('../../api/client', () => ({ api: { get: mock.get, post: mock.post }, errText: (e: unknown) => e instanceof Error ? e.message : String(e) }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: mock.user }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mock.t }) }))
const key = { id: 'release-key', algorithm: 'Ed25519', public_key: 'B'.repeat(42) + 'A', not_before_ms: '0', not_after_ms: null, revoked_ms: null, revision: '2' }
const publisher = { id: 'publisher', revision: '7', enabled: true, namespaces: [{ kind: 'prefix', value: 'notes-' }], keys: [key] }
const root = '/api/admin/app-platform'
const render = (page: React.ReactNode) => renderPage(page, { wrapper: ({ children }) => <ConfigProvider theme={{ token: { motion: false } }}>{children}</ConfigProvider> })
beforeEach(() => {
  mock.user = 'admin'; mock.t = (key: string) => key; mock.get.mockReset(); mock.post.mockReset()
  mock.get.mockResolvedValue({ initialized: true, paused: false, publishers: [publisher] })
  mock.post.mockResolvedValue({ publisher_id: 'publisher', revision: '8' })
})
afterEach(cleanup)

describe('publisher trust review', () => {
  it('requires an explicit reviewed confirmation before disabling a publisher', async () => {
    const user = userEvent.setup()
    render(<AppPlatformPublishersPanel />)
    await user.click(await screen.findByRole('button', { name: 'appTrust.disable' }))
    await screen.findByRole('dialog')
    expect(mock.post).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'appTrust.confirm' })).toHaveProperty('disabled', true)
    await user.click(screen.getByRole('checkbox', { name: 'appTrust.acknowledge' }))
    await user.click(screen.getByRole('button', { name: 'appTrust.confirm' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(`${root}/publishers`, { action: 'policy', publisher_id: 'publisher', expected_revision: '7', enabled: false, namespaces: publisher.namespaces }))
  })

  it('does not initialize a fresh deployment by simply opening the tab', async () => {
    mock.get.mockResolvedValue({ initialized: false, paused: false, publishers: [] })
    const user = userEvent.setup()
    render(<AppPlatformPublishersPanel />)
    await user.click(await screen.findByRole('button', { name: 'appTrust.initialize' }))
    expect(mock.post).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'appTrust.cancel' }))
    expect(mock.post).not.toHaveBeenCalled()
  })

  it('keeps recovery pause visible and provides no trust-write or resume button', async () => {
    mock.get.mockResolvedValue({ initialized: true, paused: true, publishers: [publisher] })
    render(<AppPlatformPublishersPanel />)
    await screen.findByText('appTrust.paused')
    expect(screen.queryByRole('button', { name: 'appTrust.initialize' })).toBeNull()
    expect(screen.getByRole('button', { name: 'appTrust.disable' })).toHaveProperty('disabled', true)
    expect(mock.post).not.toHaveBeenCalled()
  })

  it('initializes only after acknowledgement and reloads the directory', async () => {
    mock.get.mockResolvedValueOnce({ initialized: false, paused: false, publishers: [] })
    const user = userEvent.setup()
    render(<AppPlatformPublishersPanel />)
    await user.click(await screen.findByRole('button', { name: 'appTrust.initialize' }))
    await user.click(screen.getByRole('checkbox', { name: 'appTrust.acknowledge' }))
    await user.click(screen.getByRole('button', { name: 'appTrust.confirm' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(`${root}/control/initialize`, { action: 'initialize' }))
    await screen.findByRole('button', { name: 'appTrust.register' })
    expect(mock.get).toHaveBeenCalledTimes(2)
  })

  it('adds a rotation key without changing existing keys or enabling the publisher', async () => {
    const user = userEvent.setup()
    render(<AppPlatformPublishersPanel />)
    await user.click(await screen.findByRole('button', { name: 'appTrust.addKey' }))
    const dialog = within(screen.getByRole('dialog'))
    await user.type(dialog.getByLabelText('appTrust.keyID'), 'next-key')
    await user.type(dialog.getByLabelText('appTrust.publicKey'), 'A'.repeat(43))
    await user.click(dialog.getByRole('button', { name: 'appTrust.review' }))
    await screen.findByText('66:68:7a:ad:f8:62:bd:77:6c:8f:c1:8b:8e:9f:8e:20:08:97:14:85:6e:e2:33:b3:90:2a:59:1d:0d:5f:29:25')
    expect(mock.post).not.toHaveBeenCalled()
    await user.click(screen.getByRole('checkbox', { name: 'appTrust.acknowledge' }))
    await user.click(screen.getByRole('button', { name: 'appTrust.confirm' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(`${root}/publishers`, {
      action: 'add_key', publisher_id: 'publisher', expected_revision: '7',
      key: { id: 'next-key', public_key: 'A'.repeat(43), not_before_ms: '0', not_after_ms: null },
    }))
  })

  it('uses current publisher and key revisions in a revocation review', async () => {
    const user = userEvent.setup()
    render(<AppPlatformPublishersPanel />)
    await user.click(await screen.findByRole('button', { name: 'appTrust.revoke' }))
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('checkbox', { name: 'appTrust.acknowledge' }))
    await user.click(screen.getByRole('button', { name: 'appTrust.confirm' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(`${root}/publishers`, { action: 'revoke_key', publisher_id: 'publisher', expected_revision: '7', key_id: 'release-key', expected_key_revision: '2' }))
  })

  it('locks further writes after an uncertain result until an explicit reload', async () => {
    mock.post.mockRejectedValue(new Error('connection lost'))
    const user = userEvent.setup()
    render(<AppPlatformPublishersPanel />)
    await user.click(await screen.findByRole('button', { name: 'appTrust.disable' }))
    await user.click(screen.getByRole('checkbox', { name: 'appTrust.acknowledge' }))
    await user.click(screen.getByRole('button', { name: 'appTrust.confirm' }))
    await screen.findByText('appTrust.reloadRequired')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.getByRole('button', { name: 'appTrust.disable' })).toHaveProperty('disabled', true)
    expect(mock.post).toHaveBeenCalledTimes(1)
    mock.get.mockResolvedValue({ initialized: true, paused: false, publishers: [{ ...publisher, revision: '9' }] })
    await user.click(screen.getByRole('button', { name: 'appTrust.refresh' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'appTrust.disable' })).toHaveProperty('disabled', false))
    await user.click(screen.getByRole('button', { name: 'appTrust.disable' }))
    expect(mock.post).toHaveBeenCalledTimes(1)
    await screen.findByRole('button', { name: 'appTrust.confirm' })
    await user.click(screen.getByRole('checkbox', { name: 'appTrust.acknowledge' }))
    await user.click(screen.getByRole('button', { name: 'appTrust.confirm' }))
    await waitFor(() => expect(mock.post).toHaveBeenLastCalledWith(`${root}/publishers`, { action: 'policy', publisher_id: 'publisher', expected_revision: '9', enabled: false, namespaces: publisher.namespaces }))
  })

  it('allows correcting a public key draft before explicit registration', async () => {
    const user = userEvent.setup()
    render(<AppPlatformPublishersPanel />)
    await user.click(await screen.findByRole('button', { name: 'appTrust.register' }))
    const dialog = within(screen.getByRole('dialog'))
    await user.type(dialog.getByLabelText('appTrust.publisher'), 'new-publisher')
    await user.type(dialog.getByRole('textbox', { name: 'appTrust.namespaceValue' }), 'sample-')
    await user.type(dialog.getByLabelText('appTrust.keyID'), 'Key')
    await user.type(dialog.getByLabelText('appTrust.publicKey'), 'A'.repeat(43))
    await user.click(dialog.getByRole('button', { name: 'appTrust.review' }))
    await dialog.findByText('appTrust.invalidKey')
    expect(mock.post).not.toHaveBeenCalled()
    await user.clear(dialog.getByLabelText('appTrust.keyID'))
    await user.type(dialog.getByLabelText('appTrust.keyID'), 'release-key')
    await waitFor(() => expect(dialog.getByRole('button', { name: /appTrust.review/ }).className).not.toContain('ant-btn-loading'))
    await user.click(dialog.getByRole('button', { name: 'appTrust.review' }))
    await screen.findByText('66:68:7a:ad:f8:62:bd:77:6c:8f:c1:8b:8e:9f:8e:20:08:97:14:85:6e:e2:33:b3:90:2a:59:1d:0d:5f:29:25')
    expect(mock.post).not.toHaveBeenCalled()
    await user.click(screen.getByRole('checkbox', { name: 'appTrust.acknowledge' }))
    await user.click(screen.getByRole('button', { name: 'appTrust.confirm' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(`${root}/publishers`, {
      action: 'register', publisher_id: 'new-publisher', enabled: false,
      namespaces: [{ kind: 'prefix', value: 'sample-' }],
      key: { id: 'release-key', public_key: 'A'.repeat(43), not_before_ms: '0', not_after_ms: null },
    }))
  })

  it('does not clear an uncertain save lock when the panel language changes', async () => {
    mock.post.mockRejectedValue(new Error('connection lost'))
    const user = userEvent.setup()
    const page = render(<AppPlatformPublishersPanel />)
    await user.click(await screen.findByRole('button', { name: 'appTrust.disable' }))
    await user.click(screen.getByRole('checkbox', { name: 'appTrust.acknowledge' }))
    await user.click(screen.getByRole('button', { name: 'appTrust.confirm' }))
    await screen.findByText('appTrust.reloadRequired')
    mock.t = (key: string) => `translated:${key}`
    page.rerender(<AppPlatformPublishersPanel />)
    await screen.findByText('translated:appTrust.reloadRequired')
    expect(screen.getByRole('button', { name: 'translated:appTrust.disable' })).toHaveProperty('disabled', true)
    expect(mock.get).toHaveBeenCalledTimes(1)
    expect(mock.post).toHaveBeenCalledTimes(1)
  })

  it('discards a trust read from an earlier account lifecycle', async () => {
    let resolve!: (value: unknown) => void
    mock.get.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const page = render(<AppPlatformPublishersPanel />)
    mock.user = 'other'
    mock.get.mockResolvedValue({ initialized: true, paused: false, publishers: [] })
    page.rerender(<AppPlatformPublishersPanel />)
    await act(async () => resolve({ initialized: true, paused: false, publishers: [publisher] }))
    expect(screen.queryByText('publisher')).toBeNull()
  })

  it('does not apply an earlier account save failure to the current account', async () => {
    let reject!: (reason: Error) => void
    mock.post.mockReturnValueOnce(new Promise((_, fail) => { reject = fail }))
    const user = userEvent.setup()
    const page = render(<AppPlatformPublishersPanel />)
    await user.click(await screen.findByRole('button', { name: 'appTrust.disable' }))
    await user.click(screen.getByRole('checkbox', { name: 'appTrust.acknowledge' }))
    await user.click(screen.getByRole('button', { name: 'appTrust.confirm' }))
    mock.user = 'other'
    page.rerender(<AppPlatformPublishersPanel />)
    await act(async () => reject(new Error('old account failure')))
    await waitFor(() => expect(screen.getByRole('button', { name: 'appTrust.disable' })).toHaveProperty('disabled', false))
    expect(screen.queryByText('old account failure')).toBeNull()
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })
})
