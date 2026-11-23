import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axe from 'axe-core'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { MemoryRouter, useLocation } from 'react-router'
import { ApiError } from '../../api/client'
import PlatformLifecycleModal from './PlatformLifecycleModal'
import { usePlatformLifecycle, type LifecycleAction } from './platformLifecycle'
import type { PlatformRelease } from '../../api/appPlatform'

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), user: 'admin', lang: 'zh-CN', t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: { get: mock.get, post: mock.post }, errText: (e: unknown) => e instanceof Error ? e.message : String(e) }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: mock.user }) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: mock.lang }) }))
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
const repairReview = () => {
  const enabled = enableReview()
  return { ...enabled, review: { ...enabled.review, action: 'repair', data_effect: 'checkpoint_and_retain' } }
}
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

const base: PlatformRelease = { release_sha256: release, app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', trust_revision: '1', installable: true }
const installed = (extra: Partial<PlatformRelease> = {}): PlatformRelease => ({ ...base, installable: false, reason: 'already_installed', installation_id: operation, ...extra })
let changes = 0
function Harness({ item, action }: { item: PlatformRelease; action: LifecycleAction }) {
  const lifecycle = usePlatformLifecycle(() => { changes++ })
  return <>
    <button onClick={() => lifecycle.begin(item, action)}>start</button>
    {lifecycle.hiddenPending && <button onClick={lifecycle.reopen}>reopen</button>}
    <PlatformLifecycleModal lifecycle={lifecycle} />
    <LocationProbe />
  </>
}
function LocationProbe() { const where = useLocation(); return <output data-testid="location">{where.pathname}</output> }
const tree = (item: PlatformRelease = base, action: LifecycleAction = 'install') => <MemoryRouter initialEntries={[location]}><Harness item={item} action={action} /></MemoryRouter>
let location = '/manage/apps'
function mount(item: PlatformRelease = base, action: LifecycleAction = 'install', path = '/manage/apps') { location = path; return render(tree(item, action)) }
const operations = () => mock.post.mock.calls.filter(([url]) => url.endsWith('/operations'))
const finished = { operation_id: operation, installation_id: operation, stage: 'complete', terminal: true, control_paused: false }
// Opens the flow and, for an install, takes the audience step with "next".
async function start(next = true) {
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'start' }))
  await screen.findByRole('dialog')
  if (next) await user.click(await screen.findByRole('button', { name: 'appPlatform.next' }))
  return user
}
// A closing modal stays in the DOM while it animates out, and jsdom never finishes the animation.
const closed = () => waitFor(() => { const d = screen.queryByRole('dialog'); expect(!d || d.className.includes('ant-zoom-leave')).toBe(true) })
async function preview() { const user = await start(); await screen.findByRole('document'); return user }

beforeEach(() => {
  sessionStorage.clear(); mock.lang = 'zh-CN'; mock.user = 'admin'; mock.t = translate; mock.get.mockReset(); mock.post.mockReset(); changes = 0
  mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? finished : { items: [], next_cursor: '' }))
  mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? review() : { operation_id: operation }))
})
afterEach(cleanup)

