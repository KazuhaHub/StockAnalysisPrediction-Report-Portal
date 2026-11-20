import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AppPostgresPolicyPanel from './AppPostgresPolicyPanel'
import { ApiError } from '../../api/client'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
const translate = vi.hoisted(() => (key: string, options?: Record<string, unknown>) => {
  if (key.startsWith('err.')) return `localized ${key}`
  return options ? `${key}:${JSON.stringify(options)}` : key
})
vi.mock('../../api/client', async original => ({
  ...await original<typeof import('../../api/client')>(),
  api: apiMock,
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: translate }) }))

const installation = '11111111-1111-4111-8111-111111111111'
const route = `/api/admin/app-platform/installations/${installation}/postgres-policy`
const digest = 'a'.repeat(64)
const caDigest = 'c'.repeat(64)
const view = (extra: Record<string, unknown> = {}) => ({
  id: 'orders', host: 'db.example.com', port: 5432, database: 'orders', network: 'public',
  label: 'postgres://db.example.com:5432/orders', network_target_sha256: 'b'.repeat(64), ...extra,
})
const preview = {
  policy_revision: '7', declared: true, granted: true, ready: true, previous_policy_sha256: digest,
  targets: [view({ ca_sha256: caDigest })], deadline_ms: 10000, concurrency: 2,
  private_networks_sha256: 'd'.repeat(64), disclosure: 'Connect and SELECT 1 only; use a role with no table privileges.',
}
const planFor = (targets: Record<string, unknown>[], granted = true) => ({
  plan_id: 'plan-1', plan_sha256: 'e'.repeat(64), expires_ms: '1', policy_revision: '7',
  diff: {
    granted, previous_granted: true, targets, deadline_ms: 10000, concurrency: 2, previous_policy_sha256: digest,
    policy_sha256: 'f'.repeat(64), private_networks_sha256: 'd'.repeat(64), disclosure: preview.disclosure,
  },
})
const changed = vi.fn()

function mount(props: Partial<{ ready: boolean }> = {}) {
  return render(<AppPostgresPolicyPanel root="/api/admin/app-platform" installation={installation} ready={props.ready ?? true} onChanged={changed} />)
}

beforeEach(() => {
  apiMock.get.mockReset(); apiMock.post.mockReset(); changed.mockReset()
  apiMock.get.mockResolvedValue(preview)
})
afterEach(cleanup)

