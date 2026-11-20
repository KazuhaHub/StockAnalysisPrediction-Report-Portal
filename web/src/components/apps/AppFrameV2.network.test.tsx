import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { BridgeHostOptions } from '../../lib/bridge-v2'
import type { NetworkTargetRequest, NetworkTargetsResult } from '../../lib/network-v2'
import AppFrameV2 from './AppFrameV2'
import { resetNetworkTargetsGate } from '../../lib/networkTargetsGate'

const sessions = vi.hoisted(() => [] as { options: BridgeHostOptions; close: ReturnType<typeof vi.fn>; updatePresentation: ReturnType<typeof vi.fn> }[])
const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../lib/bridge-v2', async original => ({ ...await original<typeof import('../../lib/bridge-v2')>(), createBridgeHost: (options: BridgeHostOptions) => { const session = { options, close: vi.fn(), updatePresentation: vi.fn(() => true) }; sessions.push(session); return session } }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: apiMock, errText: (failure: unknown) => failure instanceof Error ? failure.message : String(failure) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string, options?: Record<string, unknown>) => options ? `${key}:${JSON.stringify(options)}` : key }) }))

const installationId = '00000000-0000-0000-0000-000000000001'
const binding = { installationId, generation: '9007199254740993', policyRevision: '1', releaseSha256: 'a'.repeat(64) }
const presentation = { locale: 'zh-CN', timeZone: 'Asia/Shanghai', layout: 'document', theme: { dark: true, colorPrimary: '#1677ff', colorBg: '#141414', colorText: '#ffffff', colorBorder: '#424242', colorBgLayout: '#000000', borderRadius: 8 } } as const
const props = { source: '/app-assets/fixture/index.html', binding, presentation, capabilities: ['network.http'], title: 'Fixture', height: 640 }
const limits = { request_bytes: 1024, response_bytes: 4096, deadline_ms: 5000, concurrency: 2 }
const policySha = 'c'.repeat(64)
const settings = {
  policy_revision: '7',
  networkPolicy: { format: 'portal.network.http-policy/2-dev.1', policy_sha256: policySha, bounds: limits, targets: [] },
}
const server = (id: string, origin: string, network: string) => ({ id, origin, paths: ['/v1/*'], methods: ['GET'], network, network_target_sha256: 'd'.repeat(64) })
const plan = {
  plan_id: '11111111-1111-4111-8111-111111111111', plan_sha256: 'e'.repeat(64), expires_ms: '1', policy_revision: '8',
  diff: {
    added: [{ ...server('server-lan', 'http://10.0.0.5:8080', 'private'), plain_http: true, allowlist: { prefix: '10.0.0.0/24', ports: [8080], plain_http: true } }],
    removed: [], paused_bindings: ['lan-token'],
    bounds: { old: limits, new: { ...limits, concurrency: 1 } }, narrowing: [],
    allowlist: { old_sha256: '', new_sha256: 'f'.repeat(64) },
  },
}
const asked: NetworkTargetRequest = { id: 'app-chosen-name', origin: 'https://app-text.example', paths: ['/app-path'], methods: ['GET'], network: 'public' }

async function confirm(dialog: HTMLElement) {
  const button = within(dialog).getByRole('button', { name: 'appPlatform.network.review.confirm' })
  await waitFor(() => expect(button.hasAttribute('disabled')).toBe(false), { timeout: 3000 })
  fireEvent.click(button)
}

function mount(extra: Partial<React.ComponentProps<typeof AppFrameV2>> = {}) {
  const view = render(<AppFrameV2 {...props} canReviewNetwork {...extra} />)
  fireEvent.load(screen.getByTitle('Fixture'))
  return { view, options: sessions[sessions.length - 1].options }
}
beforeEach(() => {
  resetNetworkTargetsGate()
  apiMock.get.mockReset().mockResolvedValue(settings)
  apiMock.post.mockReset().mockImplementation(async (url: string) => url.includes('/capability-policy/plan') ? plan : { ok: true, policy_revision: '8', paused_bindings: ['lan-token'] })
})
afterEach(() => { cleanup(); sessions.splice(0) })

