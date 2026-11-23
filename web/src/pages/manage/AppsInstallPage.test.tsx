import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter, Route, Routes, useParams } from 'react-router'
import AppsAdminPage from './AppsAdminPage'
import AppsInstallPage from './AppsInstallPage'
import { layout } from './appsAdminFixtures'
import { cardOf, catalogue, chartsApp, display, fx, installationId, notesApp, operation, platformEntry, resetFixtures, root, sha, verifiedRow, withPlatform, catalogueUrl } from './appsConsoleFixtures'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), postBytes: vi.fn() }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: apiMock, errText: (e: unknown) => e instanceof Error && e.message !== 'boom' ? e.message : 'Request failed' }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook, initReactI18next: { type: '3rdParty', init: () => undefined } }
})
const auth = vi.hoisted(() => ({ user: 'admin' }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: auth.user }) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }), useOptionalPrefs: () => ({ lang: 'en-US' }) }))
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
const pageTree = (path = '/manage/apps/install') => (
    <App>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/manage/apps" element={<AppsAdminPage />} />
          <Route path="/manage/apps/install" element={<AppsInstallPage />} />
          <Route path="/manage/apps/:key" element={<Detail />} />
        </Routes>
      </MemoryRouter>
    </App>
)
const renderPage = (path = '/manage/apps/install') => render(pageTree(path))



beforeEach(() => { auth.user = 'admin'; localStorage.clear(); resetFixtures(apiMock) })
afterEach(() => vi.unstubAllGlobals())

describe('Install from the catalogue', () => {
  it('names no publisher id on a catalogue card or in the dialog header when the publisher has no name', async () => {
    renderPage()
    await screen.findByText('Charts')
    expect(document.body.textContent).not.toContain('appStore.publishedBy:stranger')
    const user = userEvent.setup()
    await user.click((await screen.findAllByRole('button', { name: /tools|Tools/ }))[0])
    const head = document.querySelector('.rp-market-detail-head')!
    expect(head.textContent).not.toContain('stranger')
  })

  it('keeps the list on screen while the same source is read again', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Charts')
    let answer: (value: unknown) => void = () => undefined
    const base = apiMock.post.getMockImplementation()!
    apiMock.post.mockImplementation((url: string, body: unknown) => url === `${root}/market/catalogue` ? new Promise(resolve => { answer = resolve }) : base(url, body))
    await user.click(screen.getByRole('button', { name: 'appStore.install.custom' }))
    await user.type(screen.getByRole('textbox', { name: 'appStore.customPrompt' }), catalogueUrl)
    await user.click(screen.getByRole('button', { name: 'appStore.customOpen' }))
    await waitFor(() => expect(apiMock.post.mock.calls.filter(([url]) => url === `${root}/market/catalogue`).length).toBeGreaterThan(1))
    expect(screen.getByText('Charts')).toBeTruthy()
    await act(async () => { answer(catalogue()) })
    expect(await screen.findByText('Charts')).toBeTruthy()
  })

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

  it('shows a slow download\'s progress on the Install button, on the dialog and on the card', async () => {
    const base = apiMock.post.getMockImplementation()!
    apiMock.post.mockImplementation(async (url: string, body: unknown) => url === `${root}/market/stage`
      ? { state: 'downloading', release_sha256: sha('c'), bytes_done: String(1024 * 1024), bytes_total: String(4 * 1024 * 1024) }
      : base(url, body))
    apiMock.get.mockImplementation(((get) => async (url: string, ...rest: unknown[]) => url.startsWith(`${root}/market/stage?`) ? new Promise(() => undefined) : get(url, ...rest))(apiMock.get.getMockImplementation()!))
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'appStore.installAction' }))
    expect(await within(dialog).findByRole('button', { name: /appStore\.downloadProgress:\{"done":1,"total":4\}/ })).toBeTruthy()
    expect(within(dialog).getByRole('progressbar')).toBeTruthy()
    expect(screen.getByRole('button', { name: /25%/ })).toBeTruthy()
  })

  it('lets the dialog close while a download runs on, and says a later failure on the card', async () => {
    const base = apiMock.post.getMockImplementation()!
    let finish!: (v: unknown) => void
    apiMock.post.mockImplementation(async (url: string, body: unknown) => url === `${root}/market/stage` ? new Promise(done => { finish = done }) : base(url, body))
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'appStore.installAction' }))
    await waitFor(() => expect(finish).toBeTypeOf('function'))
    await user.click(within(dialog).getByRole('button', { name: /close/i }))
    await act(async () => { finish({ state: 'failed', release_sha256: sha('c'), error_code: 'app_platform_busy' }) })
    await waitFor(() => expect(document.querySelector('.rp-market-card [role="alert"]')?.textContent).toContain('err.app_platform_busy'))
  })

  it('installs an older version when it is chosen in the dialog', async () => {
    fx.releaseRows[sha('e')] = verifiedRow('e', '2.0.0-beta.1')
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
    fx.releaseRows[sha('c')] = verifiedRow('c', '9.9.9')
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
    fx.catalogueResponse = () => { throw new Error('catalogue down') }
    renderPage()
    expect(await screen.findByText('catalogue down')).toBeTruthy()
    expect(screen.queryByText('appStore.catalogueEmpty')).toBeNull()
    expect(screen.getByRole('button', { name: 'appStore.retry' })).toBeTruthy()
  })
})

