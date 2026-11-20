import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import AppNetworkTargetsPanel from './AppNetworkTargetsPanel'
import type { NetworkPolicyView } from '../../lib/appNetworkTargetsV2'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../api/client', async original => ({
  ...await original<typeof import('../../api/client')>(),
  api: apiMock,
  errText: (failure: unknown) => failure instanceof Error ? failure.message : String(failure),
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string, options?: Record<string, unknown>) => options && !('defaultValue' in options) ? `${key}:${JSON.stringify(options)}` : key }) }))

const installation = '11111111-1111-4111-8111-111111111111'
const planUrl = `/api/admin/app-platform/installations/${installation}/capability-policy/plan`
const limits = { request_bytes: 1024, response_bytes: 4096, deadline_ms: 5000, concurrency: 2 }
const target = (id: string, origin: string, network = 'public') => ({ id, origin, paths: ['/v1/*'], methods: ['GET'], network, network_target_sha256: 'd'.repeat(64) })
const policy: NetworkPolicyView = { format: 'portal.network.http-policy/2-dev.1', policy_sha256: 'c'.repeat(64), bounds: limits, targets: [target('api', 'https://api.example.com'), target('lan', 'http://10.0.0.5:8080', 'private')] }
const plan = (diff: Record<string, unknown>) => ({
  plan_id: '22222222-2222-4222-8222-222222222222', plan_sha256: 'e'.repeat(64), expires_ms: '1', policy_revision: '5',
  diff: { added: [], removed: [], paused_bindings: [], bounds: { old: limits, new: limits }, narrowing: [], allowlist: { old_sha256: '', new_sha256: '' }, ...diff },
})
async function confirm(dialog: HTMLElement) {
  const button = within(dialog).getByRole('button', { name: 'appPlatform.network.review.confirm' })
  await waitFor(() => expect(button.hasAttribute('disabled')).toBe(false), { timeout: 3000 })
  fireEvent.click(button)
}
const changed = vi.fn()
function mount(ready = true) {
  return render(<AppNetworkTargetsPanel installation={installation} policyRevision="4" policy={policy} ready={ready} onChanged={changed} />)
}
beforeEach(() => { apiMock.get.mockReset(); apiMock.post.mockReset(); changed.mockReset() })
afterEach(cleanup)

describe('network destinations panel', () => {
  it('lists every target with its class, paths, methods and limits', () => {
    mount()
    const panel = screen.getByTestId('network-targets-panel')
    expect(within(panel).getByText('https://api.example.com')).toBeTruthy()
    expect(within(panel).getByText('http://10.0.0.5:8080')).toBeTruthy()
    expect(within(panel).getByText('appPlatform.network.class.private')).toBeTruthy()
    expect(within(panel).getAllByText('/v1/*')).toHaveLength(2)
    expect(within(panel).getAllByText('appPlatform.network.limit.shared')).toHaveLength(2)
  })

  it('removes a target through plan, a dialog built from the server diff, then consume', async () => {
    apiMock.post.mockImplementation(async (url: string) => url === planUrl
      ? plan({ removed: [target('lan', 'http://10.0.0.5:8080', 'private')], paused_bindings: ['lan-token'] })
      : { ok: true, policy_revision: '5', paused_bindings: ['lan-token'] })
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'appPlatform.network.removeTarget:{"id":"lan"}' }))
    const dialog = await screen.findByRole('dialog')
    expect(apiMock.post).toHaveBeenCalledWith(planUrl, { capability: 'network.http', policyRevision: '4', previousPolicySha256: 'c'.repeat(64), add: [], remove: ['lan'] }, undefined)
    expect(within(dialog).getByTestId('network-remove-lan')).toBeTruthy()
    expect(within(dialog).getByTestId('network-paused-bindings').textContent).toContain('lan-token')
    expect(changed).not.toHaveBeenCalled()
    await confirm(dialog)
    await waitFor(() => expect(apiMock.post).toHaveBeenLastCalledWith('/api/admin/app-platform/capability-policy-reviews/22222222-2222-4222-8222-222222222222', { sha256: 'e'.repeat(64) }))
    await waitFor(() => expect(changed).toHaveBeenCalledOnce())
  })

  it('adds a target, marks private and plain HTTP prominently, and cancelling consumes nothing', async () => {
    apiMock.post.mockResolvedValue(plan({ added: [{ ...target('new', 'http://192.168.1.9:80', 'private'), plain_http: true, allowlist: { prefix: '192.168.1.0/24', ports: [80], plain_http: true } }] }))
    mount()
    fireEvent.click(screen.getByRole('button', { name: /appPlatform.network.add/ }))
    fireEvent.change(await screen.findByLabelText('appPlatform.network.id'), { target: { value: 'new' } })
    fireEvent.change(screen.getByLabelText('appPlatform.network.origin'), { target: { value: 'http://192.168.1.9:80' } })
    fireEvent.change(screen.getByLabelText('appPlatform.network.pathsHint'), { target: { value: '/a/*\n/b' } })
    fireEvent.click(screen.getByRole('button', { name: 'appPlatform.network.review.plan' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledOnce())
    expect(apiMock.post.mock.calls[0][1]).toEqual({
      capability: 'network.http', policyRevision: '4', previousPolicySha256: 'c'.repeat(64), remove: [],
      add: [{ id: 'new', origin: 'http://192.168.1.9:80', paths: ['/a/*', '/b'], methods: ['GET'], network: 'public' }],
    })
    const review = await screen.findByTestId('network-review')
    expect(within(review).getByTestId('network-private-tag')).toBeTruthy()
    expect(within(review).getByTestId('network-plain-http-tag')).toBeTruthy()
    expect(review.textContent).toContain('appPlatform.network.review.allowlistValue')
    fireEvent.click(within(screen.getAllByRole('dialog').find(item => item.contains(review))!).getByRole('button', { name: 'common.cancel' }))
    expect(apiMock.post).toHaveBeenCalledOnce()
    expect(changed).not.toHaveBeenCalled()
  })

  it('sends only the shared bounds the administrator changed', async () => {
    apiMock.post.mockResolvedValue(plan({ bounds: { old: limits, new: { ...limits, concurrency: 1 } } }))
    mount()
    fireEvent.click(screen.getByRole('button', { name: /appPlatform.network.editLimits/ }))
    fireEvent.change(await screen.findByLabelText('appPlatform.network.limit.concurrencyName'), { target: { value: '1' } })
    fireEvent.click(screen.getByRole('button', { name: 'appPlatform.network.review.plan' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledOnce())
    expect(apiMock.post.mock.calls[0][1]).toEqual({ capability: 'network.http', policyRevision: '4', previousPolicySha256: 'c'.repeat(64), add: [], remove: [], bounds: { concurrency: 1 } })
    expect((await screen.findByTestId('network-bounds-change')).textContent).toContain('2 → 1')
  })

  it('shows a server refusal instead of a dialog', async () => {
    apiMock.post.mockRejectedValue(new Error('app_platform_private_target_not_allowed'))
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'appPlatform.network.removeTarget:{"id":"api"}' }))
    expect(await screen.findByText('app_platform_private_target_not_allowed')).toBeTruthy()
    expect(screen.queryByTestId('network-review')).toBeNull()
  })

  it('offers no change while the installation is not ready', () => {
    mount(false)
    expect(screen.getByRole('button', { name: /appPlatform.network.add/ }).hasAttribute('disabled')).toBe(true)
    expect(screen.getByRole('button', { name: 'appPlatform.network.removeTarget:{"id":"api"}' }).hasAttribute('disabled')).toBe(true)
  })
})
