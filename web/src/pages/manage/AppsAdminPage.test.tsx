import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter, Route, Routes, useParams } from 'react-router'
import AppsAdminPage from './AppsAdminPage'
import { layout } from './appsAdminFixtures'
import { DESCRIPTOR_LOCALES } from '../../lib/descriptor-contract.generated'
import type { AppLayoutEntry } from '../../api/types'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), postBytes: vi.fn() }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: apiMock, errText: (e: unknown) => e instanceof Error && e.message !== 'boom' ? e.message : 'Request failed' }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook, initReactI18next: { type: '3rdParty', init: () => undefined } }
})
const auth = vi.hoisted(() => ({ user: 'admin' }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: auth.user }) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
// Dragging itself is dnd.test.tsx's concern. This stand-in exposes the wrapper's contract, a reordered
// id sequence, through a button, so the test can assert what the page persists.
vi.mock('./dnd', () => ({
  SortableWrapper: ({ ids, onReorder, children }: { ids: string[]; onReorder: (ids: string[]) => void; children: React.ReactNode }) => (
    <div>
      <button onClick={() => onReorder([...ids].reverse())}>reverse</button>
      {children}
    </div>
  ),
  SortableItem: ({ id, children }: { id: string; children: React.ReactNode }) => <div data-sortable-id={id}>{children}</div>,
  DragHandle: ({ label }: { label?: string }) => <span aria-label={label} />,
}))

function Detail() {
  return <div>detail:{useParams().key}</div>
}
const pageTree = (path = '/manage/apps') => (
    <App>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/manage/apps" element={<AppsAdminPage />} />
          <Route path="/manage/apps/:key" element={<Detail />} />
        </Routes>
      </MemoryRouter>
    </App>
)
const renderPage = (path = '/manage/apps') => render(pageTree(path))


const root = '/api/admin/app-platform'
const installationId = '33333333-3333-4333-8333-333333333333'
const plan = '22222222-2222-4222-8222-222222222222'
const operation = '11111111-1111-4111-8111-111111111111'
const planSha = 'd'.repeat(64)
const sha = (c: string) => c.repeat(64)
const catalogueUrl = 'https://market.example/index.json'
const display = (name: string, description: string) => ({ icon: 'file-text', texts: DESCRIPTOR_LOCALES.map(locale => ({ locale, name, description })) })
const notesApp = { publisher_id: 'kazuha', app_id: 'notes', versions: [{ version: '1.1.0', descriptor_sha256: sha('b') }, { version: '1.0.0', descriptor_sha256: sha('a') }], display: display('Notes', 'Personal notes'), publisher_trusted: true }
const chartsApp = { publisher_id: 'kazuha', app_id: 'charts', versions: [{ version: '2.0.0', descriptor_sha256: sha('c') }, { version: '2.0.0-beta.1', descriptor_sha256: sha('e') }], display: display('Charts', 'Draw charts'), publisher_trusted: true }
const strangerApp = { publisher_id: 'stranger', app_id: 'tools', versions: [{ version: '0.1.0', descriptor_sha256: sha('f') }], publisher_trusted: false }
const catalogue = (apps: unknown[] = [notesApp, chartsApp, strangerApp]) => ({ catalogue_sha256: sha('9'), publisher_verified: false, apps, releases: (apps as typeof notesApp[]).flatMap(a => a.versions.map(v => ({ app_id: a.app_id, publisher_id: a.publisher_id, version: v.version, descriptor_sha256: v.descriptor_sha256, descriptor_url: 'https://market.example/d.json', signature_url: 'https://market.example/d.sig' }))) })
const platformEntry = (state: string, version = '1.0.0'): AppLayoutEntry => ({ key: `platform:${installationId}`, kind: 'platform', name: 'Notes', builtin: false, icon: 'file-text', installation_id: installationId, app_id: 'notes', publisher_id: 'kazuha', state, version, access: [] })
const verifiedRow = (c: string, version: string, extra: Record<string, unknown> = {}) => ({ release_sha256: sha(c), app_id: c === 'b' || c === 'a' ? 'notes' : 'charts', publisher_id: 'kazuha', version, trust_revision: '1', installable: true, display: display('Notes', 'Personal notes'), ...extra })
const planFor = (action: string, release: string, app = 'notes') => ({ plan_id: plan, plan_sha256: planSha, expires_ms: String(Date.now() + 900000), review: action === 'install'
  ? { action, app_id: app, publisher_id: 'kazuha', release_sha256: release, declined_capabilities: [], open_to_ous: [] }
  : { action, installation_id: installationId, app_id: app, publisher_id: 'kazuha', release_sha256: release, generation: '1', target_generation: '2', source_generation: '1', managed: false, data_effect: 'preserve', source_release_sha256: sha('a'), policy_revision: '1', target_policy_revision: '1', declined_capabilities: [], previous_capabilities: [], target_capabilities: [], declared_data_schema: '1', grants_sha256: planSha, plugin_data_cleared: true, managed_data_cleared: false } })

