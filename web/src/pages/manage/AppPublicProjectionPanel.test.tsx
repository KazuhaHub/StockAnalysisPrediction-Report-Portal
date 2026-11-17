import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AppPublicProjectionPanel from './AppPublicProjectionPanel'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
const translate = vi.hoisted(() => (key: string, options?: Record<string, unknown>) => options ? `${key}:${JSON.stringify(options)}` : key)
vi.mock('../../api/client', async original => ({
  ...await original<typeof import('../../api/client')>(),
  api: apiMock,
  errText: (failure: unknown) => failure instanceof Error ? failure.message : String(failure),
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: translate }) }))

const installation = '11111111-1111-4111-8111-111111111111'
const route = `/api/admin/app-platform/installations/${installation}/public-projection`
const digest = 'd'.repeat(64)
const preview = {
  policy_revision: '3', granted: false, ready: true,
  declared: { routes: [
    { id: 'history', path: '/v1/public/history', query: ['from'], cache_seconds: 10, response_bytes: 4096 },
    { id: 'status', path: '/v1/public/status', cache_seconds: 10, response_bytes: 1000 },
  ], document: 'public/index.html' },
  policy: { routes: ['history', 'status'], document: true, client_per_minute: 60, backend_per_second: 5 },
  url: `/public/apps/${installation}/`,
  disclosure: 'Anyone who knows this address can read these responses without signing in.',
  disclosure_sha256: digest,
}
const changed = vi.fn()

function mount(props: Partial<{ granted: boolean; ready: boolean }> = {}) {
  return render(<AppPublicProjectionPanel root="/api/admin/app-platform" installation={installation} granted={props.granted ?? false} ready={props.ready ?? true} onChanged={changed} />)
}

beforeEach(() => {
  apiMock.get.mockReset(); apiMock.post.mockReset(); changed.mockReset()
  apiMock.get.mockResolvedValue(preview)
})
afterEach(cleanup)

describe('public projection panel (doc 179 section 4)', () => {
  it('shows the disclosure, the address and every route before anything is sent, then echoes the preview digest', async () => {
    apiMock.post.mockResolvedValue({ ok: true, granted: true })
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.public.enable' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('appPlatform.capability.public.projection')).toBeTruthy()
    expect(within(dialog).getByText(`${window.location.origin}/public/apps/${installation}/`)).toBeTruthy()
    expect(within(dialog).getByText(/\/v1\/public\/history/)).toBeTruthy()
    expect(within(dialog).getByText(/\/v1\/public\/status/)).toBeTruthy()
    expect(apiMock.post).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'appPlatform.settings.public.confirm' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(route, { enabled: true, policyRevision: '3', routes: ['history', 'status'], document: true, clientPerMinute: 60, backendPerSecond: 5, disclosureSha256: digest }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
  })

  it('narrows the routes and the document, and keeps the descriptor order', async () => {
    apiMock.post.mockResolvedValue({ ok: true, granted: true })
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.public.enable' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('checkbox', { name: /history/ }))
    await user.click(within(dialog).getByRole('checkbox', { name: /appPlatform\.settings\.public\.document/ }))
    await user.click(within(dialog).getByRole('button', { name: 'appPlatform.settings.public.confirm' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(route, expect.objectContaining({ routes: ['status'], document: false })))
  })

  it('cannot publish with no route selected', async () => {
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.public.enable' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('checkbox', { name: /history/ }))
    await user.click(within(dialog).getByRole('checkbox', { name: /status/ }))
    expect(within(dialog).getByRole('button', { name: 'appPlatform.settings.public.confirm' }).hasAttribute('disabled')).toBe(true)
  })

  it('withdraws in one click with the current revision and no policy', async () => {
    apiMock.post.mockResolvedValue({ ok: true, granted: false })
    mount({ granted: true })
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.public.withdraw' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(route, { enabled: false, policyRevision: '3', routes: null, document: null, clientPerMinute: null, backendPerSecond: null, disclosureSha256: null }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
  })

  it('shows a refusal inside the dialog and sends nothing else', async () => {
    apiMock.post.mockRejectedValue(new Error('plan changed'))
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.public.enable' }))
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'appPlatform.settings.public.confirm' }))
    expect(await screen.findByText('plan changed')).toBeTruthy()
    expect(changed).not.toHaveBeenCalled()
  })

  it('disables both controls with an explanation while the installation is not ready', () => {
    mount({ granted: true, ready: false })
    for (const name of ['appPlatform.settings.public.edit', 'appPlatform.settings.public.withdraw']) {
      expect(screen.getByRole('button', { name }).hasAttribute('disabled')).toBe(true)
    }
    expect(screen.getByText('appPlatform.settings.public.notReady')).toBeTruthy()
  })
})
