import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axe from 'axe-core'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
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
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'index.css'), 'utf8')
const review = () => ({ plan_id: plan, plan_sha256: digest, expires_ms: String(Date.now() + 900000), review: { action: 'install', app_id: 'notes', publisher_id: 'publisher', release_sha256: release, declined_capabilities: ['storage.files'], open_to_ous: [] } })
const updateReview = () => ({
  plan_id: plan,
  plan_sha256: digest,
  expires_ms: String(Date.now() + 900000),
  review: {
    action: 'update',
    installation_id: operation,
    app_id: 'notes',
    publisher_id: 'publisher',
    source_release_sha256: 'c'.repeat(64),
    release_sha256: release,
    source_generation: '1',
    target_generation: '2',
    policy_revision: '1',
    target_policy_revision: '2',
    declined_capabilities: [],
    previous_capabilities: ['reports.read'],
    target_capabilities: ['reports.read', 'storage.files'],
    declared_data_schema: '1',
    grants_sha256: digest,
  },
})
const uninstallReview = () => ({
  plan_id: plan,
  plan_sha256: digest,
  expires_ms: String(Date.now() + 900000),
  review: {
    action: 'uninstall',
    installation_id: operation,
    app_id: 'notes',
    publisher_id: 'publisher',
    release_sha256: release,
    generation: '1',
    target_generation: '2',
    managed: false,
    data_effect: 'delete',
    plugin_data_cleared: true,
    managed_data_cleared: false,
  },
})
const disableReview = () => ({
  plan_id: plan,
  plan_sha256: digest,
  expires_ms: String(Date.now() + 900000),
  review: {
    action: 'disable',
    installation_id: operation,
    app_id: 'notes',
    publisher_id: 'publisher',
    release_sha256: release,
    generation: '1',
    target_generation: '2',
    managed: false,
    data_effect: 'preserve',
  },
})
const enableReview = () => ({
  plan_id: plan,
  plan_sha256: digest,
  expires_ms: String(Date.now() + 900000),
  review: {
    action: 'enable',
    installation_id: operation,
    app_id: 'notes',
    publisher_id: 'publisher',
    release_sha256: release,
    source_generation: '2',
    target_generation: '3',
    policy_revision: '1',
    grants_sha256: digest,
    data_effect: 'preserve',
  },
})
function mount(path = '/manage/apps') { return render(<MemoryRouter initialEntries={[path]}><AppPlatformInstallPanel /></MemoryRouter>) }
async function preview() {
  const user = userEvent.setup()
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
  it('installs closed to everyone unless OUs are chosen, and waits for confirmation before admission', async () => {
    mount()
    expect(await screen.findByRole('button', { name: 'appPlatform.review' })).toHaveProperty('disabled', false)
    const user = await preview()
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'install', release_sha256: release, trust_revision: '1', open_to_ous: [] })
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(0)
    expect(await screen.findByText('storage.files')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appPlatform.cancel' }))
    expect(sessionStorage.getItem(storageKey)).toBeNull()
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(0)
  })

  it('shows verified compatibility, required and optional permissions, and resources before install', async () => {
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? {
      items: [{
        release_sha256: release,
        app_id: 'notes',
        publisher_id: 'publisher',
        version: '1.0.0',
        trust_revision: '1',
        installable: true,
        compatibility: {
          platform_api: 'portal.app-platform/1',
          runner_protocol: 'portal.runner/1',
        },
        platforms: ['linux/amd64', 'linux/arm64'],
        required_capabilities: ['reports.read'],
        optional_capabilities: ['notifications.publish'],
        resources: { memory_mib: 256, cpu_millis: 500, pids: 128 },
        data_schema: 1,
        backend: true,
      }],
      next_cursor: '',
    } : { items: [], next_cursor: '' }))
    mount()
    await preview()
    expect(screen.getByText('appPlatform.compatibility')).toBeTruthy()
    expect(screen.getByText('portal.app-platform/1 · portal.runner/1')).toBeTruthy()
    expect(screen.getByText('appPlatform.requiredPermissions')).toBeTruthy()
    expect(screen.getAllByText('reports.read').length).toBeGreaterThan(0)
    expect(screen.getByText('appPlatform.optionalPermissions')).toBeTruthy()
    expect(screen.getAllByText('notifications.publish').length).toBeGreaterThan(0)
    expect(screen.getByText('appPlatform.resources')).toBeTruthy()
    expect(screen.getByText('appPlatform.resourceSummary')).toBeTruthy()
  })

  describe('principals.read disclosure (doc 181 section 4)', () => {
    const locales = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'locales')
    const english = JSON.parse(readFileSync(join(locales, 'en-US.json'), 'utf8')) as Record<string, string>
    const sentence = "The app's administrators can list the names of users and OUs that can use this app."
    const principalsRelease = { release_sha256: release, app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', trust_revision: '1', installable: true, required_capabilities: [], optional_capabilities: ['principals.read'] }

    it('states the disclosure in the install review', async () => {
      mock.t = (key, options) => english[key] ?? options?.defaultValue ?? key
      mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [principalsRelease], next_cursor: '' } : { items: [], next_cursor: '' }))
      mount()
      const user = userEvent.setup()
      await user.click(await screen.findByRole('button', { name: english['appPlatform.review'] }))
      await screen.findByRole('dialog')
      expect(within(screen.getByRole('dialog')).getAllByText(sentence).length).toBeGreaterThan(0)
    })

    it('states the disclosure in the update review', async () => {
      mock.t = (key, options) => english[key] ?? options?.defaultValue ?? key
      const installed = { ...principalsRelease, installable: false, reason: 'already_installed', installation_id: operation, current_release_sha256: 'c'.repeat(64), updateable: true }
      mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [installed], next_cursor: '' } : { items: [], next_cursor: '' }))
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? updateReview() : { operation_id: operation }))
      mount()
      const user = userEvent.setup()
      await user.click(await screen.findByRole('button', { name: english['appPlatform.reviewUpdate'] }))
      await screen.findByRole('dialog')
      expect(within(screen.getByRole('dialog')).getAllByText(sentence).length).toBeGreaterThan(0)
    })

    it('ships a faithful sentence in every locale', () => {
      for (const name of ['en-US', 'zh-CN', 'zh-TW']) {
        const table = JSON.parse(readFileSync(join(locales, `${name}.json`), 'utf8')) as Record<string, string>
        expect(table['appPlatform.capability.principals.read'], name).toBeTruthy()
      }
    })
  })

  describe('background network access sentence', () => {
    const sentence = 'appPlatform.backgroundNetworkNote'
    const release_ = (required: string[], optional: string[]) => ({ release_sha256: release, app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', trust_revision: '1', installable: true, backend: true, required_capabilities: required, optional_capabilities: optional, resources: { memory_mib: 256, cpu_millis: 500, pids: 128 }, platforms: ['linux/amd64'], compatibility: { platform_api: 'a', runner_protocol: 'b' }, data_schema: 1 })
    const serve = (item: Record<string, unknown>, declined: string[]) => {
      mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [item], next_cursor: '' } : { items: [], next_cursor: '' }))
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? { ...review(), review: { ...review().review, declined_capabilities: declined } } : { operation_id: operation }))
    }

    it('tells the administrator when both background.run and network.http are granted', async () => {
      serve(release_(['background.run', 'network.http'], []), [])
      mount()
      await preview()
      expect(screen.getByText(sentence)).toBeTruthy()
    })

    it('shows it for an optional network.http only while it is not declined', async () => {
      serve(release_(['background.run'], ['network.http']), [])
      mount()
      await preview()
      expect(screen.getByText(sentence)).toBeTruthy()
    })

    it('does not show it when network.http is declined, background.run is absent or only one is declared', async () => {
      for (const [item, declined] of [
        [release_(['background.run'], ['network.http']), ['network.http']],
        [release_(['network.http'], []), []],
        [release_(['background.run'], []), []],
        [release_(['reports.read'], ['background.run']), ['background.run']],
      ] as [Record<string, unknown>, string[]][]) {
        serve(item, declined)
        const view = mount()
        await preview()
        expect(screen.queryByText(sentence)).toBeNull()
        view.unmount()
      }
    })

    it('tells the administrator that reviewed credentials may be used in the background', async () => {
      const credentialSentence = 'appPlatform.backgroundCredentialsNote'
      for (const [item, declined, shown] of [
        [release_(['background.run', 'network.http', 'secrets.use'], []), [], true],
        [release_(['background.run', 'network.http'], ['secrets.use']), [], true],
        [release_(['background.run', 'network.http'], ['secrets.use']), ['secrets.use'], false],
        [release_(['network.http', 'secrets.use'], []), [], false],
        [release_(['background.run', 'network.http'], []), [], false],
      ] as [Record<string, unknown>, string[], boolean][]) {
        serve(item, declined)
        const view = mount()
        await preview()
        expect(screen.queryByText(credentialSentence) !== null).toBe(shown)
        view.unmount()
      }
    })

    it('shows it for an update review too', async () => {
      const installed = { ...release_(['background.run', 'network.http'], []), installable: false, reason: 'already_installed', installation_id: operation, current_release_sha256: 'c'.repeat(64), updateable: true }
      mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [installed], next_cursor: '' } : { items: [], next_cursor: '' }))
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? updateReview() : { operation_id: operation }))
      mount()
      const user = userEvent.setup()
      await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewUpdate' }))
      await screen.findByText('appPlatform.permissionDiff')
      expect(screen.getByText(sentence)).toBeTruthy()
    })
  })

  it('keeps long lifecycle metadata in bounded local scrollers', async () => {
    mount()
    await preview()
    expect(document.querySelector('.rp-platform-release-table')).not.toBeNull()
    expect(document.querySelector('.rp-platform-review')).not.toBeNull()
    expect(css).toMatch(/\.rp-platform-release-table \.ant-table-content\s*\{[^}]*overflow-x:\s*auto/)
    expect(css).toMatch(/\.rp-platform-release-table :is\(th, td\)\s*\{[^}]*overflow-wrap:\s*anywhere/)
    expect(css).toMatch(/\.rp-platform-review \.ant-descriptions-item-content\s*\{[^}]*min-width:\s*0/)
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

  it('refreshes the release directory exactly once after observing a terminal operation', async () => {
    let releaseReads = 0
    mock.get.mockImplementation((url: string) => {
      if (url === `${root}/releases`) {
        releaseReads++
        return Promise.resolve({ items: [], next_cursor: '' })
      }
      if (url.includes('/operations/')) {
        return Promise.resolve({
          operation_id: operation,
          installation_id: operation,
          action: 'install',
          stage: 'complete',
          terminal: true,
          control_paused: false,
        })
      }
      return Promise.resolve({ items: [], next_cursor: '' })
    })
    mount(`/manage/apps?operation=${operation}`)
    await screen.findByRole('link', { name: 'appPlatform.goApps' })
    await waitFor(() => expect(releaseReads).toBe(2))
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(releaseReads).toBe(2)
    expect(mock.post).not.toHaveBeenCalled()
  })

  it('renders lifecycle progress and review-required states for repair and deletion', async () => {
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases`
      ? { items: [], next_cursor: '' }
      : url.includes('/operations/')
      ? {
          operation_id: operation,
          installation_id: operation,
          action: 'repair',
          stage: 'recovery_required',
          terminal: true,
          control_paused: false,
          error_code: 'managed_repair_recovery_required',
        }
      : { items: [], next_cursor: '' }))
    mount(`/manage/apps?operation=${operation}`)
    expect(await screen.findByText('recovery_required')).toBeTruthy()
    expect(screen.getByText('appPlatform.operationNeedsReview')).toBeTruthy()
  })

  it('shows a paused lifecycle as a warning rather than a terminal success', async () => {
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases`
      ? { items: [], next_cursor: '' }
      : url.includes('/operations/')
      ? {
          operation_id: operation,
          installation_id: operation,
          action: 'delete_data',
          stage: 'remove_data',
          terminal: false,
          control_paused: true,
        }
      : { items: [], next_cursor: '' }))
    mount(`/manage/apps?operation=${operation}`)
    expect(await screen.findByText('err.app_platform_control_paused')).toBeTruthy()
    expect(screen.queryByRole('link', { name: 'appPlatform.goApps' })).toBeNull()
  })

  it('reviews an installed app update without requiring an audience choice', async () => {
    const installedRelease = {
      release_sha256: release,
      app_id: 'notes',
      publisher_id: 'publisher',
      version: '1.1.0',
      trust_revision: '1',
      installable: false,
      reason: 'already_installed',
      installation_id: operation,
      current_release_sha256: 'c'.repeat(64),
      updateable: true,
    }
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [installedRelease], next_cursor: '' } : url.includes('/operations/') ? { operation_id: operation, installation_id: operation, stage: 'complete', terminal: true, control_paused: false } : { items: [], next_cursor: '' }))
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? updateReview() : { operation_id: operation }))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewUpdate' }))
    await screen.findByText('appPlatform.permissionDiff')
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'update', release_sha256: release, trust_revision: '1', installation_id: operation })
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirmUpdate' }))
    await screen.findByRole('link', { name: 'appPlatform.goApps' })
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(1)
  })

  // Doc 185 section 3.2: an update that raises the data quota says so before it is confirmed, and one the runner's
  // capacity cannot hold cannot be confirmed.
  describe('an update that raises the data quota', () => {
    const installedRelease = {
      release_sha256: release,
      app_id: 'notes',
      publisher_id: 'publisher',
      version: '1.1.0',
      trust_revision: '1',
      installable: false,
      reason: 'already_installed',
      installation_id: operation,
      current_release_sha256: 'c'.repeat(64),
      updateable: true,
    }
    const quotaReview = (quota: Record<string, unknown>) => {
      const result = updateReview()
      Object.assign(result.review, { data_quota: {
        current_bytes: '268435456', current_inodes: '65536', requested_bytes: '1073741824', requested_inodes: '100000',
        new_bytes: '1073741824', new_inodes: '100000', raise_applied: true, capacity: 'fits', ...quota,
      } })
      return result
    }
    const open = async (result: ReturnType<typeof updateReview>) => {
      mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [installedRelease], next_cursor: '' } : { items: [], next_cursor: '' }))
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? result : { operation_id: operation }))
      mount()
      const user = userEvent.setup()
      await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewUpdate' }))
      await screen.findByText('appPlatform.permissionDiff')
      return user
    }

    it('shows the raise and says it stays raised after a rollback', async () => {
      await open(quotaReview({}))
      expect(screen.getByText('appPlatform.updateQuota')).toBeTruthy()
      expect(screen.getByText('appPlatform.updateQuotaRaise')).toBeTruthy()
      expect(screen.getByText('appPlatform.updateQuotaHint')).toBeTruthy()
      expect(screen.queryByTestId('update-quota-short')).toBeNull()
      expect((screen.getByRole('button', { name: 'appPlatform.confirmUpdate' }) as HTMLButtonElement).disabled).toBe(false)
    })

    it('names the shortfall and cannot be confirmed when the runner is short', async () => {
      await open(quotaReview({ capacity: 'short', capacity_dimension: 'data_quota', capacity_requested: '6442450944', capacity_remaining: '1073741824' }))
      expect(screen.getByTestId('update-quota-short')).toBeTruthy()
      expect((screen.getByRole('button', { name: 'appPlatform.confirmUpdate' }) as HTMLButtonElement).disabled).toBe(true)
    })

    it('shows nothing about a quota for an update that raises none', async () => {
      await open(updateReview())
      expect(screen.queryByText('appPlatform.updateQuota')).toBeNull()
    })
  })

  it('treats legacy null update capability lists as empty', async () => {
    const installedRelease = {
      release_sha256: release,
      app_id: 'notes',
      publisher_id: 'publisher',
      version: '1.1.0',
      trust_revision: '1',
      installable: false,
      reason: 'already_installed',
      installation_id: operation,
      current_release_sha256: 'c'.repeat(64),
      updateable: true,
    }
    const legacy = updateReview()
    legacy.review.previous_capabilities = null as never
    legacy.review.target_capabilities = null as never
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [installedRelease], next_cursor: '' } : { items: [], next_cursor: '' }))
    mock.post.mockResolvedValue(legacy)
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewUpdate' }))
    expect(await screen.findByText('appPlatform.permissionDiff')).toBeTruthy()
    expect(screen.getByText('appPlatform.confirmUpdate')).toBeTruthy()
  })

  it('reviews disable without describing data deletion', async () => {
    const installedRelease = {
      release_sha256: release,
      app_id: 'notes',
      publisher_id: 'publisher',
      version: '1.0.0',
      trust_revision: '1',
      installable: false,
      reason: 'already_installed',
      installation_id: operation,
      installed: true,
    }
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [installedRelease], next_cursor: '' } : { items: [], next_cursor: '' }))
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? disableReview() : { operation_id: operation }))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewDisable' }))
    expect(await screen.findByText('appPlatform.confirmDisableTitle')).toBeTruthy()
    expect(screen.getByText('appPlatform.disableDataRetained')).toBeTruthy()
    expect(screen.getByText('appPlatform.disableConfirmHint')).toBeTruthy()
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'disable', installation_id: operation })
    expect(screen.queryByText('appPlatform.uninstallPluginData')).toBeNull()
  })

  it('reviews a disabled app enable without changing release or permissions', async () => {
    const disabledRelease = {
      release_sha256: release,
      app_id: 'notes',
      publisher_id: 'publisher',
      version: '1.0.0',
      trust_revision: '1',
      installable: false,
      reason: 'already_installed',
      installation_id: operation,
      current_release_sha256: release,
      enableable: true,
    }
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [disabledRelease], next_cursor: '' } : url.includes('/operations/') ? { operation_id: operation, installation_id: operation, stage: 'complete', terminal: true, control_paused: false } : { items: [], next_cursor: '' }))
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? enableReview() : { operation_id: operation }))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewEnable' }))
    expect(await screen.findByText('appPlatform.enableConfirmHint')).toBeTruthy()
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'enable', installation_id: operation })
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirmEnable' }))
    await screen.findByRole('link', { name: 'appPlatform.goApps' })
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(1)
  })

  it('offers the uninstall review for a first install that failed after its candidate started', async () => {
    const failedRelease = {
      release_sha256: release,
      app_id: 'notes',
      publisher_id: 'publisher',
      version: '1.0.0',
      trust_revision: '1',
      installable: false,
      reason: 'already_installed',
      installation_id: operation,
      current_release_sha256: release,
      uninstallable: true,
    }
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [failedRelease], next_cursor: '' } : { items: [], next_cursor: '' }))
    mock.post.mockResolvedValue(uninstallReview())
    mount()
    const user = userEvent.setup()
    expect(screen.queryByRole('button', { name: 'appPlatform.reviewDisable' })).toBeNull()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewUninstall' }))
    expect(await screen.findByText('appPlatform.uninstallConfirmHint')).toBeTruthy()
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'uninstall', installation_id: operation })
    expect(screen.queryByText('appPlatform.reason.already_installed')).toBeNull()
  })

  it('keeps the uninstall review accessible to assistive technology', async () => {
    const installedRelease = {
      release_sha256: release,
      app_id: 'notes',
      publisher_id: 'publisher',
      version: '1.0.0',
      trust_revision: '1',
      installable: false,
      reason: 'already_installed',
      installation_id: operation,
      current_release_sha256: release,
      installed: true,
    }
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [installedRelease], next_cursor: '' } : { items: [], next_cursor: '' }))
    mock.post.mockResolvedValue(uninstallReview())
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewUninstall' }))
    expect((await screen.findByRole('document')).getAttribute('tabindex')).toBe('0')
    const result = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
        region: { enabled: false },
      },
    })
    expect(result.violations).toEqual([])
  })

  it('keeps the complete lifecycle panel keyboard reachable and free of axe violations', async () => {
    mount()
    const user = userEvent.setup()
    const pageResult = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
        region: { enabled: false },
      },
    })
    expect(pageResult.violations).toEqual([])

    const reviewButton = screen.getByRole('button', { name: 'appPlatform.review' })
    reviewButton.focus()
    await user.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true))
    const dialogResult = await axe.run(dialog, {
      rules: {
        'color-contrast': { enabled: false },
        region: { enabled: false },
      },
    })
    expect(dialogResult.violations).toEqual([])
  })

  it('requests the uninstall review close with Escape', async () => {
    const installedRelease = {
      release_sha256: release,
      app_id: 'notes',
      publisher_id: 'publisher',
      version: '1.0.0',
      trust_revision: '1',
      installable: false,
      reason: 'already_installed',
      installation_id: operation,
      current_release_sha256: release,
      installed: true,
    }
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [installedRelease], next_cursor: '' } : url.includes('/operations/') ? { operation_id: operation, installation_id: operation, stage: 'complete', terminal: true, control_paused: false } : { items: [], next_cursor: '' }))
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? uninstallReview() : { operation_id: operation }))
    mount()
    const user = userEvent.setup()
    const trigger = await screen.findByRole('button', { name: 'appPlatform.reviewUninstall' })
    await user.click(trigger)
    const dialog = await screen.findByRole('dialog')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(dialog.className).toContain('ant-zoom-leave'))
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('requires a separate confirmation before uninstalling and clearing plugin data', async () => {
    const installedRelease = {
      release_sha256: release,
      app_id: 'notes',
      publisher_id: 'publisher',
      version: '1.0.0',
      trust_revision: '1',
      installable: false,
      reason: 'already_installed',
      installation_id: operation,
      current_release_sha256: release,
      installed: true,
    }
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases` ? { items: [installedRelease], next_cursor: '' } : url.includes('/operations/') ? { operation_id: operation, installation_id: operation, stage: 'complete', terminal: true, control_paused: false } : { items: [], next_cursor: '' }))
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? uninstallReview() : { operation_id: operation }))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewUninstall' }))
    await screen.findByText('appPlatform.uninstallPluginData')
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'uninstall', installation_id: operation })
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirmUninstall' }))
    await screen.findByRole('link', { name: 'appPlatform.goApps' })
    expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))).toHaveLength(1)
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

describe('opening an installation to organizational units', () => {
  it('sends the chosen OU ids as open_to_ous and shows them in the review', async () => {
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases`
      ? { items: [{ release_sha256: release, app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', trust_revision: '1', installable: true }], next_cursor: '' }
      : { items: url.includes('kind=ou') ? [{ id: '7', name: 'Research' }] : [], next_cursor: '' }))
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans')
      ? { ...review(), review: { ...review().review, open_to_ous: ['7'] } }
      : { operation_id: operation }))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('combobox', { name: 'appPlatform.audience' }))
    await user.click(await screen.findByText('Research (7)'))
    await user.click(await screen.findByRole('button', { name: 'appPlatform.review' }))
    const dialog = await screen.findByRole('dialog')
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'install', release_sha256: release, trust_revision: '1', open_to_ous: ['7'] })
    expect(within(dialog).getByText('Research')).toBeTruthy()
    expect(mock.get.mock.calls.some(([url]) => String(url).includes('kind=user'))).toBe(false)
  })

  it('says that choosing the Default OU opens the application to every organizational unit', async () => {
    mock.get.mockImplementation((url: string) => Promise.resolve(url === `${root}/releases`
      ? { items: [{ release_sha256: release, app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', trust_revision: '1', installable: true }], next_cursor: '' }
      : { items: url.includes('kind=ou') ? [{ id: '1', name: 'Default', default: true }, { id: '7', name: 'Research' }] : [], next_cursor: '' }))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('combobox', { name: 'appPlatform.audience' }))
    await user.click(await screen.findByText('Research (7)'))
    expect(screen.queryByText('appPlatform.defaultOUNote')).toBeNull()
    await user.click(await screen.findByText('Default (1)'))
    expect(await screen.findByText('appPlatform.defaultOUNote')).toBeTruthy()
  })
})