let entries: AppLayoutEntry[]
let catalogueResponse: () => unknown
let releaseRows: Record<string, Record<string, unknown>>
beforeEach(() => {
  auth.user = 'admin'
  entries = layout()
  catalogueResponse = () => catalogue()
  releaseRows = { [sha('b')]: verifiedRow('b', '1.1.0'), [sha('a')]: verifiedRow('a', '1.0.0', { installation_id: installationId, installed: true, installable: false }), [sha('c')]: verifiedRow('c', '2.0.0') }
  for (const m of Object.values(apiMock)) m.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/apps/layout') return { apps: entries }
    if (url === `${root}/market/sources`) return { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: true } }
    if (url === `${root}/storage-status`) return {}
    if (url === `${root}/subjects?kind=ou`) return { items: [{ id: '7', name: 'Research' }], next_cursor: '' }
    if (url === `${root}/installations/${installationId}/settings`) return { current_release_sha256: sha('a'), version: '1.0.0' }
    if (url.startsWith(`${root}/releases?release_sha256=`)) { const row = releaseRows[url.split('=')[1]]; return { items: row ? [row] : [], next_cursor: '' } }
    if (url.startsWith(`${root}/operations/`)) return { operation_id: operation, installation_id: installationId, action: 'install', stage: 'queued', terminal: false, control_paused: false }
    return { items: [], next_cursor: '' }
  })
  apiMock.post.mockImplementation(async (url: string, body: { action?: string; release_sha256?: string }) => {
    if (url === `${root}/market/catalogue`) return catalogueResponse()
    if (url === `${root}/market/stage`) return { release_sha256: body.release_sha256 }
    if (url === `${root}/plans`) return planFor(body.action!, body.release_sha256 ?? sha('a'))
    if (url === `${root}/operations`) return { operation_id: operation }
    throw new Error(`unexpected ${url}`)
  })
  apiMock.put.mockResolvedValue({ apps: layout() })
})
afterEach(() => vi.unstubAllGlobals())

const cardOf = (name: string) => screen.getAllByText(name).map(n => n.closest('.rp-app-card') as HTMLElement).find(Boolean) as HTMLElement
const withPlatform = (state: string, version = '1.0.0') => { entries = [...layout(), platformEntry(state, version)] }

