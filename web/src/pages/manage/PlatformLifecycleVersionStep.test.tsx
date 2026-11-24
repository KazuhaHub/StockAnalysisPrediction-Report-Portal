import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import PlatformLifecycleModal from './PlatformLifecycleModal'
import { usePlatformLifecycle, type PickMode } from './platformLifecycle'

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: { get: mock.get, post: mock.post }, errText: (e: unknown) => (e instanceof Error ? e.message : String(e)) }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: 'admin' }) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key }) }))

const root = '/api/admin/app-platform'
const installation = '33333333-3333-4333-8333-333333333333'
const plan = '22222222-2222-4222-8222-222222222222'
const sha = (c: string) => c.repeat(64)
const catalogueUrl = 'https://market.example/index.json'
const notes = { publisher_id: 'kazuha', app_id: 'notes', publisher_trusted: true, versions: [{ version: '1.2.0', descriptor_sha256: sha('c') }, { version: '1.1.0', descriptor_sha256: sha('b') }, { version: '1.0.0', descriptor_sha256: sha('a') }] }
const catalogue = (apps: unknown[] = [notes]) => ({ catalogue_sha256: sha('9'), publisher_verified: false, apps, releases: (apps as typeof notes[]).flatMap(a => a.versions.map(v => ({ app_id: a.app_id, publisher_id: a.publisher_id, version: v.version, descriptor_sha256: v.descriptor_sha256, descriptor_url: 'https://market.example/d.json', signature_url: 'https://market.example/d.sig' }))) })
const verified = (c: string, version: string) => ({ release_sha256: sha(c), app_id: 'notes', publisher_id: 'kazuha', version, trust_revision: '1', installable: true })
const rows: Record<string, unknown> = { [sha('c')]: verified('c', '1.2.0'), [sha('b')]: verified('b', '1.1.0'), [sha('a')]: verified('a', '1.0.0') }
const planFor = (release: string) => ({ plan_id: plan, plan_sha256: sha('d'), expires_ms: String(Date.now() + 900000), review: { action: 'update', installation_id: installation, app_id: 'notes', publisher_id: 'kazuha', source_release_sha256: sha('a'), release_sha256: release, source_generation: '1', target_generation: '2', policy_revision: '1', target_policy_revision: '1', declined_capabilities: [], previous_capabilities: [], target_capabilities: [], declared_data_schema: '1', grants_sha256: sha('d') } })
const subject = { installationId: installation, appId: 'notes', publisherId: 'kazuha', name: 'Notes', version: '1.0.0' }

function Harness({ mode }: { mode: PickMode }) {
  const lifecycle = usePlatformLifecycle()
  return <>
    <button onClick={() => lifecycle.pick(subject, mode)}>open picker</button>
    <PlatformLifecycleModal lifecycle={lifecycle} />
  </>
}
const mount = (mode: PickMode = 'switch') => render(<MemoryRouter><Harness mode={mode} /></MemoryRouter>)
const marketCalls = () => [...mock.get.mock.calls, ...mock.post.mock.calls].map(([url]) => String(url)).filter(url => url.includes('/market/'))
const closed = () => waitFor(() => { const d = screen.queryByRole('dialog'); expect(!d || d.className.includes('ant-zoom-leave')).toBe(true) })

beforeEach(() => {
  mock.get.mockReset(); mock.post.mockReset()
  mock.get.mockImplementation(async (url: string) => {
    if (url === `${root}/market/sources`) return { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: true } }
    if (url.startsWith(`${root}/releases?release_sha256=`)) return { items: [rows[url.split('=')[1]]], next_cursor: '' }
    return { items: [], next_cursor: '' }
  })
  mock.post.mockImplementation(async (url: string, body: { release_sha256?: string }) => {
    if (url === `${root}/market/catalogue`) return catalogue()
    if (url === `${root}/market/stage`) return { release_sha256: body.release_sha256 }
    if (url === `${root}/plans`) return planFor(body.release_sha256 ?? sha('a'))
    throw new Error(`unexpected ${url}`)
  })
})
afterEach(cleanup)