describe('application installation flow', () => {
  it('installs closed to everyone unless OUs are chosen, and waits for confirmation before admission', async () => {
    mount()
    const user = await preview()
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'install', release_sha256: release, trust_revision: '1', open_to_ous: [] })
    expect(operations()).toHaveLength(0)
    expect(await screen.findByText('storage.files')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appPlatform.cancel' }))
    expect(sessionStorage.getItem(storageKey)).toBeNull()
    expect(operations()).toHaveLength(0)
  })

  it('asks for the audience first and does not create a plan until next', async () => {
    mount()
    await start(false)
    expect(await screen.findByRole('combobox', { name: 'appPlatform.audience' })).toBeTruthy()
    expect(screen.getByText('appPlatform.step.audience')).toBeTruthy()
    expect(mock.post).not.toHaveBeenCalled()
  })

  it('shows verified compatibility, required and optional permissions, and resources before install', async () => {
    mount({ ...base, compatibility: { platform_api: 'portal.app-platform/1', runner_protocol: 'portal.runner/1' }, platforms: ['linux/amd64', 'linux/arm64'], required_capabilities: ['reports.read'], optional_capabilities: ['notifications.publish'], resources: { memory_mib: 256, cpu_millis: 500, pids: 128 }, data_schema: 1, backend: true })
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
    const principals = { ...base, required_capabilities: [], optional_capabilities: ['principals.read'] }

    it('states the disclosure in the install review', async () => {
      mock.t = (key, options) => english[key] ?? options?.defaultValue ?? key
      mount(principals)
      const user = userEvent.setup()
      await user.click(screen.getByRole('button', { name: 'start' }))
      await user.click(await screen.findByRole('button', { name: english['appPlatform.next'] }))
      await screen.findByRole('document')
      expect(within(screen.getByRole('dialog')).getAllByText(sentence).length).toBeGreaterThan(0)
    })

    it('states the disclosure in the update review', async () => {
      mock.t = (key, options) => english[key] ?? options?.defaultValue ?? key
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? updateReview() : { operation_id: operation }))
      mount(installed({ ...principals, updateable: true }), 'update')
      await start(false)
      await screen.findByRole('document')
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
    const item = (required: string[], optional: string[]): PlatformRelease => ({ ...base, backend: true, required_capabilities: required, optional_capabilities: optional, resources: { memory_mib: 256, cpu_millis: 500, pids: 128 }, platforms: ['linux/amd64'], compatibility: { platform_api: 'a', runner_protocol: 'b' }, data_schema: 1 })
    const serve = (declined: string[]) => mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? { ...review(), review: { ...review().review, declined_capabilities: declined } } : { operation_id: operation }))

    it('tells the administrator when both background.run and network.http are granted', async () => {
      serve([])
      mount(item(['background.run', 'network.http'], []))
      await preview()
      expect(screen.getByText(sentence)).toBeTruthy()
    })

    it('shows it for an optional network.http only while it is not declined', async () => {
      serve([])
      mount(item(['background.run'], ['network.http']))
      await preview()
      expect(screen.getByText(sentence)).toBeTruthy()
    })

    it('does not show it when network.http is declined, background.run is absent or only one is declared', async () => {
      for (const [release_, declined] of [
        [item(['background.run'], ['network.http']), ['network.http']],
        [item(['network.http'], []), []],
        [item(['background.run'], []), []],
        [item(['reports.read'], ['background.run']), ['background.run']],
      ] as [PlatformRelease, string[]][]) {
        serve(declined)
        const view = mount(release_)
        await preview()
        expect(screen.queryByText(sentence)).toBeNull()
        view.unmount()
      }
    })

    it('tells the administrator that reviewed credentials may be used in the background', async () => {
      const credentialSentence = 'appPlatform.backgroundCredentialsNote'
      for (const [release_, declined, shown] of [
        [item(['background.run', 'network.http', 'secrets.use'], []), [], true],
        [item(['background.run', 'network.http'], ['secrets.use']), [], true],
        [item(['background.run', 'network.http'], ['secrets.use']), ['secrets.use'], false],
        [item(['network.http', 'secrets.use'], []), [], false],
        [item(['background.run', 'network.http'], []), [], false],
      ] as [PlatformRelease, string[], boolean][]) {
        serve(declined)
        const view = mount(release_)
        await preview()
        expect(screen.queryByText(credentialSentence) !== null).toBe(shown)
        view.unmount()
      }
    })

    it('shows it for an update review too', async () => {
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? updateReview() : { operation_id: operation }))
      mount(installed({ ...item(['background.run', 'network.http'], []), updateable: true }), 'update')
      await start(false)
      await screen.findByText('appPlatform.permissionDiff')
      expect(screen.getByText(sentence)).toBeTruthy()
    })
  })

  it('keeps long lifecycle metadata in a bounded local scroller', async () => {
    mount()
    await preview()
    expect(document.querySelector('.rp-platform-review')).not.toBeNull()
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
    await screen.findByRole('button', { name: 'appPlatform.done' })
    const writes = operations()
    expect(writes).toHaveLength(2)
    expect(writes[0][1]).toEqual(writes[1][1])
    expect(sessionStorage.getItem(storageKey)).toBeNull()
  })

  it('keeps an uncertain request after the modal is hidden and offers to reopen it', async () => {
    mock.post.mockImplementation((url: string) => url.endsWith('/plans') ? Promise.resolve(review()) : Promise.reject(new Error('offline')))
    mount()
    const user = await preview()
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirm' }))
    await screen.findByText('offline')
    await user.click(screen.getByRole('button', { name: 'appPlatform.cancel' }))
    await closed()
    expect(sessionStorage.getItem(storageKey)).not.toBeNull()
    await user.click(screen.getByRole('button', { name: 'reopen' }))
    // The reopened dialog is queried by text: jsdom never finishes the earlier leave animation, which leaves the old wrapper inert.
    expect(await screen.findByText('appPlatform.retrySame')).toBeTruthy()
  })

  it('resumes progress from the URL without posting another installation', async () => {
    mount(base, 'install', `/manage/apps?operation=${operation}`)
    await screen.findByRole('button', { name: 'appPlatform.done' })
    expect(mock.get).toHaveBeenCalledWith(`${root}/operations/${operation}`, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(mock.post).not.toHaveBeenCalled()
  })

  it('opens the app settings after completion and tells the page once that the operation is terminal', async () => {
    mount(base, 'install', `/manage/apps?operation=${operation}`)
    const settings = await screen.findByRole('button', { name: 'appPlatform.openSettings' })
    await waitFor(() => expect(changes).toBe(1))
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(changes).toBe(1)
    expect(mock.post).not.toHaveBeenCalled()
    await userEvent.setup().click(settings)
    expect(screen.getByTestId('location').textContent).toBe(`/manage/apps/${encodeURIComponent(`platform:${operation}`)}`)
  })

  // antd spaces footer buttons with `.ant-btn + .ant-btn`; a button wrapped in a link breaks that adjacency and the
  // buttons render glued together.
  it('keeps every completed-footer action a direct button sibling so antd spaces them', async () => {
    mount(base, 'install', `/manage/apps?operation=${operation}`)
    await screen.findByRole('button', { name: 'appPlatform.openSettings' })
    const footer = document.querySelector('.rp-platform-review-modal .ant-modal-footer')!
    const children = [...footer.children]
    expect(children.length).toBeGreaterThanOrEqual(3)
    for (const child of children) expect(child.tagName).toBe('BUTTON')
  })

  it('does not offer the settings of an app that a completed uninstall removed', async () => {
    mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? { ...finished, action: 'uninstall' } : { items: [], next_cursor: '' }))
    mount(base, 'install', `/manage/apps?operation=${operation}`)
    await screen.findByRole('button', { name: 'appPlatform.done' })
    expect(screen.queryByRole('button', { name: 'appPlatform.openSettings' })).toBeNull()
  })

  it('renders lifecycle progress and review-required states for repair and deletion', async () => {
    mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? { operation_id: operation, installation_id: operation, action: 'repair', stage: 'recovery_required', terminal: true, control_paused: false, error_code: 'managed_repair_recovery_required' } : { items: [], next_cursor: '' }))
    mount(base, 'install', `/manage/apps?operation=${operation}`)
    expect(await screen.findByText('recovery_required')).toBeTruthy()
    expect(screen.getByText('appPlatform.operationNeedsReview')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'appPlatform.openSettings' })).toBeNull()
  })

  it('shows a paused lifecycle as a warning rather than a terminal success', async () => {
    mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? { operation_id: operation, installation_id: operation, action: 'delete_data', stage: 'remove_data', terminal: false, control_paused: true } : { items: [], next_cursor: '' }))
    mount(base, 'install', `/manage/apps?operation=${operation}`)
    expect(await screen.findByText('err.app_platform_control_paused')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'appPlatform.openSettings' })).toBeNull()
  })

  describe('the progress is an explicit list of the backend\'s stages', () => {
    const status = (stage: string) => {
      const item = [...document.querySelectorAll('.rp-operation-steps .ant-steps-item-title')].find(node => node.textContent === `appPlatform.stage.${stage}`)?.closest('.ant-steps-item')
      return item ? ['finish', 'process', 'wait', 'error'].find(name => item.classList.contains(`ant-steps-item-${name}`)) : undefined
    }
    const reading = (stage: string, extra: Record<string, unknown> = {}) => ({ operation_id: operation, installation_id: operation, action: 'install', stage, terminal: false, control_paused: false, ...extra })

    it('lists the stages of the action with the finished ones, the current one and the ones to come', async () => {
      mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? reading('stage') : { items: [], next_cursor: '' }))
      mount(base, 'install', `/manage/apps?operation=${operation}`)
      await screen.findByRole('button', { name: 'appPlatform.done' })
      expect(['queued', 'validate', 'download'].map(status)).toEqual(['finish', 'finish', 'finish'])
      expect(status('stage')).toBe('process')
      expect(['prepare_data', 'activate', 'complete'].map(status)).toEqual(['wait', 'wait', 'wait'])
      // The runtime stages belong to an app with a backend; this one has none.
      expect(status('start')).toBeUndefined()
    })

    it('adds the runtime stages for an operation the backend says is managed', async () => {
      mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? reading('start', { managed: true, revision: '6' }) : { items: [], next_cursor: '' }))
      mount(base, 'install', `/manage/apps?operation=${operation}`)
      await screen.findByRole('button', { name: 'appPlatform.done' })
      expect(status('start')).toBe('process')
      expect(status('probe')).toBe('wait')
    })

    it('finishes every step when the operation is complete', async () => {
      mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? reading('complete', { terminal: true }) : { items: [], next_cursor: '' }))
      mount(base, 'install', `/manage/apps?operation=${operation}`)
      await screen.findByRole('button', { name: 'appPlatform.openSettings' })
      expect(['queued', 'download', 'activate', 'complete'].map(status)).toEqual(['finish', 'finish', 'finish', 'finish'])
    })

    it('marks the failed step by the revision on a page opened after the failure, with no earlier poll', async () => {
      // Install, unmanaged: stage is the 4th stage (revision 4), and a failure in it leaves revision 5.
      mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? reading('failed', { terminal: true, revision: '5', error_code: 'app_platform_unavailable' }) : { items: [], next_cursor: '' }))
      mount(base, 'install', `/manage/apps?operation=${operation}`)
      await screen.findByRole('button', { name: 'appPlatform.done' })
      expect(status('stage')).toBe('error')
      expect(status('download')).toBe('finish')
      expect(status('prepare_data')).toBe('wait')
    })

    it('keeps the steps it showed: a managed flag from the backend does not change while polling', async () => {
      const answers = [reading('stage', { managed: true, revision: '5' }), reading('probe', { managed: true, revision: '8' })]
      mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? answers.shift() ?? answers[0] : { items: [], next_cursor: '' }))
      mount(base, 'install', `/manage/apps?operation=${operation}`)
      await screen.findByRole('button', { name: 'appPlatform.done' })
      expect(status('start')).toBe('wait')
      await userEvent.setup().click(screen.getByRole('button', { name: 'appPlatform.refresh' }))
      await waitFor(() => expect(status('probe')).toBe('process'))
      expect(['queued', 'validate', 'download', 'stage', 'prepare_data', 'start', 'activate', 'complete'].every(stage => status(stage) !== undefined)).toBe(true)
    })

    it('marks the step a failure stopped at, and says it in words as well', async () => {
      const answers = [reading('stage'), reading('failed', { terminal: true, revision: '5', error_code: 'app_platform_unavailable' })]
      mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? answers.shift() ?? answers[0] : { items: [], next_cursor: '' }))
      mount(base, 'install', `/manage/apps?operation=${operation}`)
      await screen.findByRole('button', { name: 'appPlatform.done' })
      await userEvent.setup().click(screen.getByRole('button', { name: 'appPlatform.refresh' }))
      await waitFor(() => expect(status('stage')).toBe('error'))
      expect(status('download')).toBe('finish')
      expect(status('prepare_data')).toBe('wait')
      expect(screen.getByText('failed')).toBeTruthy()
    })
  })

  it('reviews an installed app update without requiring an audience choice', async () => {
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? updateReview() : { operation_id: operation }))
    mount(installed({ version: '1.1.0', current_release_sha256: 'c'.repeat(64), updateable: true }), 'update')
    const user = await start(false)
    await screen.findByText('appPlatform.permissionDiff')
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'update', release_sha256: release, trust_revision: '1', installation_id: operation })
    expect(screen.queryByRole('combobox', { name: 'appPlatform.audience' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirmUpdate' }))
    await screen.findByRole('button', { name: 'appPlatform.done' })
    expect(operations()).toHaveLength(1)
  })

  it('shows the server refusal of an update, such as a downgrade, in the modal', async () => {
    mock.post.mockRejectedValue(new Error('downgrade refused'))
    mount(installed({ version: '0.9.0', current_release_sha256: 'c'.repeat(64) }), 'update')
    await start(false)
    expect(await screen.findByText('downgrade refused')).toBeTruthy()
    expect(operations()).toHaveLength(0)
  })

  // Doc 185 section 3.2: an update that raises the data quota says so before it is confirmed, and one the runner's
  // capacity cannot hold cannot be confirmed.
  describe('an update that raises the data quota', () => {
    const quotaReview = (quota: Record<string, unknown>) => {
      const result = updateReview()
      Object.assign(result.review, { data_quota: {
        current_bytes: '268435456', current_inodes: '65536', requested_bytes: '1073741824', requested_inodes: '100000',
        new_bytes: '1073741824', new_inodes: '100000', raise_applied: true, capacity: 'fits', ...quota,
      } })
      return result
    }
    const open = async (result: ReturnType<typeof updateReview>) => {
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? result : { operation_id: operation }))
      mount(installed({ version: '1.1.0', current_release_sha256: 'c'.repeat(64), updateable: true }), 'update')
      await start(false)
      await screen.findByText('appPlatform.permissionDiff')
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
    const legacy = updateReview()
    legacy.review.previous_capabilities = null as never
    legacy.review.target_capabilities = null as never
    mock.post.mockResolvedValue(legacy)
    mount(installed({ version: '1.1.0', updateable: true }), 'update')
    await start(false)
    expect(await screen.findByText('appPlatform.permissionDiff')).toBeTruthy()
    expect(screen.getByText('appPlatform.confirmUpdate')).toBeTruthy()
  })

  it('reviews disable without describing data deletion', async () => {
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? disableReview() : { operation_id: operation }))
    mount(installed({ installed: true }), 'disable')
    await start(false)
    expect(await screen.findByText('appPlatform.confirmDisableTitle')).toBeTruthy()
    expect(screen.getByText('appPlatform.disableDataRetained')).toBeTruthy()
    expect(screen.getByText('appPlatform.disableConfirmHint')).toBeTruthy()
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'disable', installation_id: operation })
    expect(screen.queryByText('appPlatform.uninstallPluginData')).toBeNull()
  })

  it('reviews a disabled app enable without changing release or permissions', async () => {
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? enableReview() : { operation_id: operation }))
    mount(installed({ current_release_sha256: release, enableable: true }), 'enable')
    const user = await start(false)
    expect(await screen.findByText('appPlatform.enableConfirmHint')).toBeTruthy()
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'enable', installation_id: operation })
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirmEnable' }))
    await screen.findByRole('button', { name: 'appPlatform.done' })
    expect(operations()).toHaveLength(1)
  })

  it('reviews the repair of a disabled managed app instead of a frontend-only enable', async () => {
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? repairReview() : { operation_id: operation }))
    mount(installed({ current_release_sha256: release, enableable: true, backend: true }), 'enable')
    const user = await start(false)
    expect(await screen.findByText('appPlatform.confirmRepairTitle')).toBeTruthy()
    expect(screen.getByText('appPlatform.repairConfirmHint')).toBeTruthy()
    expect(screen.getByText('appPlatform.repairDataRetained')).toBeTruthy()
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'repair', installation_id: operation })
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirmRepair' }))
    await screen.findByRole('button', { name: 'appPlatform.done' })
    expect(operations()).toHaveLength(1)
  })

  it('keeps the uninstall review accessible to assistive technology', async () => {
    mock.post.mockResolvedValue(uninstallReview())
    mount(installed({ current_release_sha256: release, installed: true }), 'uninstall')
    await start(false)
    expect((await screen.findByRole('document')).getAttribute('tabindex')).toBe('0')
    const result = await axe.run(document.body, { rules: { 'color-contrast': { enabled: false }, region: { enabled: false } } })
    expect(result.violations).toEqual([])
  })

  it('keeps every step keyboard reachable and free of axe violations', async () => {
    mount()
    const user = userEvent.setup()
    const trigger = screen.getByRole('button', { name: 'start' })
    trigger.focus()
    await user.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true))
    const options = { rules: { 'color-contrast': { enabled: false }, region: { enabled: false } } }
    expect((await axe.run(dialog, options)).violations).toEqual([])
    await user.click(await screen.findByRole('button', { name: 'appPlatform.next' }))
    await screen.findByRole('document')
    expect((await axe.run(dialog, options)).violations).toEqual([])
  })

  it('requests the uninstall review close with Escape', async () => {
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? uninstallReview() : { operation_id: operation }))
    mount(installed({ current_release_sha256: release, installed: true }), 'uninstall')
    const user = userEvent.setup()
    const trigger = screen.getByRole('button', { name: 'start' })
    await user.click(trigger)
    const dialog = await screen.findByRole('dialog')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(dialog.className).toContain('ant-zoom-leave'))
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('requires a separate confirmation before uninstalling and clearing plugin data', async () => {
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? uninstallReview() : { operation_id: operation }))
    mount(installed({ current_release_sha256: release, installed: true }), 'uninstall')
    const user = await start(false)
    await screen.findByText('appPlatform.uninstallPluginData')
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'uninstall', installation_id: operation })
    expect(operations()).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: 'appPlatform.confirmUninstall' }))
    await screen.findByRole('button', { name: 'appPlatform.done' })
    expect(operations()).toHaveLength(1)
  })

  it('rejects stale preview responses across an account change and return', async () => {
    let resolve!: (value: ReturnType<typeof review>) => void
    mock.post.mockReturnValue(new Promise<ReturnType<typeof review>>(done => { resolve = done }))
    const page = mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'start' }))
    await user.click(await screen.findByRole('button', { name: 'appPlatform.next' }))
    mock.user = 'other'
    page.rerender(tree())
    mock.user = 'admin'
    page.rerender(tree())
    await act(async () => { resolve(review()) })
    await closed()
    expect(screen.queryByRole('document')).toBeNull()
  })

  it('does not transmit confirmation if the request identity cannot be saved', async () => {
    mount()
    const user = await preview()
    const storage = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('storage unavailable') })
    try {
      await user.click(screen.getByRole('button', { name: 'appPlatform.confirm' }))
      await screen.findByText('storage unavailable')
      expect(operations()).toHaveLength(0)
    } finally { storage.mockRestore() }
  })

  it('cannot confirm an expired plan', async () => {
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? { ...review(), expires_ms: String(Date.now() + 50) } : { operation_id: operation }))
    mount()
    await preview()
    await waitFor(() => expect((screen.getByRole('button', { name: 'appPlatform.confirm' }) as HTMLButtonElement).disabled).toBe(true))
    expect(operations()).toHaveLength(0)
  })

  it('can go back from the review to the audience step', async () => {
    mount()
    const user = await preview()
    await user.click(screen.getByRole('button', { name: 'appPlatform.back' }))
    expect(await screen.findByRole('combobox', { name: 'appPlatform.audience' })).toBeTruthy()
  })
})