test('a viewer who is not an administrator gets no network.targets handler at all', () => {
  const { options } = mount({ canReviewNetwork: false })
  expect(options.onNetworkTargetsRequest).toBeUndefined()
  cleanup()
  const withoutGrant = mount({ capabilities: [], canReviewNetwork: true })
  expect(withoutGrant.options.onNetworkTargetsRequest).toBeUndefined()
  expect(apiMock.get).not.toHaveBeenCalled()
  expect(apiMock.post).not.toHaveBeenCalled()
})

test('the dialog shows the server diff and none of the text the app sent; confirm consumes and reports applied', async () => {
  const applied = vi.fn()
  const { options } = mount({ onNetworkPolicyChanged: applied })
  let result!: Promise<NetworkTargetsResult>
  act(() => { result = options.onNetworkTargetsRequest!([asked], new AbortController().signal) })
  const dialog = await screen.findByRole('dialog')
  expect(apiMock.get).toHaveBeenCalledWith(`/api/admin/app-platform/installations/${installationId}/settings`, expect.anything())
  const [planUrl, body] = apiMock.post.mock.calls[0]
  expect(planUrl).toBe(`/api/admin/app-platform/installations/${installationId}/capability-policy/plan`)
  expect(body).toEqual({ capability: 'network.http', policyRevision: '7', previousPolicySha256: policySha, add: [asked], remove: [] })
  expect('bounds' in body).toBe(false)
  expect(within(dialog).getByText('appPlatform.network.review.appChosenId:{"id":"server-lan"}')).toBeTruthy()
  expect(within(dialog).getByText('http://10.0.0.5:8080')).toBeTruthy()
  expect(within(dialog).getByTestId('network-private-tag')).toBeTruthy()
  expect(within(dialog).getByTestId('network-plain-http-tag')).toBeTruthy()
  expect(within(dialog).getByTestId('network-bounds-change').textContent).toContain('2 → 1')
  expect(within(dialog).getByTestId('network-paused-bindings').textContent).toContain('lan-token')
  expect(dialog.textContent).not.toContain('app-chosen-name')
  expect(dialog.textContent).not.toContain('app-text.example')
  expect(apiMock.post).toHaveBeenCalledTimes(1)
  await confirm(dialog)
  await expect(result).resolves.toEqual({ state: 'applied', policyRevision: '8' })
  expect(apiMock.post).toHaveBeenLastCalledWith(`/api/admin/app-platform/capability-policy-reviews/${plan.plan_id}`, { sha256: plan.plan_sha256 })
  // The relaunch hook is the bridge's, called after the answer is posted.
  act(() => options.onNetworkTargetsApplied?.('8'))
  expect(applied).toHaveBeenCalledWith('8')
})

test('cancel declines without consuming, and an aborted request closes the dialog', async () => {
  const { options } = mount()
  let result!: Promise<NetworkTargetsResult>
  act(() => { result = options.onNetworkTargetsRequest!([asked], new AbortController().signal) })
  const dialog = await screen.findByRole('dialog')
  fireEvent.click(within(dialog).getByRole('button', { name: 'common.cancel' }))
  await expect(result).resolves.toEqual({ state: 'declined' })
  expect(apiMock.post).toHaveBeenCalledTimes(1)
  const abort = new AbortController()
  let second!: Promise<NetworkTargetsResult>
  act(() => { second = options.onNetworkTargetsRequest!([asked], abort.signal) })
  await screen.findByRole('dialog')
  act(() => abort.abort())
  await expect(second).resolves.toEqual({ state: 'declined' })
})

test('a refused consume keeps the dialog open with the server reason and never reports applied', async () => {
  apiMock.post.mockImplementation(async (url: string) => {
    if (url.includes('/capability-policy/plan')) return plan
    throw new Error('app_platform_plan_changed')
  })
  const { options } = mount()
  let result!: Promise<NetworkTargetsResult>
  act(() => { result = options.onNetworkTargetsRequest!([asked], new AbortController().signal) })
  const dialog = await screen.findByRole('dialog')
  await confirm(dialog)
  await within(dialog).findByText('app_platform_plan_changed')
  fireEvent.click(within(dialog).getByRole('button', { name: 'common.cancel' }))
  await expect(result).resolves.toEqual({ state: 'declined' })
})