describe('Installed apps', () => {
  it('lists every app as a card with where it is switched on, built-ins marked', async () => {
    renderPage()
    await screen.findByText('nav.batch')
    expect(within(cardOf('nav.batch')).getByText('apps.status.all')).toBeTruthy()
    expect(within(cardOf('nav.quotes')).getByText('apps.status.some:{"open":1,"total":2}')).toBeTruthy()
    expect(within(cardOf('Hello')).getByText('apps.status.none')).toBeTruthy()
    expect(within(cardOf('nav.batch')).getByText('appStore.builtinBadge')).toBeTruthy()
    expect(within(cardOf('Hello')).queryByText('appStore.builtinBadge')).toBeNull()
    expect(cardOf('nav.batch').querySelector('[data-icon="play-circle"]')).not.toBeNull()
    expect(within(cardOf('Hello')).getByText('1.2.0')).toBeTruthy()
  })

  it('shows one page: no segmented switch, no preview or unverified notice, no separate platform tab', async () => {
    renderPage()
    await screen.findByText('nav.batch')
    expect(screen.queryByText('appPlatform.development')).toBeNull()
    expect(screen.queryByText('appMarket.unverified')).toBeNull()
    expect(screen.queryByRole('tab', { name: 'appPlatform.tab' })).toBeNull()
    expect(screen.getByRole('tab', { name: 'appTrust.tab' })).toBeTruthy()
    expect(screen.getByText(/appStore\.fromCatalogue/)).toBeTruthy()
  })

  it('opens the app page from the card and offers the name as a link', async () => {
    const user = userEvent.setup()
    renderPage()
    const link = (await screen.findByRole('link', { name: /nav\.quotes/ })) as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('/manage/apps/builtin%3Aquotes')
    await user.click(link)
    expect(await screen.findByText('detail:builtin:quotes')).toBeTruthy()
  })

  it('filters both lists by the search box', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('nav.batch')
    await screen.findByText('Notes')
    await user.type(screen.getByRole('searchbox', { name: 'apps.search' }), 'hel')
    expect(screen.queryByText('nav.batch')).toBeNull()
    expect(screen.queryByText('Notes')).toBeNull()
    expect(screen.getByText('Hello')).toBeTruthy()
  })

  it('says the load failed instead of claiming there are no apps', async () => {
    apiMock.get.mockRejectedValue(new Error('boom'))
    renderPage()
    expect(await screen.findByText('Request failed')).toBeTruthy()
    expect(screen.queryByText('apps.empty')).toBeNull()
  })

  it('persists a reordering only when Done is pressed, and discards it on Cancel', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'apps.reorder' }))
    await user.click(await screen.findByRole('button', { name: 'reverse' }))
    expect(apiMock.put).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'common.done' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/apps/order', { keys: ['builtin:quotes', 'builtin:batch', 'hello'] }))
  })
})