describe('opening an installation to organizational units', () => {
  const groups = (items: unknown[]) => mock.get.mockImplementation((url: string) => Promise.resolve(url.includes('/operations/') ? finished : { items: url.includes('kind=ou') ? items : [], next_cursor: '' }))
  it('sends the chosen OU ids as open_to_ous and shows them in the review', async () => {
    groups([{ id: '7', name: 'Research' }])
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? { ...review(), review: { ...review().review, open_to_ous: ['7'] } } : { operation_id: operation }))
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'start' }))
    await user.click(await screen.findByRole('combobox', { name: 'appPlatform.audience' }))
    await user.click(await screen.findByText('Research (7)'))
    await user.click(await screen.findByRole('button', { name: 'appPlatform.next' }))
    const dialog = await screen.findByRole('dialog')
    await screen.findByRole('document')
    expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'install', release_sha256: release, trust_revision: '1', open_to_ous: ['7'] })
    expect(within(dialog).getByText('Research')).toBeTruthy()
    expect(mock.get.mock.calls.some(([url]) => String(url).includes('kind=user'))).toBe(false)
  })

  it('says that choosing the Default OU opens the application to every organizational unit', async () => {
    groups([{ id: '1', name: 'Default', default: true }, { id: '7', name: 'Research' }])
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'start' }))
    await user.click(await screen.findByRole('combobox', { name: 'appPlatform.audience' }))
    await user.click(await screen.findByText('Research (7)'))
    expect(screen.queryByText('appPlatform.defaultOUNote')).toBeNull()
    await user.click(await screen.findByText('Default (1)'))
    expect(await screen.findByText('appPlatform.defaultOUNote')).toBeTruthy()
  })

  it('does not load the OU directory for an action that needs no audience', async () => {
    mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/plans') ? disableReview() : { operation_id: operation }))
    mount(installed({ installed: true }), 'disable')
    await start(false)
    await screen.findByRole('document')
    expect(mock.get.mock.calls.some(([url]) => String(url).includes('kind=ou'))).toBe(false)
  })
})