describe('the version step of the lifecycle dialog', () => {
  it('asks the catalogue for nothing until it is opened, then shows a loading state', async () => {
    mount()
    expect(marketCalls()).toEqual([])
    let answer!: (value: unknown) => void
    mock.get.mockImplementationOnce(() => new Promise(resolve => { answer = resolve }))
    await userEvent.click(screen.getByRole('button', { name: 'open picker' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('status').textContent).toContain('appStore.versionsLoading')
    expect(within(dialog).getByRole('button', { name: 'appPlatform.next' }).hasAttribute('disabled')).toBe(true)
    answer({ official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: true } })
    expect(await within(dialog).findByRole('radio', { name: /1\.2\.0/ })).toBeTruthy()
    expect(marketCalls()).toEqual([`${root}/market/sources`, `${root}/market/catalogue`])
  })

  it('lists the versions newest first with the installed one marked and unselectable, and preselects the offered update', async () => {
    mount()
    await userEvent.click(screen.getByRole('button', { name: 'open picker' }))
    const radios = await screen.findAllByRole('radio')
    expect(radios.map(r => r.closest('label')?.textContent)).toEqual(['1.2.0appStore.latest', '1.1.0', '1.0.0appStore.current'])
    expect((radios[0] as HTMLInputElement).checked).toBe(true)
    expect((radios[2] as HTMLInputElement).disabled).toBe(true)
    expect(screen.getByRole('button', { name: 'appPlatform.next' }).hasAttribute('disabled')).toBe(false)
  })

  it('offers only newer versions when it is an update, and says so when there are none', async () => {
    mount('update')
    await userEvent.click(screen.getByRole('button', { name: 'open picker' }))
    expect((await screen.findAllByRole('radio')).map(r => r.closest('label')?.textContent)).toEqual(['1.2.0appStore.latest', '1.1.0'])
    cleanup()
    mock.post.mockImplementation(async (url: string) => url === `${root}/market/catalogue` ? catalogue([{ ...notes, versions: [{ version: '1.0.0', descriptor_sha256: sha('a') }] }]) : { release_sha256: sha('a') })
    mount('update')
    await userEvent.click(screen.getByRole('button', { name: 'open picker' }))
    expect(await screen.findByText('appStore.upToDate')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'appPlatform.next' }).hasAttribute('disabled')).toBe(true)
  })

  it('goes on to the review of the chosen version inside the same dialog, with the version as its first step', async () => {
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'open picker' }))
    await user.click(await screen.findByRole('radio', { name: /1\.1\.0/ }))
    await user.click(screen.getByRole('button', { name: 'appPlatform.next' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(`${root}/plans`, expect.objectContaining({ action: 'update', installation_id: installation, release_sha256: sha('b') })))
    expect(await screen.findByRole('document')).toBeTruthy()
    expect(screen.getAllByRole('dialog')).toHaveLength(1)
    expect(document.querySelectorAll('.ant-steps-item').length).toBe(3)
    expect(screen.getByText('appPlatform.step.version')).toBeTruthy()
    // Back returns to the version list without asking the catalogue again.
    const before = marketCalls().length
    await user.click(screen.getByRole('button', { name: 'appPlatform.back' }))
    expect(await screen.findByRole('radio', { name: /1\.1\.0/ })).toBeTruthy()
    expect(marketCalls().length).toBe(before)
  })

  it('tells a failed catalogue read, and reads it again on Retry', async () => {
    let calls = 0
    mock.post.mockImplementation(async (url: string) => { if (url === `${root}/market/catalogue`) { if (++calls === 1) throw new Error('catalogue down'); return catalogue() } throw new Error(`unexpected ${url}`) })
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'open picker' }))
    expect(await screen.findByText('catalogue down')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'common.retry' }))
    expect(await screen.findByRole('radio', { name: /1\.2\.0/ })).toBeTruthy()
    expect(calls).toBe(2)
  })

  it('tells a failed source read, and reads the sources again on Retry', async () => {
    let calls = 0
    mock.get.mockImplementation(async (url: string) => { if (url === `${root}/market/sources`) { if (++calls === 1) throw new Error('sources down'); return { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: true } } } return { initialized: true, items: [] } })
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'open picker' }))
    expect(await screen.findByText('appStore.sourcesFailed')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'common.retry' }))
    expect(await screen.findByRole('radio', { name: /1\.2\.0/ })).toBeTruthy()
  })

  it('says so when no catalogue is on or the app is not listed in it', async () => {
    mock.get.mockImplementation(async (url: string) => url === `${root}/market/sources` ? { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: false } } : { items: [] })
    mount()
    await userEvent.click(screen.getByRole('button', { name: 'open picker' }))
    expect(await screen.findByText('appStore.sourceNone')).toBeTruthy()
    cleanup()
    mock.get.mockImplementation(async (url: string) => url === `${root}/market/sources` ? { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: true } } : { items: [] })
    mock.post.mockImplementation(async () => catalogue([{ ...notes, app_id: 'other' }]))
    mount()
    await userEvent.click(screen.getByRole('button', { name: 'open picker' }))
    expect(await screen.findByText('appStore.versionsNotListed')).toBeTruthy()
  })

  it('shows the refusal of a download in the dialog and stays on the version step', async () => {
    mock.post.mockImplementation(async (url: string, body: { release_sha256?: string }) => {
      if (url === `${root}/market/catalogue`) return catalogue()
      if (url === `${root}/market/stage`) throw new Error('signature rejected')
      throw new Error(`unexpected ${url} ${body.release_sha256}`)
    })
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'open picker' }))
    await screen.findByRole('radio', { name: /1\.2\.0/ })
    await user.click(screen.getByRole('button', { name: 'appPlatform.next' }))
    expect(await screen.findByText('signature rejected')).toBeTruthy()
    expect(screen.getByRole('radio', { name: /1\.2\.0/ })).toBeTruthy()
  })

  it('closes on Cancel without asking for anything more', async () => {
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'open picker' }))
    await screen.findByRole('radio', { name: /1\.2\.0/ })
    const before = marketCalls().length
    await user.click(screen.getByRole('button', { name: 'appPlatform.cancel' }))
    await closed()
    expect(marketCalls().length).toBe(before)
    expect(mock.post).not.toHaveBeenCalledWith(`${root}/plans`, expect.anything())
  })
})