describe('PostgreSQL policy panel (doc 178 section 6)', () => {
  it('renders nothing unless the release declares the capability', async () => {
    apiMock.get.mockResolvedValue({ ...preview, declared: false })
    const { container } = mount()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith(route))
    expect(container.textContent).toBe('')
  })

  it('lists the current targets with their class and a shortened CA fingerprint', async () => {
    mount()
    expect(await screen.findByText('postgres://db.example.com:5432/orders')).toBeTruthy()
    expect(screen.getAllByText('appPostgres.network.public').length).toBeGreaterThan(0)
    expect(screen.getByText('c'.repeat(12))).toBeTruthy()
    expect(screen.getByText(/appPostgres\.disclosure/)).toBeTruthy()
  })

  it('withdraws with a plan of exactly three keys, then consumes it after confirmation', async () => {
    apiMock.post.mockImplementation(async (url: string) => url.endsWith('/plan') ? planFor([], false) : { ok: true, granted: false, policy_revision: '8' })
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPostgres.withdraw' }))
    const dialog = await screen.findByRole('dialog')
    expect(apiMock.post).toHaveBeenCalledTimes(1)
    expect(apiMock.post).toHaveBeenCalledWith(`${route}/plan`, { policyRevision: '7', previousPolicySha256: digest, targets: [] })
    expect(Object.keys(apiMock.post.mock.calls[0][1])).toHaveLength(3)
    await user.click(within(dialog).getByRole('button', { name: 'appPostgres.confirmWithdraw' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenLastCalledWith(`${route}/consume`, { plan_id: 'plan-1', plan_sha256: 'e'.repeat(64) }))
    expect(apiMock.post.mock.calls.map((call) => call[0])).toEqual([`${route}/plan`, `${route}/consume`])
    await waitFor(() => expect(changed).toHaveBeenCalled())
    expect(apiMock.get.mock.calls.length).toBeGreaterThan(1)
  })

  it('plans the edited targets, shows the private allowlist and CA in the confirmation, then consumes', async () => {
    const reviewed = [view({
      id: 'ledger', host: '10.1.2.3', port: 6432, database: 'ledger', network: 'private', ca_sha256: caDigest,
      label: 'postgres://10.1.2.3:6432/ledger', allowlist: { prefix: '10.1.2.0/24', ports: [6432], plain_http: false },
    })]
    apiMock.post.mockImplementation(async (url: string) => url.endsWith('/plan') ? planFor(reviewed) : { ok: true, granted: true, policy_revision: '8' })
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPostgres.edit' }))
    const edit = await screen.findByRole('dialog')
    const pem = '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----'
    await user.clear(within(edit).getByLabelText('appPostgres.id'))
    await user.type(within(edit).getByLabelText('appPostgres.id'), 'ledger')
    await user.clear(within(edit).getByLabelText('appPostgres.host'))
    await user.type(within(edit).getByLabelText('appPostgres.host'), '10.1.2.3')
    await user.clear(within(edit).getByLabelText('appPostgres.port'))
    await user.type(within(edit).getByLabelText('appPostgres.port'), '6432')
    await user.clear(within(edit).getByLabelText('appPostgres.database'))
    await user.type(within(edit).getByLabelText('appPostgres.database'), 'ledger')
    fireEvent.change(within(edit).getByLabelText('appPostgres.caPem'), { target: { value: pem } })
    await user.click(within(edit).getByLabelText('appPostgres.networkClass'))
    fireEvent.click(await screen.findByText('appPostgres.network.private', { selector: '.ant-select-item-option-content' }))
    await user.click(within(edit).getByRole('button', { name: 'appPostgres.review' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${route}/plan`, {
      policyRevision: '7', previousPolicySha256: digest,
      targets: [{ id: 'ledger', host: '10.1.2.3', port: 6432, database: 'ledger', network: 'private', caPem: pem }],
      deadlineMs: 10000, concurrency: 2,
    }))
    expect(apiMock.post).not.toHaveBeenCalledWith(`${route}/consume`, expect.anything())
    expect(await screen.findByText('postgres://10.1.2.3:6432/ledger')).toBeTruthy()
    expect(screen.getByText(/appPostgres\.allowlist.*10\.1\.2\.0\/24.*6432/)).toBeTruthy()
    expect(screen.getByText(/appPostgres\.caFingerprint/)).toBeTruthy()
    const confirmButton = screen.getByRole('button', { name: 'appPostgres.confirm' })
    await user.click(confirmButton)
    await waitFor(() => expect(apiMock.post).toHaveBeenLastCalledWith(`${route}/consume`, { plan_id: 'plan-1', plan_sha256: 'e'.repeat(64) }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
  })

  it('omits the CA certificate and offers no way to skip verification', async () => {
    apiMock.get.mockResolvedValue({ ...preview, granted: false, targets: [] })
    apiMock.post.mockResolvedValue(planFor([view()]))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPostgres.edit' }))
    const edit = await screen.findByRole('dialog')
    await user.type(within(edit).getByLabelText('appPostgres.id'), 'orders')
    await user.type(within(edit).getByLabelText('appPostgres.host'), 'db.example.com')
    await user.type(within(edit).getByLabelText('appPostgres.database'), 'orders')
    expect(document.body.textContent).not.toMatch(/verify|insecure|skip/i)
    await user.click(within(edit).getByRole('button', { name: 'appPostgres.review' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    expect(apiMock.post.mock.calls[0][1].targets[0]).toEqual({ id: 'orders', host: 'db.example.com', port: 5432, database: 'orders', network: 'public' })
  })

  it.each([
    'app_platform_installation_not_ready',
    'app_platform_postgres_unsupported',
    'app_platform_private_target_not_allowed',
  ])('shows a localized message for %s from the plan', async (code) => {
    apiMock.post.mockRejectedValue(new ApiError(409, 'raw', code))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPostgres.edit' }))
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'appPostgres.review' }))
    expect(await screen.findByText(`localized err.${code}`)).toBeTruthy()
    expect(apiMock.post).toHaveBeenCalledTimes(1)
  })

  it('shows a consume refusal inside the confirmation and does not refresh', async () => {
    apiMock.post.mockImplementation(async (url: string) => {
      if (url.endsWith('/plan')) return planFor([view()])
      throw new ApiError(409, 'raw', 'app_platform_private_target_not_allowed')
    })
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPostgres.edit' }))
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'appPostgres.review' }))
    await user.click(await screen.findByRole('button', { name: 'appPostgres.confirm' }))
    expect(await screen.findByText('localized err.app_platform_private_target_not_allowed')).toBeTruthy()
    expect(changed).not.toHaveBeenCalled()
  })

  it('disables editing but not withdrawal while the installation is not ready', async () => {
    mount({ ready: false })
    expect((await screen.findByRole('button', { name: 'appPostgres.edit' })).hasAttribute('disabled')).toBe(true)
    expect(screen.getByRole('button', { name: 'appPostgres.withdraw' }).hasAttribute('disabled')).toBe(false)
    expect(screen.getByText('appPostgres.notReady')).toBeTruthy()
  })

  it('shows withdraw only when granted', async () => {
    apiMock.get.mockResolvedValue({ ...preview, granted: false, targets: [] })
    mount()
    await screen.findByRole('button', { name: 'appPostgres.edit' })
    expect(screen.queryByRole('button', { name: 'appPostgres.withdraw' })).toBeNull()
  })
})
