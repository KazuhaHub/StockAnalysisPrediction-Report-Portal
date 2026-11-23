import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider, useOutletContext } from 'react-router'
import AppInstallationTabs from './AppInstallationTabs'
import AppAdminTab from './AppAdminTab'
import SettingsPage from './AppPlatformSettingsPage'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: apiMock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
const mounts = vi.hoisted(() => ({ settings: 0 }))
vi.mock('./AppPlatformSettingsPage', async () => {
  const { useEffect } = await import('react')
  return { default: function SettingsPageStub() {
    const shared = useOutletContext<{ settings: { admin_page?: boolean; revision?: string } | null; reload: () => void } | undefined>()
    useEffect(() => { mounts.settings++ }, [])
    return <div>settings page<span data-testid="shared">{shared?.settings ? `${String(shared.settings.admin_page)}|${shared.settings.revision ?? ''}` : 'none'}</span><button onClick={() => shared?.reload()}>reload</button><span data-testid="headed">{String((shared as { headed?: boolean } | undefined)?.headed)}</span></div>
  } }
})
vi.mock('../../components/apps/AppSurfaceView', () => ({
  default: (props: { installation: string; surface: string; base: string; crumbTail?: boolean; onBack?: unknown }) =>
    <div data-testid="surface">{`${props.surface}|${props.installation}|${props.base}|${String(props.crumbTail)}|${typeof props.onBack}`}</div>,
}))

const id = '11111111-1111-4111-8111-111111111111'
const base = `/manage/apps/installations/${id}`
const settingsUrl = `/api/admin/app-platform/installations/${id}/settings`

function mount(path = base) {
  const router = createMemoryRouter([{
    path: '/manage/apps/installations/:installation', element: <AppInstallationTabs />,
    children: [{ index: true, element: <SettingsPage /> }, { path: 'admin/*', element: <AppAdminTab /> }],
  }], { initialEntries: [path] })
  render(<RouterProvider router={router} />)
  return router
}
const serve = (admin_page: boolean | undefined) => apiMock.get.mockImplementation(async () => ({ installation_id: id, app_id: 'demo-app', display: { icon: 'dashboard', texts: [{ locale: 'en-US', name: 'Demo App', description: 'd' }] }, ...(admin_page === undefined ? {} : { admin_page }) }))

beforeEach(() => { apiMock.get.mockReset(); mounts.settings = 0 })
afterEach(cleanup)

describe('installation page tabs', () => {
  it('shows both tabs when the app declares an admin page, and the URL selects the tab', async () => {
    serve(true)
    const router = mount()
    const settings = await screen.findByRole('tab', { name: 'appPlatform.settings.tabSettings' })
    const admin = screen.getByRole('tab', { name: 'appAdmin.tab' })
    expect(settings.getAttribute('aria-selected')).toBe('true')
    expect(screen.getByText('settings page')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledWith(settingsUrl, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    await userEvent.click(admin)
    expect(router.state.location.pathname).toBe(`${base}/admin`)
    expect(screen.getByTestId('surface').textContent).toBe(`admin|${id}|${base}/admin|false|undefined`)
    expect(screen.getByRole('tab', { name: 'appAdmin.tab' }).getAttribute('aria-selected')).toBe('true')
    // Back and forward move between the tabs.
    await act(async () => { await router.navigate(-1) })
    expect(screen.getByRole('tab', { name: 'appPlatform.settings.tabSettings' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.getByText('settings page')).toBeTruthy()
    await act(async () => { await router.navigate(1) })
    expect(screen.getByTestId('surface')).toBeTruthy()
  })

  it('keeps the admin tab selected on a deeper route of the admin page', async () => {
    serve(true)
    mount(`${base}/admin/runs`)
    expect((await screen.findByRole('tab', { name: 'appAdmin.tab' })).getAttribute('aria-selected')).toBe('true')
  })

  it('shows no tab bar when the app has no admin page, and sends a direct visit to the admin URL to the settings', async () => {
    for (const value of [false, undefined]) {
      serve(value)
      const router = mount(`${base}/admin`)
      expect(await screen.findByText('settings page')).toBeTruthy()
      expect(screen.queryByRole('tab')).toBeNull()
      expect(router.state.location.pathname).toBe(base)
      cleanup()
    }
  })

  it('renders once: nothing of the page, no tab bar and no admin frame while the settings are out', async () => {
    let answer!: (value: unknown) => void
    apiMock.get.mockImplementation(() => new Promise(resolve => { answer = resolve }))
    mount(`${base}/admin`)
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('tab')).toBeNull()
    expect(screen.queryByTestId('surface')).toBeNull()
    expect(screen.queryByText('settings page')).toBeNull()
    await act(async () => answer({ installation_id: id, admin_page: true }))
    expect(await screen.findByTestId('surface')).toBeTruthy()
    expect(screen.getAllByRole('tab')).toHaveLength(2)
  })

  it('on a failed read shows the page without tabs and leaves the error to the page', async () => {
    apiMock.get.mockImplementation(async () => { throw new Error('down') })
    mount()
    expect(await screen.findByText('settings page')).toBeTruthy()
    expect(screen.queryByRole('tab')).toBeNull()
  })

  it('hands the one loaded settings to the page, so a single request serves both', async () => {
    serve(true)
    mount()
    await screen.findByRole('tab', { name: 'appPlatform.settings.tabSettings' })
    expect(screen.getByTestId('shared').textContent).toBe('true|')
    expect(apiMock.get.mock.calls.filter(([url]) => url === settingsUrl)).toHaveLength(1)
    expect(apiMock.get).toHaveBeenCalledTimes(1)
  })

  it('reloads on the page\'s request without unmounting the page or the tab bar, with one request per reload', async () => {
    let revision = 1
    apiMock.get.mockImplementation(async () => ({ installation_id: id, admin_page: true, revision: String(revision++) }))
    mount()
    await screen.findByRole('tab', { name: 'appAdmin.tab' })
    expect(screen.getByTestId('shared').textContent).toBe('true|1')
    await userEvent.click(screen.getByRole('button', { name: 'reload' }))
    await waitFor(() => expect(screen.getByTestId('shared').textContent).toBe('true|2'))
    expect(apiMock.get).toHaveBeenCalledTimes(2)
    expect(mounts.settings).toBe(1)
    expect(screen.getAllByRole('tab')).toHaveLength(2)
  })

  it('on a failed read gives the page no settings, so it loads and reports its own', async () => {
    apiMock.get.mockImplementation(async () => { throw new Error('down') })
    mount()
    expect(await screen.findByText('settings page')).toBeTruthy()
    expect(screen.getByTestId('shared').textContent).toBe('none')
  })

  it('leads both tabs with one breadcrumb ending in the app name and a Back link, above the tab bar', async () => {
    serve(true)
    for (const path of [base, `${base}/admin`]) {
      const { unmount } = render(<div />)
      unmount()
      cleanup()
      mount(path)
      const crumb = await screen.findByRole('navigation', { name: 'apps.breadcrumb' })
      expect(crumb.textContent).toContain('Demo App')
      const back = screen.getByRole('link', { name: /appPlatform.settings.back/ })
      const tabs = screen.getByRole('tablist')
      expect(crumb.compareDocumentPosition(tabs) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      expect(back.compareDocumentPosition(tabs) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    }
  })

  it('tells the page below that the tabs carry the header', async () => {
    serve(true)
    mount()
    await screen.findByText('settings page')
    expect(screen.getByTestId('headed').textContent).toBe('true')
  })
})
