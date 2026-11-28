import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { MemoryRouter, Route, Routes, useParams } from 'react-router'
import AppsAdminPage from './AppsAdminPage'
import { layout } from './appsAdminFixtures'
import { resetCatalogueHint } from './useUpdateHint'
import { cardOf, catalogue, plan, planSha, chartsApp, display, fx, installationId, notesApp, operation, platformEntry, resetFixtures, root, sha, verifiedRow, withPlatform, catalogueUrl } from './appsConsoleFixtures'

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
const pageTree = (path = '/manage/apps') => (
    <App>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/manage/apps" element={<AppsAdminPage />} />
          <Route path="/manage/apps/install" element={<div>install-page</div>} />
          <Route path="/manage/apps/:key" element={<Detail />} />
        </Routes>
      </MemoryRouter>
    </App>
)
const renderPage = (path = '/manage/apps') => render(pageTree(path))



beforeEach(() => { auth.user = 'admin'; localStorage.clear(); sessionStorage.clear(); resetCatalogueHint(); resetFixtures(apiMock) })
afterEach(() => vi.unstubAllGlobals())

describe('Installed apps', () => {
  it('lists every app as a compact card with where it is switched on, built-ins marked once', async () => {
    renderPage()
    await screen.findByText('nav.batch')
    expect(within(cardOf('nav.batch')).getByText('apps.status.all')).toBeTruthy()
    expect(within(cardOf('nav.quotes')).getByText('apps.status.someShort:{"open":1,"total":2}')).toBeTruthy()
    expect(within(cardOf('Hello')).getByText('apps.status.none')).toBeTruthy()
    expect(within(cardOf('nav.batch')).getAllByText('appStore.builtinBadge')).toHaveLength(1)
    expect(within(cardOf('Hello')).queryByText('appStore.builtinBadge')).toBeNull()
    expect(cardOf('nav.batch').querySelector('[data-icon="play-circle"]')).not.toBeNull()
    expect(within(cardOf('Hello')).getByText(/1\.2\.0/)).toBeTruthy()
  })

  it('has no catalogue on this page, no footer row and no unverified or preview notice', async () => {
    renderPage()
    await screen.findByText('nav.batch')
    expect(screen.queryByText('appPlatform.development')).toBeNull()
    expect(screen.queryByText('appMarket.unverified')).toBeNull()
    expect(screen.queryByRole('tab', { name: 'appPlatform.tab' })).toBeNull()
    expect(screen.getByRole('tab', { name: 'appTrust.tab' })).toBeTruthy()
    expect(document.querySelector('.rp-app-card-foot')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Charts' })).toBeNull()
  })


  it('opens the install page from a plain Install app button', async () => {
    const user = userEvent.setup()
    renderPage()
    const button = await screen.findByRole('button', { name: 'appStore.install' })
    expect(button.getAttribute('aria-haspopup')).toBeNull()
    await user.click(button)
    expect(await screen.findByText('install-page')).toBeTruthy()
  })

  it('opens the app page from the card and offers the name as a link', async () => {
    const user = userEvent.setup()
    renderPage()
    const link = (await screen.findByRole('link', { name: /nav\.quotes/ })) as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('/manage/apps/builtin%3Aquotes')
    await user.click(link)
    expect(await screen.findByText('detail:builtin:quotes')).toBeTruthy()
  })

  it('filters the apps by the search box', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('nav.batch')
    await user.type(screen.getByRole('searchbox', { name: 'apps.search' }), 'hel')
    expect(screen.queryByText('nav.batch')).toBeNull()
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

describe('The first view needs the layout only', () => {
  // The layout carries each app's verified name, publisher, version and display. The market catalogue is a remote read:
  // it loads in the background and adds elements (an update offer), never text that is already on screen.
  const entry = () => ({ ...platformEntry('installed'), publisher_name: 'Kazuha Hub', display: display('Notes', 'Personal notes') })
  const withVerifiedEntry = () => { fx.entries = [...layout(), entry() as never] }



  it('shows no app id in the publisher slot of the list view either', async () => {
    fx.entries = [...layout(), platformEntry('installed')]
    localStorage.setItem('rp.apps.view', 'list')
    renderPage()
    const row = (await screen.findByRole('link', { name: 'Notes' })).closest('.rp-app-list-app') as HTMLElement
    expect(row.textContent).toBe('Notes1.0.0')
  })


  it('does not wait for a catalogue that has no source', async () => {
    apiMock.get.mockImplementation(async (url: string) => url === `${root}/market/sources` ? {} : url === '/api/admin/apps/layout' ? { apps: fx.entries } : { items: [], next_cursor: '' })
    renderPage()
    expect(await screen.findByText('nav.batch')).toBeTruthy()
    expect(screen.queryByText('common.loading')).toBeNull()
  })
})

describe('The cards and list view', () => {

  it('starts on cards when storage holds nothing usable or throws', async () => {
    const get = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('storage unavailable') })
    try {
      renderPage()
      await screen.findByText('nav.batch')
      expect(document.querySelector('.rp-app-grid')).not.toBeNull()
    } finally { get.mockRestore() }
  })

  it('uses one shared grid rule of 240px columns and 12px gaps, with compact cards', async () => {
    const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'index.css'), 'utf8')
    expect(css).toMatch(/\.rp-app-grid\s*\{[^}]*gap:\s*12px[^}]*repeat\(auto-fill,\s*minmax\(min\(100%,\s*240px\),\s*1fr\)\)/)
    expect(css).toMatch(/\.rp-app-card > \.ant-card-body\s*\{[^}]*padding:\s*12px/)
    expect(css).toMatch(/\.rp-app-glyph\s*\{[^}]*width:\s*32px;\s*height:\s*32px/)
    expect(css).toMatch(/\.rp-app-card-desc\s*\{[^}]*white-space:\s*nowrap/)
  })
})

