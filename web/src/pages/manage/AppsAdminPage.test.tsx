import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter, Route, Routes, useParams } from 'react-router'
import AppsAdminPage from './AppsAdminPage'
import { installed, layout } from './appsAdminFixtures'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook, initReactI18next: { type: '3rdParty', init: () => undefined } }
})
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
const renderPage = () =>
  render(
    <App>
      <MemoryRouter initialEntries={['/manage/apps']}>
        <Routes>
          <Route path="/manage/apps" element={<AppsAdminPage />} />
          <Route path="/manage/apps/:key" element={<Detail />} />
        </Routes>
      </MemoryRouter>
    </App>,
  )

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.post.mockReset()
  apiMock.put.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/apps/layout') return { apps: layout() }
    if (url === '/api/apps') return { apps: installed }
    if (url === '/api/admin/apps/market')
      return {
        index_url: 'https://example.invalid/index.json',
        apps: [{ id: 'status', name: 'Status', version: '1.0.0', scopes: ['extension.read', 'extension.manage'], installed: false }],
      }
    throw new Error(`unexpected ${url}`)
  })
  apiMock.put.mockResolvedValue({ apps: layout() })
})
afterEach(() => vi.unstubAllGlobals())

const rowOf = (name: string) => screen.getByText(name).closest('tr') as HTMLElement

describe('Apps list', () => {
  it('summarizes each app by the OUs it is effectively open for', async () => {
    renderPage()
    await screen.findByText('nav.batch')
    expect(within(rowOf('nav.batch')).getByText('apps.status.all')).toBeTruthy()
    expect(within(rowOf('nav.quotes')).getByText('apps.status.some:{"open":1,"total":2}')).toBeTruthy()
    expect(within(rowOf('Hello')).getByText('apps.status.none')).toBeTruthy()
  })

  it('names the kind of each app, and an installed app by its version', async () => {
    renderPage()
    await screen.findByText('nav.batch')
    expect(within(rowOf('nav.batch')).getByText('apps.type.builtin')).toBeTruthy()
    expect(within(rowOf('Hello')).getByText('apps.type.installed')).toBeTruthy()
    expect(within(rowOf('Hello')).getByText(/1\.2\.0/)).toBeTruthy()
  })

  it('gives each built-in app its own icon, as the apps hub does, and an installed app its manifest icon', async () => {
    renderPage()
    await screen.findByText('nav.batch')
    expect(rowOf('nav.batch').querySelector('[data-icon="play-circle"]')).not.toBeNull()
    expect(rowOf('nav.quotes').querySelector('[data-icon="line-chart"]')).not.toBeNull()
    expect(rowOf('Hello').querySelector('[data-icon="appstore"]')).toBeNull()
    expect(within(rowOf('Hello')).getByText('H')).toBeTruthy()
  })

  it('fits a phone: no fixed column widths, and Type and the chevron are marked to hide when narrow', async () => {
    const { container } = renderPage()
    await screen.findByText('nav.batch')
    // A fixed width (a <col> or a width style on a header) is what pushed the page sideways at 375px.
    expect(container.querySelectorAll('col[style*="width"]').length).toBe(0)
    expect(container.querySelectorAll('th[style*="width"]').length).toBe(0)
    expect(container.querySelector('th.rp-apps-col-type')).not.toBeNull()
    expect(container.querySelector('th.rp-apps-col-chevron')).not.toBeNull()
    expect(container.querySelector('td.rp-apps-col-chevron')).not.toBeNull()
  })

  it('filters by the search box and counts the rows', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('nav.batch')
    expect(screen.getByText('apps.count:{"count":3}')).toBeTruthy()
    await user.type(screen.getByRole('searchbox', { name: 'apps.search' }), 'hel')
    expect(screen.queryByText('nav.batch')).toBeNull()
    expect(screen.getByText('apps.count:{"count":1}')).toBeTruthy()
  })

  it('opens the detail page when a row is clicked, with the key URL-encoded in the path', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByText('nav.quotes'))
    expect(await screen.findByText('detail:builtin:quotes')).toBeTruthy()
  })

  it('offers each app name as a link, so a keyboard reaches the detail page too', async () => {
    renderPage()
    const link = (await screen.findByRole('link', { name: /nav\.quotes/ })) as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('/manage/apps/builtin%3Aquotes')
  })

  it('says the load failed instead of claiming there are no apps', async () => {
    apiMock.get.mockRejectedValue(new Error('boom'))
    renderPage()
    expect(await screen.findByText('Request failed')).toBeTruthy()
    expect(screen.queryByText('apps.empty')).toBeNull()
  })
})

describe('Reorder', () => {
  it('persists the new order only when Done is pressed', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'apps.reorder' }))
    await user.click(await screen.findByRole('button', { name: 'reverse' }))
    expect(apiMock.put).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'common.done' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/apps/order', { keys: ['builtin:quotes', 'builtin:batch', 'hello'] }))
  })

  it('discards the arrangement on Cancel', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'apps.reorder' }))
    await user.click(await screen.findByRole('button', { name: 'reverse' }))
    await user.click(screen.getByRole('button', { name: 'common.cancel' }))
    expect(apiMock.put).not.toHaveBeenCalled()
    // Reopening starts again from the saved order, not from the discarded one.
    await user.click(screen.getByRole('button', { name: 'apps.reorder' }))
    await waitFor(() => {
      const ids = Array.from(document.querySelectorAll('[data-sortable-id]')).map((e) => e.getAttribute('data-sortable-id'))
      expect(ids).toEqual(['hello', 'builtin:batch', 'builtin:quotes'])
    })
  })
})

describe('Install app', () => {
  it('offers uploading a package and the app market', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: "apps.install" }))
    expect(await screen.findByRole('menuitem', { name: 'apps.upload' })).toBeTruthy()
    expect(screen.getByRole('menuitem', { name: 'apps.fromMarket' })).toBeTruthy()
  })

  it('waits for confirmation of a market app and submits the reviewed version and capabilities', async () => {
    apiMock.post.mockResolvedValue({ ok: true })
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: "apps.install" }))
    await user.click(await screen.findByRole('menuitem', { name: 'apps.fromMarket' }))
    await user.click(await screen.findByRole('button', { name: /apps\.marketFetch/ }))
    await user.click(await screen.findByRole('button', { name: /apps\.marketInstall/ }))
    await screen.findByText('apps.scopeExtensionManage')
    expect(apiMock.post).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'apps.permGrant' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/apps/market/install', {
        id: 'status',
        version: '1.0.0',
        scopes: ['extension.read', 'extension.manage'],
      }),
    )
  })

  it('previews an uploaded package for its permissions before installing it', async () => {
    const fetchMock = vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ preview: true, app: { id: 'x', name: 'X', scopes: ['query'] } }) }))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('nav.batch')
    await user.upload(screen.getByTestId('apps-upload-input'), new File(['zip'], 'x.zip', { type: 'application/zip' }))
    await screen.findByText('apps.scopeQuery')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect((fetchMock.mock.calls[0] as unknown[])[0]).toBe('/api/admin/apps/install?preview=1')
    await user.click(screen.getByRole('button', { name: 'apps.permGrant' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect((fetchMock.mock.calls[1] as unknown[])[0]).toBe('/api/admin/apps/install')
  })
})