describe('A platform app card', () => {
  it('shows its state as a status, its version and who can use it', async () => {
    withPlatform('installed')
    renderPage()
    const card = (await screen.findAllByText('Notes')).map(n => n.closest('.rp-app-card') as HTMLElement).find(c => c.querySelector('[data-icon]') && within(c).queryByText('appStore.state.enabled'))!
    expect(within(card).getByText('appStore.state.enabled')).toBeTruthy()
    expect(within(card).getByText('1.0.0')).toBeTruthy()
  })

  it('maps known states and shows an unknown one as it is', async () => {
    for (const [state, text] of [['disabled', 'appStore.state.disabled'], ['pending', 'appStore.state.installing'], ['attention', 'appStore.state.recovery'], ['odd_state', 'odd_state']]) {
      withPlatform(state)
      const view = renderPage()
      expect(await screen.findByText(text)).toBeTruthy()
      view.unmount()
    }
  })

  it('offers an update only when the catalogue lists a newer version, and reviews it through the update plan', async () => {
    withPlatform('installed')
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: catalogueUrl, release_sha256: sha('b') }))
    expect(apiMock.get).toHaveBeenCalledWith(`${root}/releases?release_sha256=${sha('b')}`)
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'update', release_sha256: sha('b'), trust_revision: '1', installation_id: installationId }))
    expect(await screen.findByText('appPlatform.confirmUpdate')).toBeTruthy()
  })

  it('shows no update when the installed version is the newest', async () => {
    withPlatform('installed', '1.1.0')
    renderPage()
    await screen.findByText('nav.batch')
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, expect.anything()))
    expect(screen.queryByRole('button', { name: /appStore\.updateTo/ })).toBeNull()
  })

  it('lists the catalogue versions newest first with the current one marked, and switches through the update plan', async () => {
    withPlatform('installed')
    const user = userEvent.setup()
    renderPage()
    await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' })
    await user.click(screen.getAllByRole('button', { name: 'appStore.switchVersion' })[0])
    const items = await screen.findAllByRole('menuitem')
    expect(items.map(i => i.textContent)).toEqual(['1.1.0', '1.0.0appStore.current'])
    expect(items[1].getAttribute('aria-disabled')).toBe('true')
    await user.click(items[0])
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, expect.objectContaining({ action: 'update', installation_id: installationId })))
  })

  it('does not hide an older version from the switch menu: the server decides, and its refusal is shown', async () => {
    withPlatform('installed', '1.1.0')
    apiMock.post.mockImplementation(async (url: string, body: { release_sha256?: string }) => {
      if (url === `${root}/market/catalogue`) return catalogue()
      if (url === `${root}/market/stage`) return { release_sha256: body.release_sha256 }
      if (url === `${root}/plans`) throw new Error('downgrade refused')
      throw new Error(`unexpected ${url}`)
    })
    releaseRows[sha('a')] = verifiedRow('a', '1.0.0', { installation_id: installationId, installable: false })
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('nav.batch')
    await user.click((await screen.findAllByRole('button', { name: 'appStore.switchVersion' }))[0])
    await user.click(await screen.findByText('1.0.0'))
    expect(await screen.findByText('downgrade refused')).toBeTruthy()
  })

  it('disables from the menu after reading the installation’s own release', async () => {
    withPlatform('installed')
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: entries }
      if (url === `${root}/installations/${installationId}/settings`) return { current_release_sha256: sha('a') }
      if (url.startsWith(`${root}/releases?release_sha256=`)) return { items: [releaseRows[sha('a')]], next_cursor: '' }
      if (url === `${root}/market/sources`) return { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: false } }
      return { items: [], next_cursor: '' }
    })
    const user = userEvent.setup()
    renderPage()
    await user.click((await screen.findAllByRole('button', { name: 'appStore.more' }))[0])
    await user.click(await screen.findByRole('menuitem', { name: 'appStore.disable' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'disable', installation_id: installationId }))
    expect(await screen.findByText('appPlatform.confirmDisableTitle')).toBeTruthy()
  })

  it('offers enable for a disabled app and uninstall in danger style', async () => {
    withPlatform('disabled')
    const user = userEvent.setup()
    renderPage()
    await user.click((await screen.findAllByRole('button', { name: 'appStore.more' }))[0])
    expect(await screen.findByRole('menuitem', { name: 'appStore.enable' })).toBeTruthy()
    expect(screen.queryByRole('menuitem', { name: 'appStore.disable' })).toBeNull()
    expect(screen.getByRole('menuitem', { name: 'appStore.uninstall' }).className).toContain('danger')
  })

  it('offers nothing beyond Manage for an app that needs recovery, unless its release can be uninstalled', async () => {
    withPlatform('attention')
    const user0 = userEvent.setup()
    const view = renderPage()
    await screen.findByText('appStore.state.recovery')
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith(`${root}/releases?release_sha256=${sha('a')}`))
    await user0.click(await screen.findByRole('button', { name: 'appStore.more' }))
    expect(await screen.findByRole('menuitem', { name: 'appStore.viewInCatalogue' })).toBeTruthy()
    expect(screen.queryByRole('menuitem', { name: 'appStore.uninstall' })).toBeNull()
    expect(screen.queryByRole('menuitem', { name: 'appStore.disable' })).toBeNull()
    view.unmount()
    releaseRows[sha('a')] = verifiedRow('a', '1.0.0', { installation_id: installationId, installable: false, uninstallable: true })
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'appStore.more' }))
    expect(await screen.findByRole('menuitem', { name: 'appStore.uninstall' })).toBeTruthy()
    expect(screen.queryByRole('menuitem', { name: 'appStore.disable' })).toBeNull()
    await user.click(screen.getByRole('menuitem', { name: 'appStore.uninstall' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'uninstall', installation_id: installationId }))
  })

  it('views the app in the catalogue from the menu', async () => {
    withPlatform('installed')
    const user = userEvent.setup()
    renderPage()
    await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' })
    await user.click((await screen.findAllByRole('button', { name: 'appStore.more' }))[0])
    await user.click(await screen.findByRole('menuitem', { name: 'appStore.viewInCatalogue' }))
    expect(await screen.findByRole('dialog')).toBeTruthy()
  })
})

