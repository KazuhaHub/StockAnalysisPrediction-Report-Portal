import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App, Modal } from 'antd'
import AppMachineReadPanel from './AppMachineReadPanel'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
const translate = vi.hoisted(() => (key: string, options?: Record<string, unknown>) => options ? `${key}:${JSON.stringify(options)}` : key)
vi.mock('../../api/client', async original => ({
  ...await original<typeof import('../../api/client')>(),
  api: apiMock,
  errText: (failure: unknown) => failure instanceof Error ? failure.message : String(failure),
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: translate, i18n: { language: 'en-US' } }) }))

const installation = '11111111-1111-4111-8111-111111111111'
const root = '/api/admin/app-platform'
const grantRoute = `${root}/installations/${installation}/machine-read`
const tokenRoute = `${root}/installations/${installation}/machine-tokens`
const digest = 'd'.repeat(64)
const preview = {
  policy_revision: '3', granted: false, ready: true,
  declared: { routes: [
    { id: 'bars', path: '/v1/machine/bars', query: ['from'], response_bytes: 4096, timeout_ms: 2000 },
    { id: 'margin', path: '/v1/machine/margin/latest', response_bytes: 1000, timeout_ms: 2000 },
  ] },
  policy: { routes: ['bars', 'margin'], installation_per_second: 5, installation_concurrency: 4 },
  openapi_path: `/machine/apps/${installation}/openapi.json`,
  disclosure: 'Holders of a machine token for this app can read these responses without signing in, until the token expires or is revoked.',
  disclosure_sha256: digest,
  runner_support: 'supported',
}
const token = {
  handle: 'abcdefghijkl', label: 'Dify production', routes: ['bars'], perMinute: 60, sourceCidrs: [], state: 'active', createdBy: 'admin',
  createdAt: '2026-11-20T10:00:00Z', expiresAt: '2027-02-18T10:00:00Z', lastUsedAt: null, revokedAt: null,
}
const secret = `pmr_abcdefghijkl_${'S'.repeat(43)}`
const changed = vi.fn()

function mount(props: Partial<{ granted: boolean; ready: boolean }> = {}) {
  return render(<App><AppMachineReadPanel root={root} installation={installation} granted={props.granted ?? false} ready={props.ready ?? true} onChanged={changed} /></App>)
}

beforeEach(() => {
  apiMock.get.mockReset(); apiMock.post.mockReset(); changed.mockReset()
  apiMock.get.mockImplementation(async (url: string) => url === tokenRoute ? { tokens: [token], usage: [{ at: '2026-11-20T11:00:00Z', action: 'app_platform.machine_token_source', detail: { handle: 'abcdefghijkl', address: '203.0.113.5' } }], now: '2026-11-20T12:00:00Z' } : preview)
})
// Modal.confirm lives outside the React tree, so it has to be torn down by hand.
afterEach(() => { Modal.destroyAll(); cleanup() })

