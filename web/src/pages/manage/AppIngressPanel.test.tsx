import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AppIngressPanel from './AppIngressPanel'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
const translate = vi.hoisted(() => (key: string, options?: Record<string, unknown>) => options ? `${key}:${JSON.stringify(options)}` : key)
vi.mock('../../api/client', async original => ({
  ...await original<typeof import('../../api/client')>(),
  api: apiMock,
  errText: (failure: unknown) => failure instanceof Error ? failure.message : String(failure),
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: translate }) }))

const installation = '11111111-1111-4111-8111-111111111111'
const route = `/api/admin/app-platform/installations/${installation}/ingress`
const digest = 'd'.repeat(64)
const preview = {
  policy_revision: '3', granted: false, ready: true,
  routes: [{ id: 'heartbeat', path: '/v1/ingress/heartbeat', body_bytes: 1024, per_endpoint_per_minute: 10 }],
  url_pattern: '/ingress/apps/{installation}/{route}/{endpoint}',
  disclosure: 'x', disclosure_sha256: digest,
}
const changed = vi.fn()

function mount(props: Partial<{ granted: boolean; ready: boolean }> = {}) {
  return render(<AppIngressPanel root="/api/admin/app-platform" installation={installation} granted={props.granted ?? false} ready={props.ready ?? true} onChanged={changed} />)
}

beforeEach(() => {
  apiMock.get.mockReset(); apiMock.post.mockReset(); changed.mockReset()
  apiMock.get.mockResolvedValue(preview)
})
afterEach(cleanup)

describe('ingress panel (doc 180 section 4)', () => {
  it('shows the disclosure, the address pattern and every route before anything is sent, then echoes the digest', async () => {
    apiMock.post.mockResolvedValue({ ok: true, granted: true })
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.ingress.enable' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('appPlatform.capability.ingress.webhook')).toBeTruthy()
    expect(within(dialog).getByText(`${window.location.origin}/ingress/apps/{installation}/{route}/{endpoint}`)).toBeTruthy()
    expect(within(dialog).getByText(/heartbeat/)).toBeTruthy()
    expect(apiMock.post).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'appPlatform.settings.ingress.confirm' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(route, { enabled: true, policyRevision: '3', disclosureSha256: digest }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
  })

  it('withdraws with one click from a fresh revision, and carries nothing else', async () => {
    apiMock.post.mockResolvedValue({ ok: true, granted: false })
    apiMock.get.mockResolvedValue({ ...preview, granted: true, policy_revision: '7' })
    mount({ granted: true, ready: false })
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.ingress.withdraw' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(route, { enabled: false, policyRevision: '7', disclosureSha256: null }))
    expect(screen.queryByText('appPlatform.settings.ingress.notReady')).toBeNull()
  })

  it('cannot be enabled while the app is not ready, and says why', () => {
    mount({ ready: false })
    expect((screen.getByRole('button', { name: 'appPlatform.settings.ingress.enable' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText('appPlatform.settings.ingress.notReady')).toBeTruthy()
  })
})
