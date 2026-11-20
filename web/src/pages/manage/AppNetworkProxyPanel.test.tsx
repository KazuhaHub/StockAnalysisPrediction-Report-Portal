import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AppNetworkProxyPanel from './AppNetworkProxyPanel'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
const translate = vi.hoisted(() => (key: string, options?: Record<string, unknown>) => options ? `${key}:${JSON.stringify(options)}` : key)
vi.mock('../../api/client', async original => ({
  ...await original<typeof import('../../api/client')>(),
  api: apiMock,
  errText: (failure: unknown) => failure instanceof Error ? failure.message : String(failure),
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: translate }) }))

const installation = '11111111-1111-4111-8111-111111111111'
const root = '/api/admin/app-platform'
const route = `${root}/installations/${installation}/network-proxy`
const GIB = 1024 * 1024 * 1024
const digest = 'a'.repeat(64)
const closes = { idle: 1, lifetime: 0, session: 0, budget: 2, policy: 0, sni_mismatch: 0 }
const grantedState = {
  policy_revision: '7', granted: true, ready: true,
  declared_hosts: ['*.eastmoney.com'],
  policy: { hosts: ['*.eastmoney.com', 'qt.gtimg.cn'], daily_bytes: 2 * GIB },
  max_daily_bytes: 2 * GIB,
  previous_policy_sha256: digest,
  today: { day: '2026-11-19', bytes: 1234, budget: 2 * GIB, reported: true },
  rollups: [{ hour: '2026-11-19T10:00:00Z', host: 'qt.gtimg.cn', tunnels: 12, bytes_in: 100, bytes_out: 50, closes }],
}
const freshState = { ...grantedState, granted: false, policy: null, previous_policy_sha256: '', today: { day: '2026-11-19', bytes: 0, budget: 2 * GIB, reported: false }, rollups: [] }
const plan = {
  plan_id: 'plan-1', plan_sha256: 'b'.repeat(64), expires_ms: 1, policy_revision: '7',
  diff: {
    added: [{ entry: '*.eastmoney.com', wildcard: true }],
    removed: [{ entry: 'x.example.com', wildcard: false }],
    daily_bytes: { old: 2 * GIB, new: GIB },
  },
}
const changed = vi.fn()

function mount(ready = true) {
  return render(<AppNetworkProxyPanel root={root} installation={installation} granted ready={ready} onChanged={changed} />)
}

beforeEach(() => {
  apiMock.get.mockReset(); apiMock.post.mockReset(); changed.mockReset()
  apiMock.get.mockResolvedValue(grantedState)
})
afterEach(cleanup)

