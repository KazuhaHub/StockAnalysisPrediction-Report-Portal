import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import AppPlatformInstallPanel from './AppPlatformInstallPanel'

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), user: 'admin', t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: { get: mock.get, post: mock.post }, errText: (e: unknown) => e instanceof Error ? e.message : String(e) }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: mock.user }) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'zh-CN' }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mock.t }) }))

const operation = '11111111-1111-4111-8111-111111111111'
const plan = '22222222-2222-4222-8222-222222222222'
const release = 'a'.repeat(64)
const digest = 'b'.repeat(64)
const root = '/api/admin/app-platform'
const storageKey = 'portal.app-install.pending:admin'
const translate = mock.t
const review = () => ({ plan_id: plan, plan_sha256: digest, expires_ms: String(Date.now() + 900000), review: { app_id: 'notes', publisher_id: 'publisher', release_sha256: release, declined_capabilities: ['storage.files'], audience: [] } })
function mount(path = '/manage/apps') { return render(<MemoryRouter initialEntries={[path]}><AppPlatformInstallPanel /></MemoryRouter>) }
async function preview() {
  const user = userEvent.setup()
  await user.click(await screen.findByRole('checkbox', { name: 'appPlatform.noAudience' }))
  await user.click(await screen.findByRole('button', { name: 'appPlatform.review' }))
  await screen.findByRole('dialog')
  return user
}

beforeEach(() => {
  sessionStorage.clear(); mock.user = 'admin'; mock.t = translate; mock.get.mockReset(); mock.post.mockReset()
  mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [{ release_sha256: release, app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', trust_revision: '1', installable: true }], next_cursor: '' } : url.includes('/operations/') ? { operation_id: operation, installation_id: operation, stage: 'complete', terminal: true, control_paused: false } : { items: [], next_cursor: '' }))
  mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? review() : { operation_id: operation }))
})
afterEach(cleanup)

describe('application installation confirmation', () => {
  it('requires explicit audience intent and waits for confirmation before admission', async () => {
    mount()
    expect(await screen.findByRole('button', { name: 'appPlatform.review' })).toHaveProperty('disabled', true)
    const user = await preview()
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'install', release_sha256: release, trust_revision: '1', audience: [] })
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(0)
    expect(await screen.findByText('appPlatform.capability.storage.files')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appPlatform.cancel' }))
    expect(sessionStorage.getItem(storageKey)).toBeNull()
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(0)
  })

  it('persists a confirmed request and retries the exact key after lost response and reload', async () => {
    let attempts = 0
    mock.post.mockImplementation((url: string) => url.endsWith('/plans') ? Promise.resolve(review()) : ++attempts === 1 ? Promise.reject(new Error('offline')) : Promise.resolve({ operation_id: operation }))
    const first = mount()
    const user = await preview()
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirm' }))
    await screen.findByText('offline')
    const request = JSON.parse(sessionStorage.getItem(storageKey)!)
    expect(request).toMatchObject({ plan_id: plan, plan_sha256: digest })
    expect(request.idempotency_key).toMatch(/^[0-9a-f-]{36}$/)
    first.unmount()
    mount()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.retrySame' }))
    await screen.findByRole('link', { name: 'appPlatform.goApps' })
    const writes = mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))
    expect(writes).toHaveLength(2)
    expect(writes[0][1]).toEqual(writes[1][1])
    expect(sessionStorage.getItem(storageKey)).toBeNull()
  })

  it('resumes progress from the URL without posting another installation', async () => {
    mount(`/manage/apps?operation=${operation}`)
    await screen.findByRole('link', { name: 'appPlatform.goApps' })
    expect(mock.get).toHaveBeenCalledWith(`${root}/operations/${operation}`, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(mock.post).not.toHaveBeenCalled()
  })

  it('keeps failed loading distinct from an empty release directory', async () => {
    mock.get.mockRejectedValue(new Error('unavailable'))
    mount()
    await screen.findByText('unavailable')
    expect(screen.queryByText('appPlatform.noReleases')).toBeNull()
    expect(await screen.findByText('appPlatform.loadFailed')).toBeTruthy()
  })

  it('rejects stale preview responses across an account change and return', async () => {
    let resolve!: (value: ReturnType<typeof review>) => void
    mock.post.mockReturnValue(new Promise<ReturnType<typeof review>>(done => { resolve = done }))
    const page = mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('checkbox', { name: 'appPlatform.noAudience' }))
    await user.click(await screen.findByRole('button', { name: 'appPlatform.review' }))
    mock.user = 'other'
    page.rerender(<MemoryRouter><AppPlatformInstallPanel /></MemoryRouter>)
    mock.user = 'admin'
    page.rerender(<MemoryRouter><AppPlatformInstallPanel /></MemoryRouter>)
    await act(async () => { resolve(review()) })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('does not transmit confirmation if the request identity cannot be saved', async () => {
    mount()
    const user = await preview()
    const storage = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('storage unavailable') })
    try {
      await user.click(screen.getByRole('button', { name: 'appPlatform.confirm' }))
      await screen.findByText('storage unavailable')
      expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(0)
    } finally { storage.mockRestore() }
  })

  it('reviews a verified remote version even when it is outside the first directory page', async () => {
    const remote = { app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', descriptor_sha256: release, descriptor_url: 'https://market.example/app.json', signature_url: 'https://market.example/app.sig' }
    mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('?release_sha256=') ? { items: [{ ...remote, release_sha256: release, trust_revision: '1', installable: true }], next_cursor: '' } : url.includes('/operations/') ? { operation_id: operation, installation_id: operation, stage: 'complete', terminal: true, control_paused: false } : { items: [], next_cursor: '' }))
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/catalogue') ? { catalogue_sha256: digest, publisher_verified: false, releases: [remote] } : url.endsWith('/stage') ? { release_sha256: release } : url.endsWith('/plans') ? review() : { operation_id: operation }))
    mount()
    const user = userEvent.setup()
    const input = await screen.findByRole('textbox', { name: 'appMarket.catalogueURL' })
    await waitFor(() => expect(input).toHaveProperty('disabled', false))
    await user.type(input, 'https://market.example/index.json')
    await user.click(screen.getByRole('button', { name: 'appMarket.fetch' }))
    await user.click(await screen.findByRole('button', { name: 'appMarket.verifyDownload' }))
    await screen.findByText('appMarket.ready')
    await user.click(screen.getByRole('checkbox', { name: 'appPlatform.noAudience' }))
    await user.click(screen.getByRole('button', { name: 'appPlatform.review' }))
    await screen.findByRole('dialog')
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirm' }))
    await screen.findByRole('link', { name: 'appPlatform.goApps' })
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(1)
  })

  it('discards a cursor response overtaken by a directory reload', async () => {
    let resolve!: (value: unknown) => void
    let fresh = false
    const item = (name: string) => ({ release_sha256: release, app_id: name, publisher_id: 'publisher', version: '1.0.0', trust_revision: '1', installable: true })
    mock.get.mockImplementation((url: string) => url.includes('/releases?after=') ? new Promise(done => { resolve = done }) : Promise.resolve(url === `${root}/releases` ? { items: [item(fresh ? 'fresh' : 'notes')], next_cursor: 'c'.repeat(64) } : { items: [], next_cursor: '' }))
    const page = mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.moreReleases' }))
    fresh = true
    mock.t = key => key
    page.rerender(<MemoryRouter><AppPlatformInstallPanel /></MemoryRouter>)
    await screen.findByText('fresh')
    await act(async () => { resolve({ items: [item('stale')], next_cursor: '' }) })
    expect(screen.queryByText('stale')).toBeNull()
  })
})