describe('A platform app card', () => {
  it('shows its state as a status, its version and who can use it', async () => {
    withPlatform('installed')
    renderPage()
    const card = (await screen.findAllByText('Notes')).map(n => n.closest('.rp-app-card') as HTMLElement).find(c => c.querySelector('[data-icon]') && within(c).queryByText('appStore.state.enabled'))!
    expect(within(card).getByText('appStore.state.enabled')).toBeTruthy()
    expect(within(card).getByText(/1\.0\.0/)).toBeTruthy()
  })

  it('maps known states and shows an unknown one as it is', async () => {
    for (const [state, text] of [['disabled', 'appStore.state.disabled'], ['pending', 'appStore.state.installing'], ['attention', 'appStore.state.recovery'], ['odd_state', 'odd_state']]) {
      withPlatform(state)
      const view = renderPage()
      expect(await screen.findByText(text)).toBeTruthy()
      view.unmount()
    }
  })









  it('disables from the menu after reading the installation’s own release', async () => {
    withPlatform('installed')
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: fx.entries }
      if (url === `${root}/installations/${installationId}/settings`) return { current_release_sha256: sha('a') }
      if (url.startsWith(`${root}/releases?release_sha256=`)) return { items: [fx.releaseRows[sha('a')]], next_cursor: '' }
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

  const enableFromMenu = async (backend: boolean) => {
    withPlatform('disabled')
    fx.releaseRows[sha('a')] = verifiedRow('a', '1.0.0', { installation_id: installationId, installable: false, ...(backend ? { backend: true } : null) })
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: fx.entries }
      if (url === `${root}/installations/${installationId}/settings`) return { current_release_sha256: sha('a') }
      if (url.startsWith(`${root}/releases?release_sha256=`)) return { items: [fx.releaseRows[sha('a')]], next_cursor: '' }
      if (url === `${root}/market/sources`) return { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: false } }
      return { items: [], next_cursor: '' }
    })
    const user = userEvent.setup()
    renderPage()
    await user.click((await screen.findAllByRole('button', { name: 'appStore.more' }))[0])
    await user.click(await screen.findByRole('menuitem', { name: 'appStore.enable' }))
  }

  it('opens the repair review when a disabled managed app is enabled from the menu', async () => {
    await enableFromMenu(true)
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'repair', installation_id: installationId }))
    expect(apiMock.post).not.toHaveBeenCalledWith(`${root}/plans`, { action: 'enable', installation_id: installationId })
    expect(await screen.findByText('appPlatform.confirmRepairTitle')).toBeTruthy()
  })

  it('opens the enable review when a disabled frontend-only app is enabled from the menu', async () => {
    await enableFromMenu(false)
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'enable', installation_id: installationId }))
    expect(apiMock.post).not.toHaveBeenCalledWith(`${root}/plans`, { action: 'repair', installation_id: installationId })
  })


})