describe('network proxy panel (doc 184 sections 5.2 and 7)', () => {
  it('lists the reviewed hosts and marks wildcard entries', async () => {
    mount()
    expect(await screen.findByText('appPlatform.networkProxy.reviewedHosts')).toBeTruthy()
    expect(screen.getAllByText('*.eastmoney.com').length).toBeGreaterThan(0)
    expect(screen.getAllByText('appPlatform.networkProxy.wildcard')).toHaveLength(1)
  })

  it('offers the declared hosts as pre-filled suggestions when nothing is granted', async () => {
    apiMock.get.mockResolvedValue(freshState)
    mount()
    expect(await screen.findByText('appPlatform.networkProxy.suggestions')).toBeTruthy()
    expect((screen.getByRole('textbox', { name: 'appPlatform.networkProxy.hostsLabel' }) as HTMLTextAreaElement).value).toBe('*.eastmoney.com')
  })

  it('plans, shows the review dialog, and consumes it with the plan digest', async () => {
    apiMock.post.mockResolvedValueOnce(plan).mockResolvedValueOnce({ ok: true, policy_revision: '8' })
    mount()
    const user = userEvent.setup()
    await screen.findByText('appPlatform.networkProxy.reviewedHosts')
    await user.click(screen.getByRole('button', { name: 'appPlatform.networkProxy.review' }))
    const dialog = await screen.findByRole('dialog')
    expect(apiMock.post).toHaveBeenCalledWith(`${route}/plan`, { policyRevision: '7', previousPolicySha256: digest, hosts: ['*.eastmoney.com', 'qt.gtimg.cn'], dailyBytes: 2 * GIB })
    expect(within(dialog).getByText(/\+ \*\.eastmoney\.com/)).toBeTruthy()
    expect(within(dialog).getByText(/- x\.example\.com/)).toBeTruthy()
    expect(within(dialog).getByText('appPlatform.networkProxy.wildcardWarning:{"base":"eastmoney.com"}')).toBeTruthy()
    expect(within(dialog).getByText(/appPlatform\.networkProxy\.budgetChange/)).toBeTruthy()
    await user.click(within(dialog).getByRole('button', { name: 'appPlatform.networkProxy.confirm' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenLastCalledWith(`${root}/network-proxy-reviews/plan-1`, { sha256: 'b'.repeat(64) }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
  })

  it('posts nothing more when the review is cancelled', async () => {
    apiMock.post.mockResolvedValueOnce(plan)
    mount()
    const user = userEvent.setup()
    await screen.findByText('appPlatform.networkProxy.reviewedHosts')
    await user.click(screen.getByRole('button', { name: 'appPlatform.networkProxy.review' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'common.cancel' }))
    expect(apiMock.post).toHaveBeenCalledTimes(1)
    expect(changed).not.toHaveBeenCalled()
  })

  it('withdraws by planning with no hosts', async () => {
    apiMock.post.mockResolvedValueOnce(plan)
    mount()
    const user = userEvent.setup()
    await screen.findByText('appPlatform.networkProxy.reviewedHosts')
    await user.click(screen.getByRole('button', { name: 'appPlatform.networkProxy.withdraw' }))
    await screen.findByRole('dialog')
    expect(apiMock.post).toHaveBeenCalledWith(`${route}/plan`, expect.objectContaining({ hosts: [], policyRevision: '7' }))
  })

  it('refuses a budget above the maximum before anything is sent', async () => {
    mount()
    const user = userEvent.setup()
    await screen.findByText('appPlatform.networkProxy.reviewedHosts')
    const input = screen.getByRole('spinbutton', { name: 'appPlatform.networkProxy.budgetLabel' })
    await user.clear(input)
    await user.type(input, '3')
    expect(await screen.findByText(/appPlatform\.networkProxy\.budgetTooLarge/)).toBeTruthy()
    const button = screen.getByRole('button', { name: 'appPlatform.networkProxy.review' })
    expect(button.hasAttribute('disabled')).toBe(true)
    await user.click(button)
    expect(apiMock.post).not.toHaveBeenCalled()
  })

  it('shows today against the budget and the rollup rows', async () => {
    mount()
    expect(await screen.findByText('1.2 KiB / 2 GiB')).toBeTruthy()
    expect(screen.getByText('2026-11-19 10:00 UTC')).toBeTruthy()
    expect(screen.getByText('12')).toBeTruthy()
    expect(screen.getByText('100 B')).toBeTruthy()
    expect(screen.getByText('50 B')).toBeTruthy()
    expect(screen.getByText('appPlatform.networkProxy.close.idle 1, appPlatform.networkProxy.close.budget 2')).toBeTruthy()
  })

  it('shows no data yet, not zero, before the runner has reported', async () => {
    apiMock.get.mockResolvedValue(freshState)
    mount()
    expect(await screen.findByText('appPlatform.networkProxy.noData')).toBeTruthy()
    expect(screen.queryByRole('progressbar')).toBeNull()
  })

  it('disables the actions while the installation is not ready', async () => {
    mount(false)
    await screen.findByText('appPlatform.networkProxy.reviewedHosts')
    expect(screen.getByRole('button', { name: 'appPlatform.networkProxy.review' }).hasAttribute('disabled')).toBe(true)
    expect(screen.getByRole('button', { name: 'appPlatform.networkProxy.withdraw' }).hasAttribute('disabled')).toBe(true)
    expect(screen.getByText('appPlatform.networkProxy.notReady')).toBeTruthy()
  })
})
