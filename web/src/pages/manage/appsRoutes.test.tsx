import { Suspense } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { RouterProvider, Route, createMemoryRouter, createRoutesFromElements, useLocation, useParams } from 'react-router'
import { appsRoutes } from './appsRoutes'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
// Each page is a stub that names itself; what is under test is how the routes are put together.
const stub = (name: string) => ({ default: function Stub() {
  const { key, installation } = useParams()
  return <div data-testid="page">{name}{key ? `:${key}` : ''}{installation ? `:${installation}` : ''}</div>
} })
vi.mock('./AppsAdminPage', () => stub('console'))
vi.mock('./AppsInstallPage', () => stub('install'))
vi.mock('./AppDetailPage', () => stub('detail'))
vi.mock('./AppAccessPage', () => stub('access'))
vi.mock('./AppCredentialsPage', () => stub('credentials'))
vi.mock('./AppPlatformSettingsPage', () => stub('settings'))
vi.mock('./AppAdminTab', () => stub('admin'))
vi.mock('./InstallationOverview', () => stub('overview'))
// The installation tabs wrapper only frames its two child routes here.
vi.mock('./AppInstallationTabs', async () => {
  const { Outlet } = await import('react-router')
  return { default: function Tabs() { return <Outlet /> } }
})

function Where() {
  const loc = useLocation()
  return <div data-testid="where">{loc.pathname + loc.search}</div>
}
function mount(path: string) {
  const router = createMemoryRouter(createRoutesFromElements(<Route path="/manage" element={<><Where /><Suspense fallback={null}><OutletHost /></Suspense></>}>{appsRoutes}</Route>), { initialEntries: [path] })
  return render(<RouterProvider router={router} />)
}
import { Outlet } from 'react-router'
function OutletHost() { return <Outlet /> }

const subRoutes: [string, string][] = [
  ['/manage/apps/install', 'install'],
  ['/manage/apps/builtin%3Aquotes', 'detail:builtin:quotes'],
  ['/manage/apps/builtin%3Aquotes/access', 'access:builtin:quotes'],
  ['/manage/apps/notes/credentials', 'credentials:notes'],
  ['/manage/apps/installations/33333333-3333-4333-8333-333333333333/overview', 'overview:33333333-3333-4333-8333-333333333333'],
  ['/manage/apps/installations/33333333-3333-4333-8333-333333333333/permissions', 'settings:33333333-3333-4333-8333-333333333333'],
  ['/manage/apps/installations/33333333-3333-4333-8333-333333333333/access', 'settings:33333333-3333-4333-8333-333333333333'],
  ['/manage/apps/installations/33333333-3333-4333-8333-333333333333/data', 'settings:33333333-3333-4333-8333-333333333333'],
  ['/manage/apps/installations/33333333-3333-4333-8333-333333333333/admin', 'admin:33333333-3333-4333-8333-333333333333'],
]

describe('the apps console routes', () => {
  for (const [path, page] of subRoutes) {
    it(`${path} keeps the console tab bar, with Apps selected`, async () => {
      mount(path)
      expect((await screen.findByTestId('page')).textContent).toBe(page)
      const tabs = screen.getByRole('tablist')
      expect(within(tabs).getAllByRole('tab').map((t) => t.textContent)).toEqual(['nav.apps', 'appTrust.tab', 'appPlatform.tasks.tab', 'appPlatform.events.tab'])
      expect(within(tabs).getByRole('tab', { name: 'nav.apps' }).getAttribute('aria-selected')).toBe('true')
    })
  }

  it('sends a platform app\'s old page and access URLs to its installation page', async () => {
    const id = '33333333-3333-4333-8333-333333333333'
    mount(`/manage/apps/platform%3A${id}`)
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe(`/manage/apps/installations/${id}/overview`))
    cleanup()
    mount(`/manage/apps/platform%3A${id}/access`)
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe(`/manage/apps/installations/${id}/access`))
    cleanup()
    // The query string (an operation to follow) survives the redirect.
    mount(`/manage/apps/platform%3A${id}?operation=abc`)
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe(`/manage/apps/installations/${id}/overview?operation=abc`))
  })

  it('declares the install page before the app page, so "install" is never read as an app key', async () => {
    mount('/manage/apps/install')
    expect((await screen.findByTestId('page')).textContent).toBe('install')
  })

  for (const [tab, label] of [['publishers', 'appTrust.tab'], ['tasks', 'appPlatform.tasks.tab'], ['subscriptions', 'appPlatform.events.tab']]) {
    it(`goes to the ${tab} tab of the console from a page under it`, async () => {
      const user = userEvent.setup()
      mount('/manage/apps/builtin%3Aquotes/access')
      await screen.findByTestId('page')
      await user.click(screen.getByRole('tab', { name: label }))
      await waitFor(() => expect(screen.getByTestId('where').textContent).toBe(`/manage/apps?tab=${tab}`))
      expect((await screen.findByTestId('page')).textContent).toBe('console')
    })
  }

  it('goes back to the list when Apps is chosen on a page under it', async () => {
    const user = userEvent.setup()
    mount('/manage/apps/install')
    await screen.findByTestId('page')
    await user.click(screen.getByRole('tab', { name: 'nav.apps' }))
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/manage/apps'))
  })
})