describe('Deep links', () => {
  it('lands on the marketplace and resumes an operation in the modal', async () => {
    renderPage(`/manage/apps?operation=${operation}`)
    expect(screen.getByRole('tab', { name: 'nav.apps' }).getAttribute('aria-selected')).toBe('true')
    expect((await screen.findAllByText(/appPlatform\.stage\.queued/)).length).toBeGreaterThan(0)
    expect(screen.getByText('appPlatform.step.progress')).toBeTruthy()
  })

  it('lands on the marketplace for an installation link', async () => {
    renderPage(`/manage/apps?installation=${installationId}`)
    expect(screen.getByRole('tab', { name: 'nav.apps' }).getAttribute('aria-selected')).toBe('true')
    await screen.findByText('nav.batch')
  })
})

describe('The tab bar', () => {
  for (const [tab, label] of [['publishers', 'appTrust.tab'], ['tasks', 'appPlatform.tasks.tab'], ['subscriptions', 'appPlatform.events.tab']]) {
    it(`opens the ${tab} tab from ?tab=${tab}`, async () => {
      renderPage(`/manage/apps?tab=${tab}`)
      expect(screen.getByRole('tablist')).toBeTruthy()
      expect(screen.getByRole('tab', { name: label }).getAttribute('aria-selected')).toBe('true')
      expect(screen.getByRole('tab', { name: 'nav.apps' }).getAttribute('aria-selected')).toBe('false')
    })
  }

  it('opens Apps for an unknown ?tab=', async () => {
    renderPage('/manage/apps?tab=nope')
    expect(screen.getByRole('tab', { name: 'nav.apps' }).getAttribute('aria-selected')).toBe('true')
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





describe('Switch version loads on demand', () => {
  const opRow = (extra: Record<string, unknown> = {}) => ({ operation_id: operation, installation_id: installationId, action: 'update', stage: 'download', managed: false, revision: '3', terminal: false, created_ms: '1000', updated_ms: '2000', from_version: '1.0.0', to_version: '1.1.0', ...extra })
  const serveOperations = (row: Record<string, unknown> | null) => {
    const original = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string, opts?: unknown) => url === `${root}/operations?latest=1` ? { items: row ? [row] : [] } : original(url, opts))
  }
  const marketCalls = () => [...apiMock.get.mock.calls, ...apiMock.post.mock.calls].map(([url]) => String(url)).filter(url => url.includes('/market/'))
  const notesCard = async () => { await screen.findAllByText('Notes'); return cardOf('Notes') }

  it('reads the catalogue once in the background, offers a plain Switch version button, and tags a newer release', async () => {
    withPlatform('installed')
    renderPage()
    const card = await notesCard()
    // The cards are there before the read answers; the tag joins them.
    expect(within(card).getByRole('button', { name: 'appStore.switchVersion' })).toBeTruthy()
    const tag = await within(card).findByText('appStore.updateAvailable:{"version":"1.1.0"}')
    expect(marketCalls()).toEqual([`${root}/market/sources`, `${root}/market/catalogue`])
    // The same order as the list's status column: state, then the update tag, then the controls; never down among the buttons.
    expect(tag.closest('.rp-app-card-foot')).toBeNull()
    const follows = (a: Element, b: Element) => !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING)
    expect(follows(card.querySelector('.rp-app-card-meta')!, tag)).toBe(true)
    expect(follows(tag, within(card).getByRole('button', { name: 'appStore.switchVersion' }))).toBe(true)
  })

  it('shows no tag and no error when the app has nothing newer, and when the catalogue cannot be read', async () => {
    withPlatform('installed', '1.1.0')
    renderPage()
    await notesCard()
    await waitFor(() => expect(marketCalls()).toHaveLength(2))
    expect(screen.queryByText(/appStore\.updateAvailable/)).toBeNull()
    cleanup(); resetCatalogueHint()
    withPlatform('installed')
    const original = apiMock.post.getMockImplementation()!
    apiMock.post.mockImplementation(async (url: string, body: unknown) => { if (url === `${root}/market/catalogue`) throw new Error('catalogue down'); return original(url, body) })
    renderPage()
    await notesCard()
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, expect.anything()))
    expect(screen.queryByText(/appStore\.updateAvailable/)).toBeNull()
    expect(screen.queryByText('catalogue down')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('makes one catalogue request per page load, however many cards there are', async () => {
    fx.entries = [...layout(), platformEntry('installed'), { ...platformEntry('installed'), key: 'platform:44444444-4444-4444-8444-444444444444', installation_id: '44444444-4444-4444-8444-444444444444' }]
    renderPage()
    await screen.findAllByText('Notes')
    await waitFor(() => expect(screen.getAllByText(/appStore\.updateAvailable/)).toHaveLength(2))
    expect(apiMock.post.mock.calls.filter(([url]) => url === `${root}/market/catalogue`)).toHaveLength(1)
  })

  it('loads the versions when the button is pressed, and continues into the review inside the same dialog', async () => {
    withPlatform('installed')
    const user = userEvent.setup()
    renderPage()
    await user.click(within(await notesCard()).getByRole('button', { name: 'appStore.switchVersion' }))
    const radio = await screen.findByRole('radio', { name: /1\.1\.0/ })
    expect(marketCalls()).toHaveLength(4)
    await user.click(radio)
    await user.click(screen.getByRole('button', { name: 'appPlatform.next' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, expect.objectContaining({ action: 'update', installation_id: installationId })))
    expect(screen.getAllByRole('dialog')).toHaveLength(1)
  })

  it('offers the same button in the list view', async () => {
    withPlatform('installed')
    localStorage.setItem('rp.apps.view', 'list')
    renderPage()
    await screen.findByRole('link', { name: 'Notes' })
    expect(screen.getAllByRole('button', { name: 'appStore.switchVersion' })).toHaveLength(1)
  })

  it('shows a running operation as a progress line: action, target version, step and stage', async () => {
    withPlatform('installed')
    serveOperations(opRow())
    renderPage()
    const line = await within(await notesCard()).findByRole('button', { name: /appPlatform\.ops\.openProgress/ })
    expect(line.textContent).toContain('appPlatform.ops.actionVersion')
    expect(line.textContent).toContain('"version":"1.1.0"')
    expect(line.textContent).toContain('appPlatform.ops.step:{"step":3,"total":9}')
    expect(line.textContent).toContain('appPlatform.stage.download')
    expect(within(line).getByRole('progressbar')).toBeTruthy()
  })

  it('opens the full step list from the progress line', async () => {
    withPlatform('installed')
    serveOperations(opRow())
    const user = userEvent.setup()
    renderPage()
    await user.click(await within(await notesCard()).findByRole('button', { name: /appPlatform\.ops\.openProgress/ }))
    expect(await screen.findByRole('dialog')).toBeTruthy()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith(`${root}/operations/${operation}`, expect.anything()))
    expect(document.querySelectorAll('.rp-operation-steps .ant-steps-item').length).toBeGreaterThan(0)
  })

  it('keeps a failure on the card with the translated reason, and a success as a badge', async () => {
    withPlatform('installed')
    serveOperations(opRow({ stage: 'failed', terminal: true, error_code: 'capacity_exhausted_disk', completed_ms: '3000' }))
    renderPage()
    expect((await within(await notesCard()).findByRole('button', { name: /appPlatform\.ops\.failed/ })).textContent).toContain('err.capacity_exhausted_disk')
    cleanup()
    serveOperations(opRow({ stage: 'complete', terminal: true, completed_ms: String(Date.now() - 60_000) }))
    renderPage()
    expect(await within(await notesCard()).findByRole('button', { name: /appPlatform\.ops\.succeeded/ })).toBeTruthy()
    cleanup()
    // A success older than a day is gone: the card does not carry an old install for ever.
    serveOperations(opRow({ stage: 'complete', terminal: true, completed_ms: String(Date.now() - 25 * 3600_000) }))
    renderPage()
    expect(within(await notesCard()).queryByRole('button', { name: /appPlatform\.ops\.succeeded/ })).toBeNull()
  })

  it('re-reads the layout when an operation is seen finishing, so the card shows the new state without a dialog', async () => {
    withPlatform('installed')
    let finished = false
    const original = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string, opts?: unknown) => url === `${root}/operations?latest=1` ? { items: [opRow(finished ? { stage: 'complete', terminal: true, completed_ms: String(Date.now()) } : {})] } : original(url, opts))
    renderPage()
    await within(await notesCard()).findByRole('button', { name: /appPlatform\.ops\.openProgress/ })
    const layoutReads = () => apiMock.get.mock.calls.filter(([url]) => url === '/api/admin/apps/layout').length
    const before = layoutReads()
    finished = true
    await waitFor(() => expect(layoutReads()).toBeGreaterThan(before), { timeout: 8000 })
  })
})