test('confirm is disabled until the dialog has been open for a moment, and re-arms for a new plan', async () => {
  const { options } = mount()
  act(() => { void options.onNetworkTargetsRequest!([asked], new AbortController().signal) })
  const dialog = await screen.findByRole('dialog')
  const button = within(dialog).getByRole('button', { name: 'appPlatform.network.review.confirm' })
  expect(button.hasAttribute('disabled')).toBe(true)
  fireEvent.click(button)
  expect(apiMock.post).toHaveBeenCalledTimes(1)
  await waitFor(() => expect(button.hasAttribute('disabled')).toBe(false), { timeout: 3000 })
})

test('the dialog says the request comes from the app and labels its ids as app-chosen', async () => {
  const { options } = mount()
  act(() => { void options.onNetworkTargetsRequest!([asked], new AbortController().signal) })
  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByTestId('network-from-app').textContent).toContain('"app":"Fixture"')
  expect(dialog.textContent).toContain('appPlatform.network.review.appChosenId')
})

test('a decline survives an iframe reload: the next request is refused without any server call', async () => {
  const first = mount()
  let result!: Promise<NetworkTargetsResult>
  act(() => { result = first.options.onNetworkTargetsRequest!([asked], new AbortController().signal) })
  const dialog = await screen.findByRole('dialog')
  fireEvent.click(within(dialog).getByRole('button', { name: 'common.cancel' }))
  await expect(result).resolves.toEqual({ state: 'declined' })
  const calls = apiMock.get.mock.calls.length + apiMock.post.mock.calls.length
  // The reload: a new bridge session (and here a new frame) with a fresh session-level state.
  fireEvent.load(screen.getByTitle('Fixture'))
  const reloaded = sessions[sessions.length - 1].options
  await expect(reloaded.onNetworkTargetsRequest!([asked], new AbortController().signal)).resolves.toEqual({ state: 'declined' })
  cleanup()
  const remounted = mount().options
  await expect(remounted.onNetworkTargetsRequest!([asked], new AbortController().signal)).resolves.toEqual({ state: 'declined' })
  expect(apiMock.get.mock.calls.length + apiMock.post.mock.calls.length).toBe(calls)
})

test('a request that fails before any dialog is paced too', async () => {
  apiMock.get.mockResolvedValue({ policy_revision: '7' })
  const { options } = mount()
  await expect(options.onNetworkTargetsRequest!([asked], new AbortController().signal)).resolves.toEqual({ state: 'not_permitted' })
  await expect(options.onNetworkTargetsRequest!([asked], new AbortController().signal)).resolves.toEqual({ state: 'declined' })
  expect(apiMock.get).toHaveBeenCalledTimes(1)
})

test('a consume that finishes after its request was aborted does not answer it, and still relaunches', async () => {
  let release!: () => void
  apiMock.post.mockImplementation(async (url: string) => {
    if (url.includes('/capability-policy/plan')) return plan
    await new Promise<void>(resolve => { release = resolve })
    return { ok: true, policy_revision: '8', paused_bindings: [] }
  })
  const changedHook = vi.fn()
  const { options } = mount({ onNetworkPolicyChanged: changedHook })
  const abort = new AbortController()
  let result!: Promise<NetworkTargetsResult>
  act(() => { result = options.onNetworkTargetsRequest!([asked], abort.signal) })
  const dialog = await screen.findByRole('dialog')
  await confirm(dialog)
  await waitFor(() => expect(release).toBeDefined())
  act(() => abort.abort())
  await expect(result).resolves.toEqual({ state: 'declined' })
  await act(async () => { release() })
  await waitFor(() => expect(changedHook).toHaveBeenCalledWith('8'))
})