describe('permission lists', () => {
  const item = { ...base, required_capabilities: ['background.run', 'tasks.schedule'], optional_capabilities: [] }
  it('are joined with a comma and a space in English', async () => {
    mock.lang = 'en-US'
    mount(item)
    await start(false)
    expect(await screen.findByText('background.run, tasks.schedule')).toBeTruthy()
  })
  it('are joined with the enumeration mark in Chinese', async () => {
    mount(item)
    await start(false)
    expect(await screen.findByText('background.run、tasks.schedule')).toBeTruthy()
  })
})

describe('a rejected or interrupted confirmation', () => {
  for (const [action, build, confirmName] of [
    ['update', updateReview, 'appPlatform.confirmUpdate'],
    ['disable', disableReview, 'appPlatform.confirmDisable'],
    ['enable', enableReview, 'appPlatform.confirmEnable'],
    ['uninstall', uninstallReview, 'appPlatform.confirmUninstall'],
  ] as const) {
    it(`reviews a ${action} again after the server definitively refused it, with a new plan for the same target`, async () => {
      let plans = 0
      mock.post.mockImplementation((url: string) => {
        if (url.endsWith('/plans')) { plans++; return Promise.resolve(build()) }
        return Promise.reject(new ApiError(409, 'plan changed'))
      })
      mount(installed({ current_release_sha256: release, installed: true, enableable: true, updateable: true }), action)
      const user = await start(false)
      await user.click(await screen.findByRole('button', { name: confirmName }))
      await screen.findByText('plan changed')
      await user.click(await screen.findByRole('button', { name: 'appPlatform.reviewAgain' }))
      await waitFor(() => expect(plans).toBe(2))
      expect(sessionStorage.getItem(storageKey)).toBeNull()
      expect(await screen.findByRole('button', { name: confirmName })).toBeTruthy()
      expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/plans')).map(([, body]) => body.action)).toEqual([action, action])
    })
  }

  it('remembers the action, so a flow resumed after a reload says what it is confirming', async () => {
    mock.post.mockImplementation((url: string) => url.endsWith('/plans') ? Promise.resolve(uninstallReview()) : Promise.reject(new Error('offline')))
    const first = mount(installed({ current_release_sha256: release, installed: true }), 'uninstall')
    const user = await start(false)
    await user.click(await screen.findByRole('button', { name: 'appPlatform.confirmUninstall' }))
    await screen.findByText('offline')
    expect(JSON.parse(sessionStorage.getItem(storageKey)!)).toMatchObject({ action: 'uninstall', app_id: 'notes' })
    first.unmount()
    mock.post.mockImplementation(() => Promise.resolve({ operation_id: operation }))
    mount()
    expect(await screen.findByText('appPlatform.confirmUninstallTitle')).toBeTruthy()
    const retry = screen.getByRole('button', { name: 'appPlatform.retrySame' })
    expect(retry.className).toContain('ant-btn-dangerous')
    expect(screen.queryByText('appPlatform.step.audience')).toBeNull()
    await user.click(retry)
    await screen.findByRole('button', { name: 'appPlatform.done' })
    // Only the request identity is sent, never the remembered action.
    expect(operations()[operations().length - 1][1]).toEqual({ plan_id: plan, plan_sha256: digest, idempotency_key: expect.stringMatching(/^[0-9a-f-]{36}$/) })
  })

  it('keeps the install steps for a resumed install, and reads an entry saved before the action was kept', async () => {
    sessionStorage.setItem(storageKey, JSON.stringify({ plan_id: plan, plan_sha256: digest, idempotency_key: operation, action: 'install', app_id: 'notes' }))
    const view = mount()
    expect(await screen.findByText('appPlatform.confirmTitle')).toBeTruthy()
    expect(screen.getByText('appPlatform.step.audience')).toBeTruthy()
    view.unmount()
    cleanup()
    sessionStorage.setItem(storageKey, JSON.stringify({ plan_id: plan, plan_sha256: digest, idempotency_key: operation }))
    mount()
    expect(await screen.findByRole('button', { name: 'appPlatform.retrySame' })).toBeTruthy()
  })
})

describe('the organizational unit directory', () => {
  it('discards a cursor response overtaken by a reload of the directory', async () => {
    let resolve!: (value: unknown) => void
    let fresh = false
    mock.get.mockImplementation((url: string) => url.includes('kind=ou&after=')
      ? new Promise(done => { resolve = done })
      : Promise.resolve(url.includes('kind=ou') ? { items: [{ id: fresh ? '2' : '1', name: fresh ? 'Fresh' : 'First' }], next_cursor: '1' } : url.includes('/operations/') ? finished : { items: [], next_cursor: '' }))
    const page = mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'start' }))
    await user.click(await screen.findByRole('button', { name: 'appPlatform.moreGroups' }))
    fresh = true
    mock.t = key => key
    page.rerender(tree())
    await act(async () => { resolve({ items: [{ id: '9', name: 'Stale' }], next_cursor: '' }) })
    await waitFor(() => expect((screen.getByRole('combobox', { name: 'appPlatform.audience' }) as HTMLInputElement).disabled).toBe(false))
    await user.click(screen.getByRole('combobox', { name: 'appPlatform.audience' }))
    await screen.findByText('Fresh (2)')
    expect(screen.queryByText('Stale (9)')).toBeNull()
  })
})
