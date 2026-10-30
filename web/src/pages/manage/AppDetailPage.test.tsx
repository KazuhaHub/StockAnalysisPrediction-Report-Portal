import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import AppDetailPage from './AppDetailPage'
import { installed, layout } from './appsAdminFixtures'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), del: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock, errText: () => 'Request failed', ApiError: class extends Error {} }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})
// The backend dialog has its own behaviour; here only whether this page opens it matters.
vi.mock('../../components/AppBackendModal', () => ({
  default: ({ app }: { app: { id: string } | null }) => (app ? <div>backend-modal:{app.id}</div> : null),
}))

function Where() {
  const loc = useLocation()
  return <div data-testid="where">{loc.pathname}</div>
}
const renderAt = (path: string) =>
  render(
    <App>
      <MemoryRouter initialEntries={[path]}>
        <Where />
        <Routes>
          <Route path="/manage/apps" element={<div>list</div>} />
          <Route path="/manage/apps/:key" element={<AppDetailPage />} />
          <Route path="/manage/apps/:key/access" element={<div>access</div>} />
        </Routes>
      </MemoryRouter>
    </App>,
  )

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.del.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/apps/layout') return { apps: layout() }
    if (url === '/api/apps') return { apps: installed }
    throw new Error(`unexpected ${url}`)
  })
  apiMock.del.mockResolvedValue({})
})

describe('App detail: built-in app', () => {
  it('summarizes the app and offers only the sections that apply', async () => {
    renderAt('/manage/apps/builtin%3Aquotes')
    expect(await screen.findByRole('heading', { name: 'nav.quotes' })).toBeTruthy()
    expect(screen.getByText('apps.quotesDesc')).toBeTruthy()
    expect(screen.getByText('/apps/quotes')).toBeTruthy()
    expect(screen.getByRole('button', { name: /apps\.section\.status\.title/ })).toBeTruthy()
    expect(screen.getByText('apps.section.scopes.title')).toBeTruthy()
    // No backend connection and no uninstall for a compiled-in app.
    expect(screen.queryByText('apps.section.backend.title')).toBeNull()
    expect(screen.queryByText('apps.section.uninstall.title')).toBeNull()
  })

  it('shows the permission a built-in app needs', async () => {
    renderAt('/manage/apps/builtin%3Abatch')
    await screen.findByRole('heading', { name: 'nav.batch' })
    expect(screen.getAllByText('apps.perm.run_batch').length).toBeGreaterThan(0)
  })

  it('shows the current service status on its card and opens the access page', async () => {
    const user = userEvent.setup()
    renderAt('/manage/apps/builtin%3Aquotes')
    const card = await screen.findByRole('button', { name: /apps\.section\.status\.title/ })
    expect(within(card).getByText('apps.status.some:{"open":1,"total":2}')).toBeTruthy()
    await user.click(card)
    expect(await screen.findByText('access')).toBeTruthy()
    expect(screen.getByTestId('where').textContent).toBe('/manage/apps/builtin%3Aquotes/access')
  })

  it('names a section card by its title, its explanation and its current value', async () => {
    renderAt('/manage/apps/builtin%3Aquotes')
    const card = await screen.findByRole('button', { name: /apps\.section\.status\.title/ })
    expect(card.getAttribute('aria-label')).toBeNull()
    expect(card.textContent).toContain('apps.section.status.hint')
    expect(card.textContent).toContain('apps.status.some')
  })

  it('says so when the app is unknown', async () => {
    renderAt('/manage/apps/nope')
    expect(await screen.findByText('apps.notFound')).toBeTruthy()
  })
})

describe('App detail: installed app', () => {
  it('shows version, entry and granted scopes, with backend and uninstall sections', async () => {
    renderAt('/manage/apps/hello')
    expect(await screen.findByRole('heading', { name: 'Hello' })).toBeTruthy()
    expect(screen.getByText('1.2.0')).toBeTruthy()
    expect(screen.getByText('index.html')).toBeTruthy()
    expect(screen.getByText('apps.scopeQuery')).toBeTruthy()
    expect(screen.getByRole('button', { name: /apps\.section\.backend\.title/ })).toBeTruthy()
    expect(screen.getByRole('button', { name: /apps\.section\.uninstall\.title/ })).toBeTruthy()
  })

  it('does not claim the app has no permissions when the installed list could not be read', async () => {
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      throw new Error('boom')
    })
    renderAt('/manage/apps/hello')
    expect(await screen.findByText('apps.detail.installedFailed')).toBeTruthy()
    expect(screen.queryByText('apps.permNone')).toBeNull()
  })

  it('opens the backend connection dialog for the app', async () => {
    const user = userEvent.setup()
    renderAt('/manage/apps/hello')
    await user.click(await screen.findByRole('button', { name: /apps\.section\.backend\.title/ }))
    expect(await screen.findByText('backend-modal:hello')).toBeTruthy()
  })

  it('uninstalls only after confirmation, then returns to the list', async () => {
    const user = userEvent.setup()
    renderAt('/manage/apps/hello')
    await user.click(await screen.findByRole('button', { name: /apps\.section\.uninstall\.title/ }))
    // The confirmation says what else goes with the app; nothing is deleted yet.
    expect(await screen.findByText('apps.section.uninstall.confirmBody')).toBeTruthy()
    expect(apiMock.del).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'apps.section.uninstall.confirmOk' }))
    await waitFor(() => expect(apiMock.del).toHaveBeenCalledWith('/api/admin/apps/hello'))
    expect(await screen.findByText('list')).toBeTruthy()
  })

  it('keeps the app when the confirmation is cancelled', async () => {
    const user = userEvent.setup()
    renderAt('/manage/apps/hello')
    await user.click(await screen.findByRole('button', { name: /apps\.section\.uninstall\.title/ }))
    await user.click(await screen.findByRole('button', { name: 'common.cancel' }))
    expect(apiMock.del).not.toHaveBeenCalled()
    expect(screen.getByRole('heading', { name: 'Hello' })).toBeTruthy()
  })
})