describe('machine read panel (doc 183 section 6.4)', () => {
  it('shows the disclosure and every route before anything is sent, then echoes the preview digest', async () => {
    apiMock.post.mockResolvedValue({ ok: true, granted: true })
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.machine.enable' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('appPlatform.capability.machine.read')).toBeTruthy()
    expect(within(dialog).getByText(/\/v1\/machine\/bars/)).toBeTruthy()
    expect(apiMock.post).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'appPlatform.settings.machine.confirm' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(grantRoute, { enabled: true, policyRevision: '3', routes: ['bars', 'margin'], installationPerSecond: 5, installationConcurrency: 4, disclosureSha256: digest }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
  })

  it('narrows the routes, and cannot enable with none or against a runner without support', async () => {
    apiMock.post.mockResolvedValue({ ok: true, granted: true })
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.machine.enable' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('checkbox', { name: /bars/ }))
    await user.click(within(dialog).getByRole('button', { name: 'appPlatform.settings.machine.confirm' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(grantRoute, expect.objectContaining({ routes: ['margin'] })))
    cleanup()
    apiMock.get.mockResolvedValue({ ...preview, runner_support: 'unsupported' })
    mount()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.machine.enable' }))
    const blocked = await screen.findByRole('dialog')
    expect(within(blocked).getByText('appPlatform.settings.machine.runnerUnsupported')).toBeTruthy()
    expect(within(blocked).getByRole('button', { name: 'appPlatform.settings.machine.confirm' }).hasAttribute('disabled')).toBe(true)
  })

  it('withdraws in one click with the current revision and no policy', async () => {
    apiMock.post.mockResolvedValue({ ok: true, granted: false })
    mount({ granted: true })
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.machine.disable' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(grantRoute, { enabled: false, policyRevision: '3', routes: null, installationPerSecond: null, installationConcurrency: null, disclosureSha256: null }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
  })

  it('lists the tokens and recent use without any secret, and keeps withdrawal available while not ready', async () => {
    mount({ granted: true, ready: false })
    expect(await screen.findByText('Dify production')).toBeTruthy()
    expect(screen.getByText('abcdefghijkl')).toBeTruthy()
    expect(screen.getByText(/203\.0\.113\.5/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'appPlatform.settings.machine.disable' }).hasAttribute('disabled')).toBe(false)
    expect(screen.getByRole('button', { name: 'appPlatform.settings.machine.create' }).hasAttribute('disabled')).toBe(true)
    expect(screen.getByText('appPlatform.settings.machine.notReady')).toBeTruthy()
    expect(document.body.textContent).not.toContain('pmr_')
  })

  it('creates a token, shows its secret and the OpenAPI address once, and forgets them', async () => {
    apiMock.post.mockResolvedValue({ token: secret, tokenInfo: token, openapiPath: preview.openapi_path })
    mount({ granted: true })
    const user = userEvent.setup()
    await screen.findByText('Dify production')
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.machine.create' }))
    const dialog = await screen.findByRole('dialog')
    const create = within(dialog).getByRole('button', { name: 'appPlatform.settings.machine.createConfirm' })
    expect(create.hasAttribute('disabled')).toBe(true)
    await user.type(within(dialog).getByPlaceholderText('appPlatform.settings.machine.labelPlaceholder'), 'Dify production')
    await user.click(within(dialog).getByRole('checkbox', { name: /margin/ }))
    await user.type(within(dialog).getByPlaceholderText('203.0.113.0/24'), '203.0.113.0/24')
    await user.click(create)
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(tokenRoute, { label: 'Dify production', routes: ['bars'], perMinute: 60, sourceCidrs: ['203.0.113.0/24'], expiresInDays: 90 }))
    const shown = await screen.findByText(secret)
    expect(shown).toBeTruthy()
    expect(screen.getByText(`${window.location.origin}${preview.openapi_path}`)).toBeTruthy()
    expect(screen.getByText('appPlatform.settings.machine.shownOnce')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.machine.done' }))
    await waitFor(() => expect(screen.queryByText(secret)).toBeNull())
    expect(document.body.textContent).not.toContain(secret)
  })

  it('rotates with a grace period and shows the successor once', async () => {
    apiMock.post.mockResolvedValue({ token: secret, tokenInfo: { ...token, handle: 'mnopqrstuvwx' }, openapiPath: preview.openapi_path })
    mount({ granted: true })
    const user = userEvent.setup()
    await screen.findByText('Dify production')
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.machine.rotate' }))
    const dialog = await screen.findByRole('dialog')
    const buttons = within(dialog).getAllByRole('button', { name: 'appPlatform.settings.machine.rotate' })
    await user.click(buttons[buttons.length - 1])
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${tokenRoute}/abcdefghijkl/rotate`, { graceDays: 1, expiresInDays: 90 }))
    expect(await screen.findByText(secret)).toBeTruthy()
  })

  it('shows a refusal inside the dialog and sends nothing else', async () => {
    apiMock.post.mockRejectedValue(new Error('plan changed'))
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.machine.enable' }))
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'appPlatform.settings.machine.confirm' }))
    expect(await screen.findByText('plan changed')).toBeTruthy()
    expect(changed).not.toHaveBeenCalled()
  })

  // Last: Modal.confirm renders outside the React tree and lingers for the rest of the file.
  it('revokes only after a confirmation', async () => {
    apiMock.post.mockResolvedValue({ ok: true })
    mount({ granted: true })
    const user = userEvent.setup()
    await screen.findByText('Dify production')
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.machine.revoke' }))
    expect(apiMock.post).not.toHaveBeenCalled()
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'appPlatform.settings.machine.revoke' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${tokenRoute}/abcdefghijkl/revoke`, {}))
  })
})
