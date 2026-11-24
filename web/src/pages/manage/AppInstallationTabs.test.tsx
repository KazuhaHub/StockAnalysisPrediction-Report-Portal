import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect } from 'react'
import { createMemoryRouter, RouterProvider, useOutletContext, useParams } from 'react-router'
import { ApiError } from '../../api/client'
import AppInstallationTabs from './AppInstallationTabs'
import AppAdminTab from './AppAdminTab'
import { resetCatalogueHint } from './useUpdateHint'

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
  installation_id: id, app_id: 'demo-app', publisher_id: 'kazuha', version: '1.0.0', display, revision: '1', phase: 'ready', desired_state: 'enabled', repair_eligible: false, configuration_gaps: [],
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

beforeEach(() => { apiMock.get.mockReset(); apiMock.post.mockReset(); resetCatalogueHint(); mounts.page = 0 })
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
    apiMock.get.mockImplementation(async (url: string) => url === settingsUrl ? settingsOf({ revision: String(revision++) }) : { items: [] })
    mount()
    await screen.findByText('overview page')
    expect(screen.getByTestId('shared').textContent).toBe('1|Demo App')
    expect(apiMock.get.mock.calls.filter(([url]) => url === settingsUrl)).toHaveLength(1)
    await userEvent.click(screen.getByRole('button', { name: 'reload' }))
    await waitFor(() => expect(screen.getByTestId('shared').textContent).toBe('2|Demo App'))
    expect(mounts.page).toBe(1)
    expect(screen.getAllByRole('tab').length).toBeGreaterThan(0)
  })

  it('shows the app\'s description under its name, on one truncated line', async () => {
    serve()
    mount()
    await screen.findByText('overview page')
    expect(screen.getByText('d').className).toContain('rp-installation-description')
  })

  it('keeps the query string when it redirects to the Overview', async () => {
    serve()
    const router = mount(`${base}/nonsense?operation=abc`)
    await screen.findByText('overview page')
    expect(router.state.location.pathname).toBe(`${base}/overview`)
    expect(router.state.location.search).toBe('?operation=abc')
  })

  it('keeps the page, its tabs and the last good settings when a quiet re-read fails, and says so with a retry', async () => {
    let fail = false
    apiMock.get.mockImplementation(async () => { if (fail) throw new Error('refresh down'); return settingsOf({ revision: '1' }) })
    mount()
    await screen.findByText('overview page')
    fail = true
    await userEvent.click(screen.getByRole('button', { name: 'reload' }))
    expect(await screen.findByText('refresh down')).toBeTruthy()
    expect(screen.getByText('appPlatform.settings.refreshFailed')).toBeTruthy()
    expect(screen.getByText('overview page')).toBeTruthy()
    expect(screen.getByTestId('shared').textContent).toBe('1|Demo App')
    expect(screen.getAllByRole('tab').length).toBeGreaterThan(0)
    fail = false
    await userEvent.click(screen.getByRole('button', { name: /common\.retry/ }))
    await waitFor(() => expect(screen.queryByText('refresh down')).toBeNull())
  })

  it('re-reads the settings when an operation is seen finishing, with the dialog closed: the header offers Enable and shows the new version', async () => {
    let finished = false
    const row = (stage: string, terminal: boolean) => ({ operation_id: '22222222-2222-4222-8222-222222222222', installation_id: id, action: 'disable', stage, managed: false, revision: '3', terminal, created_ms: '1000', updated_ms: '2000', ...(terminal ? { completed_ms: '3000' } : {}) })
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.includes('/operations?')) return { items: [finished ? row('complete', true) : row('revoke', false)] }
      return finished ? settingsOf({ version: '1.1.0', phase: 'disabled', desired_state: 'disabled' }) : settingsOf()
    })
    mount()
    await screen.findByText('overview page')
    expect(screen.getByRole('button', { name: 'appStore.disable' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'appStore.enable' })).toBeNull()
    finished = true
    // No dialog is open: the feed alone sees the operation end.
    expect(await screen.findByRole('button', { name: 'appStore.enable' }, { timeout: 8000 })).toBeTruthy()
    expect(screen.getByText('1.1.0')).toBeTruthy()
  })

  it('tags a newer release next to the version and offers Update to it, without waiting for the catalogue', async () => {
    const sha = (c: string) => c.repeat(64)
    apiMock.get.mockImplementation(async (url: string) => url.endsWith('/market/sources') ? { official: { name: 'Hub', url: 'https://market.example/index.json', enabled: true } } : url.includes('/operations?') ? { items: [] } : settingsOf())
    apiMock.post.mockResolvedValue({ catalogue_sha256: sha('9'), publisher_verified: false, releases: [], apps: [{ publisher_id: 'kazuha', app_id: 'demo-app', publisher_trusted: true, versions: [{ version: '1.2.0', descriptor_sha256: sha('b') }, { version: '1.0.0', descriptor_sha256: sha('a') }] }] })
    mount()
    // The page is up first, without the hint.
    await screen.findByText('overview page')
    expect(await screen.findByText('appStore.updateAvailable')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'appStore.updateTo' })).toBeTruthy()
  })

  it('shows neither the tag nor an Update button when the catalogue fails, and no error', async () => {
    apiMock.get.mockImplementation(async (url: string) => url.endsWith('/market/sources') ? { official: { name: 'Hub', url: 'https://market.example/index.json', enabled: true } } : url.includes('/operations?') ? { items: [] } : settingsOf())
    apiMock.post.mockRejectedValue(new Error('catalogue down'))
    mount()
    await screen.findByText('overview page')
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(screen.queryByText('appStore.updateAvailable')).toBeNull()
    expect(screen.queryByRole('button', { name: 'appStore.updateTo' })).toBeNull()
    expect(screen.queryByText('catalogue down')).toBeNull()
  })
})
