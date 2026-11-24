import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect } from 'react'
import { createMemoryRouter, RouterProvider, useOutletContext, useParams } from 'react-router'
import { ApiError } from '../../api/client'
import AppInstallationTabs from './AppInstallationTabs'
import AppAdminTab from './AppAdminTab'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: apiMock, errText: (e: unknown) => (e instanceof Error ? e.message : String(e)) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: 'admin' }) }))
const mounts = vi.hoisted(() => ({ page: 0 }))
// The tabs' pages are stand-ins that report what the frame gave them.
function PageStub({ name }: { name: string }) {
  const shared = useOutletContext<{ settings: { revision?: string } | null; reload: () => void; name: string } | undefined>()
  const tab = useParams().tab
  useEffect(() => { if (name === 'overview page') mounts.page++ }, [name])
  return <div>{name}<span data-testid="tab">{tab ?? ''}</span><span data-testid="shared">{shared?.settings ? `${shared.settings.revision ?? ''}|${shared.name}` : 'none'}</span><button onClick={() => shared?.reload()}>reload</button></div>
}
vi.mock('./PlatformLifecycleModal', () => ({ default: () => null }))
vi.mock('./appsConsoleFlow', () => ({ useAppsConsoleFlow: () => ({ lifecycle: { showOperation: () => undefined }, busy: false, busyReason: '', acting: '' }) }))
vi.mock('../../components/apps/AppSurfaceView', () => ({
  default: (props: { installation: string; surface: string; base: string; crumbTail?: boolean }) => <div data-testid="surface">{`${props.surface}|${props.installation}|${props.base}|${String(props.crumbTail)}`}</div>,
}))

const id = '11111111-1111-4111-8111-111111111111'
const base = `/manage/apps/installations/${id}`
const settingsUrl = `/api/admin/app-platform/installations/${id}/settings`
const display = { icon: 'dashboard', texts: [{ locale: 'en-US', name: 'Demo App', description: 'd' }] }
const settingsOf = (extra: Record<string, unknown> = {}) => ({
  installation_id: id, app_id: 'demo-app', publisher_id: 'kazuha', version: '1.0.0', display, revision: '1',
  backend: { required: false, registered: false, enabled: false }, recovery: { count: 0, points: [], pending: false }, ...extra,
})

function mount(path = base) {
  const router = createMemoryRouter([{
    path: '/manage/apps/installations/:installation', element: <AppInstallationTabs />,
    children: [
      { path: 'overview', element: <PageStub name="overview page" /> },
      { path: 'admin/*', element: <AppAdminTab /> },
      { path: ':tab', element: <PageStub name="settings page" /> },
    ],
  }, { path: '/manage/apps', element: <div>apps list</div> }], { initialEntries: [path] })
  render(<RouterProvider router={router} />)
  return router
}
const serve = (extra: Record<string, unknown> = {}) => apiMock.get.mockImplementation(async () => settingsOf(extra))
const tabNames = () => screen.getAllByRole('tab').map(tab => tab.textContent)

beforeEach(() => { apiMock.get.mockReset(); mounts.page = 0 })
afterEach(cleanup)