describe('Install from the catalogue', () => {
  it('shows only apps that are not installed, once per app, loaded automatically', async () => {
    withPlatform('installed')
    renderPage()
    await screen.findByText('Charts')
    expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, { catalogue_url: catalogueUrl })
    expect(screen.getByRole('button', { name: 'Charts' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Notes' })).toBeNull()
    expect(screen.getAllByRole('button', { name: 'Charts' })).toHaveLength(1)
  })

  it('opens a detail dialog with the version choice defaulting to the newest, the version list and the signature note', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Draw charts')).toBeTruthy()
    expect(within(dialog).getAllByText('appStore.latest').length).toBeGreaterThan(0)
    expect(within(dialog).getByText('2.0.0-beta.1')).toBeTruthy()
    expect(within(dialog).getByText('appStore.signatureCheck')).toBeTruthy()
    expect(within(dialog).queryByText('appMarket.unverified')).toBeNull()
  })

  it('downloads and verifies the chosen version, then opens the install steps', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    await user.click(await within(await screen.findByRole('dialog')).findByRole('button', { name: 'appStore.installAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: catalogueUrl, release_sha256: sha('c') }))
    expect(apiMock.get).toHaveBeenCalledWith(`${root}/releases?release_sha256=${sha('c')}`)
    expect(await screen.findByText('appPlatform.step.audience')).toBeTruthy()
    expect(apiMock.post.mock.calls.filter(([url]) => url === `${root}/plans`)).toHaveLength(0)
  })

  it('installs an older version when it is chosen in the dialog', async () => {
    releaseRows[sha('e')] = verifiedRow('e', '2.0.0-beta.1')
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('combobox', { name: 'appStore.versions' }))
    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find(o => o.textContent?.includes('2.0.0-beta.1'))!
    await user.click(option)
    await user.click(within(dialog).getByRole('button', { name: 'appStore.installAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: catalogueUrl, release_sha256: sha('e') }))
  })

  it('refuses a verified release that is not the one asked for', async () => {
    releaseRows[sha('c')] = verifiedRow('c', '9.9.9')
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    await user.click(await within(await screen.findByRole('dialog')).findByRole('button', { name: 'appStore.installAction' }))
    expect(await screen.findByText('appMarket.verificationChanged')).toBeTruthy()
    expect(screen.queryByText('appPlatform.step.audience')).toBeNull()
  })

  it('does not offer to install from an untrusted publisher, and points to sources and trust', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'tools' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('appStore.untrusted')).toBeTruthy()
    expect((within(dialog).getByRole('button', { name: 'appStore.installAction' }) as HTMLButtonElement).disabled).toBe(true)
    await user.click(within(dialog).getByRole('button', { name: 'appStore.openTrust' }))
    await waitFor(() => expect(screen.getByRole('tab', { name: 'appTrust.tab' }).getAttribute('aria-selected')).toBe('true'))
  })

  it('shows a failed catalogue as an error with a retry, never as an empty catalogue', async () => {
    catalogueResponse = () => { throw new Error('catalogue down') }
    renderPage()
    expect(await screen.findByText('catalogue down')).toBeTruthy()
    expect(screen.queryByText('appStore.catalogueEmpty')).toBeNull()
    expect(screen.getByRole('button', { name: 'appStore.retry' })).toBeTruthy()
  })
})

describe('Install app menu', () => {
  it('opens a custom catalogue and lets the source switch return to the official one', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Charts')
    await user.click(screen.getByRole('button', { name: /appStore\.install/ }))
    await user.click(await screen.findByRole('menuitem', { name: /appStore\.install\.custom/ }))
    const input = await screen.findByRole('textbox', { name: 'appStore.customPrompt' })
    await user.type(input, 'http://insecure.example/index.json')
    expect((screen.getByRole('button', { name: 'appStore.customOpen' }) as HTMLButtonElement).disabled).toBe(true)
    await user.clear(input)
    await user.type(input, 'https://custom.example/index.json')
    await user.click(screen.getByRole('button', { name: 'appStore.customOpen' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, { catalogue_url: 'https://custom.example/index.json' }))
    expect(screen.getByRole('combobox', { name: 'appStore.source' })).toBeTruthy()
  })

  it('stages an offline package and opens the install steps for it', async () => {
    apiMock.postBytes.mockResolvedValue({ release_sha256: sha('c') })
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Charts')
    const file = new File(['zip'], 'charts.portalapp')
    await user.upload(screen.getByTestId('offline-package'), file)
    await waitFor(() => expect(apiMock.postBytes).toHaveBeenCalledWith(`${root}/market/offline`, file, 'application/zip'))
    expect(await screen.findByText('appPlatform.step.audience')).toBeTruthy()
  })

  it('reports a refused offline package without opening anything', async () => {
    apiMock.postBytes.mockRejectedValue(new Error('package refused'))
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Charts')
    await user.upload(screen.getByTestId('offline-package'), new File(['zip'], 'bad.portalapp'))
    await waitFor(() => expect(apiMock.postBytes).toHaveBeenCalled())
    expect(screen.queryByText('appPlatform.step.audience')).toBeNull()
  })
})

describe('Deep links', () => {
  it('lands on the marketplace and resumes an operation in the modal', async () => {
    renderPage(`/manage/apps?operation=${operation}`)
    expect(screen.getByRole('tab', { name: 'nav.apps' }).getAttribute('aria-selected')).toBe('true')
    expect(await screen.findByText(/appPlatform\.stage\.queued/)).toBeTruthy()
    expect(screen.getByText('appPlatform.step.progress')).toBeTruthy()
  })

  it('lands on the marketplace for an installation link', async () => {
    renderPage(`/manage/apps?installation=${installationId}`)
    expect(screen.getByRole('tab', { name: 'nav.apps' }).getAttribute('aria-selected')).toBe('true')
    await screen.findByText('nav.batch')
  })
})

describe('Sources and trust', () => {
  it('holds the official catalogue switch and the image storage status', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('tab', { name: 'appTrust.tab' }))
    const toggle = await screen.findByRole('switch', { name: 'appMarket.officialEnabled' })
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    apiMock.post.mockResolvedValue({})
    await user.click(toggle)
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/sources`, { official_enabled: false }))
  })
})

describe('Prerelease policy and publisher identity', () => {
  const versioned = (versions: string[], extra: Record<string, unknown> = {}) => ({ ...chartsApp, ...extra, versions: versions.map((version, i) => ({ version, descriptor_sha256: sha('0123456789abcdef'[i])})) })

  it('offers the newest stable version by default and marks it latest, while listing everything newest first', async () => {
    catalogueResponse = () => catalogue([versioned(['3.0.0-beta.1', '2.0.0', '1.0.0'])])
    releaseRows[sha('1')] = verifiedRow('1', '2.0.0')
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getAllByRole('listitem').map(i => i.textContent?.replace('appStore.latest', '').trim())).toEqual(['3.0.0-beta.1', '2.0.0', '1.0.0'])
    await user.click(within(dialog).getByRole('button', { name: 'appStore.installAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: catalogueUrl, release_sha256: sha('1') }))
  })

  it('defaults to the newest prerelease when there is no stable version', async () => {
    catalogueResponse = () => catalogue([versioned(['0.1.0-beta.10', '0.1.0-beta.2'])])
    releaseRows[sha('0')] = verifiedRow('0', '0.1.0-beta.10')
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'appStore.installAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: catalogueUrl, release_sha256: sha('0') }))
  })

  it('shows the plain version in the select, without a stray prefix', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    const dialog = await screen.findByRole('dialog')
    const shown = dialog.querySelector('.ant-select')?.textContent ?? ''
    expect(shown).toContain('2.0.0')
    expect(shown).not.toMatch(/v\s*2\.0\.0/)
  })

  it('does not nudge a stable install toward a prerelease', async () => {
    withPlatform('installed', '1.0.0')
    catalogueResponse = () => catalogue([{ ...notesApp, versions: [{ version: '2.0.0-beta.1', descriptor_sha256: sha('e') }, { version: '1.0.0', descriptor_sha256: sha('a') }] }])
    renderPage()
    await screen.findByText('nav.batch')
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, expect.anything()))
    expect(screen.queryByRole('button', { name: /appStore\.updateTo/ })).toBeNull()
  })

  it('offers a prerelease install the newer prerelease', async () => {
    withPlatform('installed', '1.0.0-beta.1')
    catalogueResponse = () => catalogue([{ ...notesApp, versions: [{ version: '1.0.0-beta.2', descriptor_sha256: sha('e') }, { version: '1.0.0-beta.1', descriptor_sha256: sha('a') }] }])
    renderPage()
    expect(await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.0.0-beta.2"}' })).toBeTruthy()
  })

  it('matches an installed card to the catalogue by publisher and app id, never by app id alone', async () => {
    withPlatform('installed', '1.0.0')
    const imposter = { ...notesApp, publisher_id: 'imposter', versions: [{ version: '9.0.0', descriptor_sha256: sha('9') }], display: display('Imposter Notes', 'Not yours') }
    catalogueResponse = () => catalogue([imposter, notesApp])
    renderPage()
    // The other publisher's app is a different app: it is offered for install, and gives the installed card nothing.
    expect(await screen.findByRole('button', { name: 'Imposter Notes' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /appStore\.updateTo:\{"version":"9/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Notes' })).toBeNull()
  })

  it('gives an installation with no publisher in its layout entry no catalogue match', async () => {
    entries = [...layout(), { ...platformEntry('installed'), publisher_id: undefined }]
    renderPage()
    await screen.findByRole('button', { name: 'Notes' })
    expect(screen.queryByRole('button', { name: /appStore\.updateTo/ })).toBeNull()
  })
})

describe('A prerelease install', () => {
  const prereleaseCatalogue = () => catalogue([{ ...notesApp, versions: [{ version: '1.0.0-beta.2', descriptor_sha256: sha('e') }, { version: '0.9.0', descriptor_sha256: sha('7') }, { version: '1.0.0-beta.1', descriptor_sha256: sha('a') }] }])
  it('is offered the newer prerelease, and the dialog opens on it', async () => {
    withPlatform('installed', '1.0.0-beta.1')
    catalogueResponse = prereleaseCatalogue
    const user = userEvent.setup()
    renderPage()
    await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.0.0-beta.2"}' })
    await user.click((await screen.findAllByRole('button', { name: 'appStore.more' }))[0])
    await user.click(await screen.findByRole('menuitem', { name: 'appStore.viewInCatalogue' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('button', { name: 'appStore.updateTo:{"version":"1.0.0-beta.2"}' })).toBeTruthy()
  })
  it('opens the dialog on the installed version when nothing newer is offered', async () => {
    withPlatform('installed', '1.1.0')
    const user = userEvent.setup()
    renderPage()
    await user.click((await screen.findAllByRole('button', { name: 'appStore.more' }))[0])
    await user.click(await screen.findByRole('menuitem', { name: 'appStore.viewInCatalogue' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('appStore.alreadyInstalled')).toBeTruthy()
  })
})

describe('The catalogue dialog of an installed app', () => {
  const openDialog = async () => {
    withPlatform('installed', '1.0.0')
    const user = userEvent.setup()
    renderPage()
    await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' })
    await user.click((await screen.findAllByRole('button', { name: 'appStore.more' }))[0])
    await user.click(await screen.findByRole('menuitem', { name: 'appStore.viewInCatalogue' }))
    return { user, dialog: await screen.findByRole('dialog') }
  }

  it('offers to update to a newer version rather than to install', async () => {
    const { user, dialog } = await openDialog()
    expect(within(dialog).queryByRole('button', { name: 'appStore.installAction' })).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'update', release_sha256: sha('b'), trust_revision: '1', installation_id: installationId }))
  })

  it('offers to switch to an older version and hides the button for the installed one', async () => {
    const { user, dialog } = await openDialog()
    await user.click(within(dialog).getByRole('combobox', { name: 'appStore.versions' }))
    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find(o => o.textContent === '1.0.0')!
    await user.click(option)
    expect(within(dialog).queryByRole('button', { name: /appStore\.(installAction|updateTo|switchTo)/ })).toBeNull()
    expect(within(dialog).getByText('appStore.alreadyInstalled')).toBeTruthy()
  })

  it('reviews an enable when the staged release is the disabled installation’s own', async () => {
    releaseRows[sha('b')] = verifiedRow('b', '1.1.0', { installable: false, installation_id: installationId, enableable: true })
    const { user, dialog } = await openDialog()
    await user.click(within(dialog).getByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'enable', installation_id: installationId }))
  })

  it('reviews an uninstall when the staged release is one that can only be uninstalled', async () => {
    releaseRows[sha('b')] = verifiedRow('b', '1.1.0', { installable: false, installation_id: installationId, uninstallable: true })
    const { user, dialog } = await openDialog()
    await user.click(within(dialog).getByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'uninstall', installation_id: installationId }))
  })

  it('says so instead of planning an update when the staged release is the installed one', async () => {
    releaseRows[sha('b')] = verifiedRow('b', '1.1.0', { installable: false, installation_id: installationId, installed: true })
    const { user, dialog } = await openDialog()
    await user.click(within(dialog).getByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' }))
    expect(await within(dialog).findByText('appStore.alreadyInstalled')).toBeTruthy()
    expect(apiMock.post.mock.calls.filter(([url]) => url === `${root}/plans`)).toHaveLength(0)
  })
})

describe('Platform not initialised', () => {
  it('says the platform is not set up and points to sources and trust, instead of claiming no catalogue exists', async () => {
    const { ApiError } = await import('../../api/client')
    const base = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === `${root}/market/sources`) throw new ApiError(503, 'unavailable', 'app_platform_unavailable')
      if (url === `${root}/publishers`) return { initialized: false, paused: false }
      return base(url)
    })
    const user = userEvent.setup()
    renderPage()
    expect(await screen.findByText('appStore.platformNotReady')).toBeTruthy()
    expect(screen.queryByText('appStore.sourceNone')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'appStore.openTrust' }))
    await waitFor(() => expect(screen.getByRole('tab', { name: 'appTrust.tab' }).getAttribute('aria-selected')).toBe('true'))
  })
})

describe('Sources that cannot be read', () => {
  it('reports a 503 as a failed read, not as an uninitialised platform, when the platform is initialised', async () => {
    const { ApiError } = await import('../../api/client')
    const base = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === `${root}/market/sources`) throw new ApiError(503, 'unavailable', 'app_platform_unavailable')
      if (url === `${root}/publishers`) return { initialized: true, paused: false }
      return base(url)
    })
    renderPage()
    expect(await screen.findByText('appStore.sourcesFailed')).toBeTruthy()
    expect(screen.queryByText('appStore.platformNotReady')).toBeNull()
  })
})

describe('Source list', () => {
  it('follows the official catalogue switch on Sources and trust without a remount', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Charts')
    let official = true
    const base = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string) => url === `${root}/market/sources` ? { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: official } } : base(url))
    await user.click(screen.getByRole('tab', { name: 'appTrust.tab' }))
    official = false
    await user.click(screen.getByRole('tab', { name: 'nav.apps' }))
    expect(await screen.findByText('appStore.sourceNone')).toBeTruthy()
  })
})

describe('A different account', () => {
  it('does not open a lifecycle flow that an earlier account started', async () => {
    withPlatform('installed')
    let release!: (value: unknown) => void
    const base = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation((url: string) => url === `${root}/installations/${installationId}/settings` ? new Promise(done => { release = done }) : base(url))
    const user = userEvent.setup()
    const view = renderPage()
    await user.click((await screen.findAllByRole('button', { name: 'appStore.more' }))[0])
    await user.click(await screen.findByRole('menuitem', { name: 'appStore.disable' }))
    auth.user = 'other'
    view.rerender(pageTree())
    await act(async () => { release({ current_release_sha256: sha('a') }) })
    await new Promise(r => setTimeout(r, 50))
    expect(apiMock.post.mock.calls.filter(([url]) => url === `${root}/plans`)).toHaveLength(0)
  })
})
