import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { MemoryRouter, Route, Routes, useParams } from 'react-router'
import AppsAdminPage from './AppsAdminPage'
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
          <Route path="/manage/apps/install" element={<div>install-page</div>} />
          <Route path="/manage/apps/:key" element={<Detail />} />
        </Routes>
      </MemoryRouter>
    </App>
)
const renderPage = (path = '/manage/apps') => render(pageTree(path))



beforeEach(() => { auth.user = 'admin'; localStorage.clear(); resetFixtures(apiMock) })
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

  it('reads the catalogue quietly, so a card can offer an update', async () => {
    withPlatform('installed')
    renderPage()
    await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' })
    expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, { catalogue_url: catalogueUrl })
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

describe('The page settles once, then shows the cards complete', () => {
  // The cards show what the catalogue adds, so the page waits for it with one loading state and renders every card at
  // once: a name, a publisher or an update offer never appears or changes after the first card is on screen.
  it('shows one page spinner and no card until the layout and the catalogue have both answered', async () => {
    withPlatform('installed')
    let answer: (value: unknown) => void = () => undefined
    const original = apiMock.post.getMockImplementation()!
    apiMock.post.mockImplementation((url: string, body: unknown) => url === `${root}/market/catalogue` ? new Promise(resolve => { answer = resolve }) : original(url, body))
    renderPage()
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, expect.anything()))
    // Everything else has answered; only the catalogue is out.
    expect(screen.getByText('common.loading')).toBeTruthy()
    expect(screen.queryByText('Notes')).toBeNull()
    expect(screen.queryByText('nav.batch')).toBeNull()
    expect(screen.queryByText(/notes/)).toBeNull()
    expect(document.querySelector('.rp-app-grid')).toBeNull()
    await act(async () => { answer(catalogue()) })
    // The first card on screen is already final: its name, version and update offer are all there, nothing arrives later.
    const card = within(await screen.findByText('nav.batch').then(() => cardOf('Notes')))
    expect(card.getByText(/kazuha.*1\.0\.0/)).toBeTruthy()
    expect(card.getByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' })).toBeTruthy()
    expect(screen.queryByText('common.loading')).toBeNull()
  })

  it('settles on a failed catalogue: the cards render with what is known and an inline retry, never an endless spinner', async () => {
    withPlatform('installed')
    const user = userEvent.setup()
    const original = apiMock.post.getMockImplementation()!
    apiMock.post.mockImplementation(async (url: string, body: unknown) => {
      if (url === `${root}/market/catalogue`) throw new Error('catalogue down')
      return original(url, body)
    })
    renderPage()
    expect(await screen.findByText('appStore.catalogueFailed')).toBeTruthy()
    expect(screen.queryByText('common.loading')).toBeNull()
    expect(within(cardOf('Notes')).getByText(/1\.0\.0/)).toBeTruthy()
    expect(screen.getByText('nav.batch')).toBeTruthy()
    // The retry asks again, and the page keeps its cards while it does.
    apiMock.post.mockImplementation(original)
    await user.click(screen.getByRole('button', { name: 'common.retry' }))
    expect(await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' })).toBeTruthy()
    expect(screen.queryByText('appStore.catalogueFailed')).toBeNull()
    expect(screen.queryByText('common.loading')).toBeNull()
  })

  it('does not wait for a catalogue that has no source', async () => {
    apiMock.get.mockImplementation(async (url: string) => url === `${root}/market/sources` ? {} : url === '/api/admin/apps/layout' ? { apps: fx.entries } : { items: [], next_cursor: '' })
    renderPage()
    expect(await screen.findByText('nav.batch')).toBeTruthy()
    expect(screen.queryByText('common.loading')).toBeNull()
  })
})

describe('The cards and list view', () => {
  it('shows a dense list, with the same actions, when the list view is chosen, and remembers the choice', async () => {
    withPlatform('installed')
    const user = userEvent.setup()
    const view = renderPage()
    await screen.findByText('nav.batch')
    expect(document.querySelector('.rp-app-grid')).not.toBeNull()
    await user.click(screen.getByTitle('appStore.view.list'))
    expect(document.querySelector('.rp-app-grid')).toBeNull()
    expect(await screen.findByRole('columnheader', { name: 'appStore.col.status' })).toBeTruthy()
    expect(screen.getByRole('columnheader', { name: 'appStore.col.access' })).toBeTruthy()
    expect(screen.getByText('nav.batch')).toBeTruthy()
    await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' })
    expect(localStorage.getItem('rp.apps.view')).toBe('list')
    view.unmount()
    renderPage()
    expect(await screen.findByRole('columnheader', { name: 'appStore.col.status' })).toBeTruthy()
  })

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
    fx.releaseRows[sha('a')] = verifiedRow('a', '1.0.0', { installation_id: installationId, installable: false })
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
    fx.releaseRows[sha('a')] = verifiedRow('a', '1.0.0', { installation_id: installationId, installable: false, uninstallable: true })
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

describe('A prerelease install', () => {
  const prereleaseCatalogue = () => catalogue([{ ...notesApp, versions: [{ version: '1.0.0-beta.2', descriptor_sha256: sha('e') }, { version: '0.9.0', descriptor_sha256: sha('7') }, { version: '1.0.0-beta.1', descriptor_sha256: sha('a') }] }])
  it('is offered the newer prerelease, and the dialog opens on it', async () => {
    withPlatform('installed', '1.0.0-beta.1')
    fx.catalogueResponse = prereleaseCatalogue
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
    fx.releaseRows[sha('b')] = verifiedRow('b', '1.1.0', { installable: false, installation_id: installationId, enableable: true })
    const { user, dialog } = await openDialog()
    await user.click(within(dialog).getByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'enable', installation_id: installationId }))
  })

  it('reviews an uninstall when the staged release is one that can only be uninstalled', async () => {
    fx.releaseRows[sha('b')] = verifiedRow('b', '1.1.0', { installable: false, installation_id: installationId, uninstallable: true })
    const { user, dialog } = await openDialog()
    await user.click(within(dialog).getByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'uninstall', installation_id: installationId }))
  })

  it('says so instead of planning an update when the staged release is the installed one', async () => {
    fx.releaseRows[sha('b')] = verifiedRow('b', '1.1.0', { installable: false, installation_id: installationId, installed: true })
    const { user, dialog } = await openDialog()
    await user.click(within(dialog).getByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' }))
    expect(await within(dialog).findByText('appStore.alreadyInstalled')).toBeTruthy()
    expect(apiMock.post.mock.calls.filter(([url]) => url === `${root}/plans`)).toHaveLength(0)
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


describe('Prerelease policy and publisher identity', () => {
  it('does not nudge a stable install toward a prerelease', async () => {
    withPlatform('installed', '1.0.0')
    fx.catalogueResponse = () => catalogue([{ ...notesApp, versions: [{ version: '2.0.0-beta.1', descriptor_sha256: sha('e') }, { version: '1.0.0', descriptor_sha256: sha('a') }] }])
    renderPage()
    await screen.findByText('nav.batch')
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, expect.anything()))
    expect(screen.queryByRole('button', { name: /appStore\.updateTo/ })).toBeNull()
  })

  it('offers a prerelease install the newer prerelease', async () => {
    withPlatform('installed', '1.0.0-beta.1')
    fx.catalogueResponse = () => catalogue([{ ...notesApp, versions: [{ version: '1.0.0-beta.2', descriptor_sha256: sha('e') }, { version: '1.0.0-beta.1', descriptor_sha256: sha('a') }] }])
    renderPage()
    expect(await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.0.0-beta.2"}' })).toBeTruthy()
  })

  it('gives an installation with no publisher in its layout entry no catalogue match', async () => {
    fx.entries = [...layout(), { ...platformEntry('installed'), publisher_id: undefined }]
    renderPage()
    await screen.findByText('nav.batch')
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, expect.anything()))
    expect(screen.queryByRole('button', { name: /appStore\.updateTo/ })).toBeNull()
  })

  it('does not give an installed card another publisher’s versions for the same app id', async () => {
    withPlatform('installed', '1.0.0')
    const imposter = { ...notesApp, publisher_id: 'imposter', versions: [{ version: '9.0.0', descriptor_sha256: sha('9') }], display: display('Imposter Notes', 'Not yours') }
    fx.catalogueResponse = () => catalogue([imposter, notesApp])
    renderPage()
    expect(await screen.findByRole('button', { name: 'appStore.updateTo:{"version":"1.1.0"}' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /appStore\.updateTo:\{"version":"9/ })).toBeNull()
  })
})