describe('The install page top bar', () => {
  it('opens a custom catalogue and lets the source switch return to the official one', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Charts')
    await user.click(screen.getByRole('button', { name: 'appStore.install.custom' }))
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

describe('Versions and publishers in the catalogue', () => {
  const versioned = (versions: string[], extra: Record<string, unknown> = {}) => ({ ...chartsApp, ...extra, versions: versions.map((version, i) => ({ version, descriptor_sha256: sha('0123456789abcdef'[i])})) })

  it('offers the newest stable version by default and marks it latest, while listing everything newest first', async () => {
    fx.catalogueResponse = () => catalogue([versioned(['3.0.0-beta.1', '2.0.0', '1.0.0'])])
    fx.releaseRows[sha('1')] = verifiedRow('1', '2.0.0')
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getAllByRole('listitem').map(i => i.textContent?.replace('appStore.latest', '').trim())).toEqual(['3.0.0-beta.1', '2.0.0', '1.0.0'])
    await user.click(within(dialog).getByRole('button', { name: 'appStore.installAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: catalogueUrl, release_sha256: sha('1') }))
  })

  it('defaults to the newest prerelease when there is no stable version', async () => {
    fx.catalogueResponse = () => catalogue([versioned(['0.1.0-beta.10', '0.1.0-beta.2'])])
    fx.releaseRows[sha('0')] = verifiedRow('0', '0.1.0-beta.10')
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

  it('treats another publisher’s app with the same id as a different app: offered, while the installed one is not', async () => {
    withPlatform('installed', '1.0.0')
    const imposter = { ...notesApp, publisher_id: 'imposter', versions: [{ version: '9.0.0', descriptor_sha256: sha('9') }], display: display('Imposter Notes', 'Not yours') }
    fx.catalogueResponse = () => catalogue([imposter, notesApp])
    renderPage()
    expect(await screen.findByRole('button', { name: 'Imposter Notes' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Notes' })).toBeNull()
  })
})

describe('The install page', () => {
  it('has a breadcrumb back to the apps and a title', async () => {
    renderPage()
    await screen.findByRole('button', { name: 'Charts' })
    const crumb = screen.getByRole('navigation', { name: 'apps.breadcrumb' })
    expect(within(crumb).getByRole('link', { name: 'apps.title' }).getAttribute('href')).toBe('/manage/apps')
    expect(within(crumb).getByText('appStore.install')).toBeTruthy()
    expect(screen.getByRole('heading', { name: 'appStore.install' })).toBeTruthy()
  })

  it('puts the custom catalogue and the offline package in the top bar', async () => {
    renderPage()
    await screen.findByRole('button', { name: 'Charts' })
    expect(screen.getByRole('button', { name: 'appStore.install.custom' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'appStore.install.offline' })).toBeTruthy()
    expect(screen.getByRole('combobox', { name: 'appStore.source' })).toBeTruthy()
  })

  it('installs a catalogue app straight from the small Install button on its card', async () => {
    const user = userEvent.setup()
    renderPage()
    const card = (await screen.findByRole('button', { name: 'Charts' })).closest('.rp-app-card') as HTMLElement
    await user.click(within(card).getByRole('button', { name: 'appStore.installAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: catalogueUrl, release_sha256: sha('c') }))
    expect(await screen.findByText('appPlatform.step.audience')).toBeTruthy()
  })

  it('says a failed download on the card itself', async () => {
    apiMock.post.mockImplementation(async (url: string, body: { release_sha256?: string }) => {
      if (url === `${root}/market/catalogue`) return catalogue()
      if (url === `${root}/market/stage`) throw new Error('download failed')
      return { release_sha256: body.release_sha256 }
    })
    const user = userEvent.setup()
    renderPage()
    const card = (await screen.findByRole('button', { name: 'Charts' })).closest('.rp-app-card') as HTMLElement
    await user.click(within(card).getByRole('button', { name: 'appStore.installAction' }))
    expect(await within(card).findByText('download failed')).toBeTruthy()
  })

  it('says a failed download in the dialog next to the Install button', async () => {
    apiMock.post.mockImplementation(async (url: string) => {
      if (url === `${root}/market/catalogue`) return catalogue()
      throw new Error('download failed')
    })
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Charts' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'appStore.installAction' }))
    const message = await within(dialog).findByText('download failed')
    expect(message.closest('.rp-market-detail-head')).not.toBeNull()
  })

  it('offers no install for an untrusted publisher on the card', async () => {
    renderPage()
    const card = (await screen.findByRole('button', { name: 'tools' })).closest('.rp-app-card') as HTMLElement
    expect((within(card).getByRole('button', { name: 'appStore.installAction' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('resumes an operation here and, once it is done, offers the app settings and a way back to the list', async () => {
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.startsWith(`${root}/operations/`)) return { operation_id: operation, installation_id: installationId, action: 'install', stage: 'complete', terminal: true, control_paused: false }
      if (url === '/api/admin/apps/layout') return { apps: fx.entries }
      if (url === `${root}/market/sources`) return { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: true } }
      return { items: [], next_cursor: '' }
    })
    renderPage(`/manage/apps/install?operation=${operation}`)
    const settings = await screen.findByRole('button', { name: 'appPlatform.openSettings' })
    expect(screen.getByRole('button', { name: 'appStore.backToApps' })).toBeTruthy()
    await userEvent.setup().click(settings)
    expect(await screen.findByText(`detail:platform:${installationId}`)).toBeTruthy()
  })

  it('reads the sources again each time it opens, so the official switch is honoured', async () => {
    let enabled = true
    const base = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string) => url === `${root}/market/sources` ? { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled } } : base(url))
    const first = renderPage()
    await screen.findByRole('button', { name: 'Charts' })
    first.unmount()
    enabled = false
    renderPage()
    expect(await screen.findByText('appStore.sourceNone')).toBeTruthy()
  })
})