describe('the installation page', () => {
  it('has Overview, Permissions & network and Access for a static app, and no Data tab', async () => {
    serve()
    mount()
    await screen.findByText('overview page')
    expect(tabNames()).toEqual(['appPlatform.tab.overview', 'appPlatform.tab.permissions', 'appPlatform.tab.access'])
  })

  it('adds Data for an app with a backend or data, and the app\'s own page only when its release declares one', async () => {
    serve({ backend: { required: true, registered: true, enabled: true } })
    mount()
    await screen.findByText('overview page')
    expect(tabNames()).toEqual(['appPlatform.tab.overview', 'appPlatform.tab.permissions', 'appPlatform.tab.access', 'appPlatform.tab.data'])
    cleanup()
    serve({ backend: { required: true, registered: true, enabled: true }, admin_page: true })
    mount()
    await screen.findByText('overview page')
    expect(tabNames()).toEqual(['appPlatform.tab.overview', 'appPlatform.tab.permissions', 'appPlatform.tab.access', 'appPlatform.tab.data', 'appAdmin.tab'])
  })

  it('selects the tab from the URL, so each tab can be linked to and back and forward move between them', async () => {
    serve({ backend: { required: true, registered: true, enabled: true }, admin_page: true })
    const router = mount(`${base}/permissions`)
    await screen.findByText('settings page')
    expect(screen.getByRole('tab', { name: 'appPlatform.tab.permissions' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.getByTestId('tab').textContent).toBe('permissions')
    await userEvent.click(screen.getByRole('tab', { name: 'appPlatform.tab.data' }))
    expect(router.state.location.pathname).toBe(`${base}/data`)
    await userEvent.click(screen.getByRole('tab', { name: 'appPlatform.tab.overview' }))
    expect(router.state.location.pathname).toBe(`${base}/overview`)
    await act(async () => { await router.navigate(-1) })
    expect(screen.getByRole('tab', { name: 'appPlatform.tab.data' }).getAttribute('aria-selected')).toBe('true')
    await userEvent.click(screen.getByRole('tab', { name: 'appAdmin.tab' }))
    expect(router.state.location.pathname).toBe(`${base}/admin`)
    expect(screen.getByTestId('surface').textContent).toBe(`admin|${id}|${base}/admin|false`)
  })

  it('keeps the app\'s own page selected on a deeper route of it', async () => {
    serve({ admin_page: true })
    mount(`${base}/admin/runs`)
    expect((await screen.findByRole('tab', { name: 'appAdmin.tab' })).getAttribute('aria-selected')).toBe('true')
  })

  it('sends the bare URL, an unknown tab and a tab the app does not have to the Overview', async () => {
    for (const path of [base, `${base}/nonsense`, `${base}/data`, `${base}/admin`]) {
      serve()
      const router = mount(path)
      expect(await screen.findByText('overview page')).toBeTruthy()
      expect(router.state.location.pathname).toBe(`${base}/overview`)
      cleanup()
    }
  })

  it('shows one breadcrumb and one Back link, with the app name and version in the identity row', async () => {
    serve()
    mount()
    await screen.findByText('overview page')
    expect(document.querySelectorAll('.ant-breadcrumb')).toHaveLength(1)
    expect(screen.getAllByRole('link', { name: /appPlatform\.settings\.back/ })).toHaveLength(1)
    expect(screen.getByRole('heading', { name: 'Demo App' })).toBeTruthy()
    expect(screen.getByText('1.0.0')).toBeTruthy()
  })

  it('renders once: no tab bar and no page while the settings are out', async () => {
    let answer!: (value: unknown) => void
    apiMock.get.mockImplementation(() => new Promise(resolve => { answer = resolve }))
    mount(`${base}/permissions`)
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('tab')).toBeNull()
    expect(screen.queryByText('settings page')).toBeNull()
    await act(async () => answer(settingsOf()))
    expect(await screen.findByText('settings page')).toBeTruthy()
  })

  it('says a failed read with a retry instead of a page without tabs, and a missing installation as not found', async () => {
    apiMock.get.mockImplementation(async () => { throw new Error('down') })
    mount()
    expect(await screen.findByText('down')).toBeTruthy()
    expect(screen.queryByRole('tab')).toBeNull()
    serve()
    await userEvent.click(screen.getByRole('button', { name: /common\.retry/ }))
    expect(await screen.findByText('overview page')).toBeTruthy()
    cleanup()
    apiMock.get.mockImplementation(async () => { throw new ApiError(404, 'missing') })
    mount()
    expect(await screen.findByText('appPlatform.settings.notFound')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /common\.retry/ })).toBeNull()
  })

  it('makes one settings request for the frame and the tab together, and reloads without remounting the tab', async () => {
    let revision = 1
    apiMock.get.mockImplementation(async () => settingsOf({ revision: String(revision++) }))
    mount()
    await screen.findByText('overview page')
    expect(screen.getByTestId('shared').textContent).toBe('1|Demo App')
    expect(apiMock.get.mock.calls.filter(([url]) => url === settingsUrl)).toHaveLength(1)
    await userEvent.click(screen.getByRole('button', { name: 'reload' }))
    await waitFor(() => expect(screen.getByTestId('shared').textContent).toBe('2|Demo App'))
    expect(mounts.page).toBe(1)
    expect(screen.getAllByRole('tab').length).toBeGreaterThan(0)
  })
})
