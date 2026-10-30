import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter, Route, Routes } from 'react-router'
import AppAccessPage from './AppAccessPage'
import { groups, layout } from './appsAdminFixtures'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const renderAt = (path: string) =>
  render(
    <App>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/manage/apps/:key/access" element={<AppAccessPage />} />
        </Routes>
      </MemoryRouter>
    </App>,
  )

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.put.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/groups') return { groups }
    if (url === '/api/admin/apps/layout') return { apps: layout() }
    if (url === '/api/apps') return { apps: [] }
    throw new Error(`unexpected ${url}`)
  })
  apiMock.put.mockResolvedValue({})
})

const heading = (name: string) => screen.findByRole('heading', { name })
const save = () => screen.getByRole('button', { name: 'common.save' })
const inherit = () => screen.getByRole('button', { name: 'apps.access.inherit' })

describe('App service status', () => {
  it('opens on the Default OU and says the value comes from the system default', async () => {
    renderAt('/manage/apps/builtin%3Aquotes/access')
    expect(await heading('nav.quotes · Default')).toBeTruthy()
    expect(screen.getByText('apps.access.source.system')).toBeTruthy()
    expect((screen.getByRole('radio', { name: /apps\.access\.on$/ }) as HTMLInputElement).checked).toBe(true)
  })

  it('shows where a child OU inherits from, and what is overridden in the tree', async () => {
    const user = userEvent.setup()
    // Hello is closed on Default and inherited as closed on Research.
    renderAt('/manage/apps/hello/access')
    await heading('Hello · Default')
    await user.click(screen.getByText('Research'))
    await heading('Hello · Research')
    expect(screen.getByText('apps.access.source.inherited:{"from":"Default"}')).toBeTruthy()
    expect((screen.getByRole('radio', { name: /apps\.access\.off$/ }) as HTMLInputElement).checked).toBe(true)
  })

  it('labels the OUs that override the app', async () => {
    renderAt('/manage/apps/builtin%3Aquotes/access')
    await heading('nav.quotes · Default')
    // Research carries its own closed value for quotes; Default is only inheriting.
    expect(screen.getAllByText('apps.access.overridden')).toHaveLength(1)
  })

  it('marks the source as overridden on an OU with its own value, and offers Inherit there', async () => {
    const user = userEvent.setup()
    renderAt('/manage/apps/builtin%3Aquotes/access')
    await heading('nav.quotes · Default')
    await user.click(screen.getByText('Research'))
    await heading('nav.quotes · Research')
    expect(screen.getByText('apps.access.source.overridden')).toBeTruthy()
    expect((inherit() as HTMLButtonElement).disabled).toBe(false)
  })

  it('while inheriting, shows the effective value but needs a choice before Save is enabled', async () => {
    const user = userEvent.setup()
    renderAt('/manage/apps/builtin%3Aquotes/access')
    await heading('nav.quotes · Default')
    expect((screen.getByRole('radio', { name: /apps\.access\.on$/ }) as HTMLInputElement).checked).toBe(true)
    expect((save() as HTMLButtonElement).disabled).toBe(true)
    // Picking the value already in effect still pins it as an explicit override.
    await user.click(screen.getByRole('radio', { name: /apps\.access\.on$/ }))
    expect((save() as HTMLButtonElement).disabled).toBe(false)
    await user.click(save())
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/apps/builtin%3Aquotes/access/1', { enabled: true }))
  })

  it('with its own value, enables Save only when the choice differs from that value', async () => {
    const user = userEvent.setup()
    renderAt('/manage/apps/builtin%3Aquotes/access')
    await heading('nav.quotes · Default')
    await user.click(screen.getByText('Research')) // overrides quotes as closed
    await heading('nav.quotes · Research')
    expect((screen.getByRole('radio', { name: /apps\.access\.off$/ }) as HTMLInputElement).checked).toBe(true)
    expect((save() as HTMLButtonElement).disabled).toBe(true)
    await user.click(screen.getByRole('radio', { name: /apps\.access\.on$/ }))
    expect((save() as HTMLButtonElement).disabled).toBe(false)
    await user.click(screen.getByRole('radio', { name: /apps\.access\.off$/ }))
    expect((save() as HTMLButtonElement).disabled).toBe(true)
  })

  it('saves the chosen value explicitly for the selected OU', async () => {
    const user = userEvent.setup()
    renderAt('/manage/apps/builtin%3Aquotes/access')
    await heading('nav.quotes · Default')
    await user.click(screen.getByRole('radio', { name: /apps\.access\.off$/ }))
    expect(apiMock.put).not.toHaveBeenCalled()
    await user.click(save())
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/apps/builtin%3Aquotes/access/1', { enabled: false }))
  })

  it('sends null to inherit again', async () => {
    const user = userEvent.setup()
    renderAt('/manage/apps/builtin%3Aquotes/access')
    await heading('nav.quotes · Default')
    await user.click(screen.getByText('Research'))
    await heading('nav.quotes · Research')
    await user.click(inherit())
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/apps/builtin%3Aquotes/access/2', { enabled: null }))
  })

  it('disables Inherit where the OU already inherits', async () => {
    renderAt('/manage/apps/builtin%3Aquotes/access')
    await heading('nav.quotes · Default')
    expect((inherit() as HTMLButtonElement).disabled).toBe(true)
  })

  it('warns that a built-in app is only hidden, not blocked', async () => {
    renderAt('/manage/apps/builtin%3Aquotes/access')
    await heading('nav.quotes · Default')
    expect(screen.getByText('apps.access.builtinNote')).toBeTruthy()
  })

  it('does not show that note for an installed app', async () => {
    renderAt('/manage/apps/hello/access')
    await heading('Hello · Default')
    expect(screen.queryByText('apps.access.builtinNote')).toBeNull()
  })

  it('links back through the breadcrumb', async () => {
    renderAt('/manage/apps/hello/access')
    await heading('Hello · Default')
    const crumbs = screen.getByRole('navigation', { name: 'apps.breadcrumb' })
    expect(crumbs.textContent).toContain('apps.title')
    expect(crumbs.textContent).toContain('Hello')
    expect(crumbs.textContent).toContain('apps.section.status.title')
  })
})
