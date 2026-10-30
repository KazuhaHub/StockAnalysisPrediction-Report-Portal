import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { App, ConfigProvider } from 'antd'
import { MemoryRouter } from 'react-router'
import UsersPage from './UsersPage'
import { mockNarrowViewport } from '../../test/narrow'

// Groups management used to live in a Drawer opened from a toolbar button; it is now
// the second sub-tab. Both tabs must render off a single load.
const listing = vi.hoisted(() => ({
  groups: [] as { id: number; name: string; is_default?: boolean; members: number; weight: number | null }[],
}))
const post = vi.hoisted(() => vi.fn().mockResolvedValue({}))
vi.mock('../../api/client', () => ({
  api: {
    get: (url: string) =>
      Promise.resolve(url === '/api/admin/users' ? { users: [], roles: [], groups: listing.groups, me: '' } : { targets: [], granted: [] }),
    post,
    put: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

describe('UsersPage — accounts / groups sub-tabs', () => {
  it('keeps a pending OU edit until changing units is explicitly confirmed', async () => {
    listing.groups = [
      { id: 1, name: 'Default', is_default: true, members: 0, weight: 2 },
      { id: 7, name: 'Research Desk', members: 3, weight: null },
    ]
    render(<ConfigProvider theme={{ token: { motion: false } }}><MemoryRouter initialEntries={['/manage/users?tab=groups&ou=7']}><App><UsersPage /></App></MemoryRouter></ConfigProvider>)
    const name = await screen.findByDisplayValue('Research Desk')
    fireEvent.change(name, { target: { value: 'Draft desk' } })
    fireEvent.click(within(screen.getByRole('tree')).getByText('Default'))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'manage.stay' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect((screen.getByDisplayValue('Draft desk') as HTMLInputElement).value).toBe('Draft desk')
    fireEvent.click(within(screen.getByRole('tree')).getByText('Default'))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'manage.leave' }))
    await waitFor(() => expect(screen.queryByDisplayValue('Draft desk')).toBeNull())
  })
  it('creates an ordinary OU beneath Default instead of creating another root', async () => {
    listing.groups = [{ id: 1, name: 'Default', is_default: true, members: 0, weight: 2 }]
    render(<MemoryRouter initialEntries={['/manage/users?tab=groups']}><App><UsersPage /></App></MemoryRouter>)
    fireEvent.click(await screen.findByText('ou.add'))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'New team' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/admin/groups', expect.objectContaining({ parent_id: 1 })))
  })
  it('renders both sub-tabs', async () => {
    render(
      <MemoryRouter>
        <App>
          <UsersPage />
        </App>
      </MemoryRouter>,
    )
    expect(await screen.findByText('users.tabAccounts')).toBeTruthy()
    expect(await screen.findByText('users.tabGroups')).toBeTruthy()
  })

  it('offers an account-validity field in the add-user form', async () => {
    render(
      <MemoryRouter>
        <App>
          <UsersPage />
        </App>
      </MemoryRouter>,
    )
    fireEvent.click(await screen.findByText('users.add'))
    // The add/edit modal must carry the account expiry (有效期) control (ADR 0022 R4). Assert on the
    // form-only hint so it can't be satisfied by the same-labelled table column header.
    expect(await screen.findByText('users.expiresHint')).toBeTruthy()
  })

  // The forecast access summary links straight to the OU editor, and to the unit it is about.
  it('opens on the groups tab, with the named unit selected, when linked there', async () => {
    listing.groups = [
      { id: 1, name: 'Default', is_default: true, members: 0, weight: 2 },
      { id: 7, name: 'Research Desk', members: 3, weight: null },
    ]
    render(
      <MemoryRouter initialEntries={['/manage/users?tab=groups&ou=7']}>
        <App>
          <UsersPage />
        </App>
      </MemoryRouter>,
    )
    expect(await screen.findByDisplayValue('Research Desk')).toBeTruthy()
    expect(screen.getByRole('tab', { name: 'users.tabGroups' }).getAttribute('aria-selected')).toBe('true')
  })

  it('falls back to the Default unit when the linked unit does not exist', async () => {
    listing.groups = [{ id: 1, name: 'Default', is_default: true, members: 0, weight: 2 }]
    render(
      <MemoryRouter initialEntries={['/manage/users?tab=groups&ou=999']}>
        <App>
          <UsersPage />
        </App>
      </MemoryRouter>,
    )
    expect(await screen.findByDisplayValue('Default')).toBeTruthy()
  })

  it('keeps the OU directory focused on structure instead of hosting feature policy editors', async () => {
    listing.groups = [{ id: 1, name: 'Default', is_default: true, members: 0, weight: 2 }]
    const { container } = render(
      <MemoryRouter initialEntries={['/manage/users?tab=groups']}>
        <App>
          <UsersPage />
        </App>
      </MemoryRouter>,
    )
    await screen.findByDisplayValue('Default')
    expect(container.querySelector('.rp-ou-console')).toBeTruthy()
    expect(container.querySelector('.rp-ou-policy-grid')).toBeNull()
    expect(container.querySelector('.rp-ou-global-settings')).toBeNull()
    expect(screen.queryByText('ou.sectionSecurity')).toBeNull()
    expect(screen.queryByText('users.urgentTitle')).toBeNull()
  })

  it('opens on the accounts tab when no tab is asked for', async () => {
    listing.groups = []
    render(
      <MemoryRouter>
        <App>
          <UsersPage />
        </App>
      </MemoryRouter>,
    )
    expect((await screen.findByRole('tab', { name: 'users.tabAccounts' })).getAttribute('aria-selected')).toBe('true')
  })
})

describe('UsersPage on a phone', () => {
  mockNarrowViewport()
  const open = (path: string) => render(<ConfigProvider theme={{ token: { motion: false } }}><MemoryRouter initialEntries={[path]}><App><UsersPage /></App></MemoryRouter></ConfigProvider>)
  const units = () => { listing.groups = [{ id: 1, name: 'Default', is_default: true, members: 0, weight: 2 }, { id: 7, name: 'Research Desk', members: 3, weight: null }] }

  it('folds the account filter tree and keeps one collapse control out of it', async () => {
    units()
    open('/manage/users')
    const toggle = await screen.findByRole('button', { name: /ou.title/ })
    expect(screen.queryByRole('tree')).toBeNull()
    fireEvent.click(toggle)
    expect(await screen.findByRole('tree')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'ou.collapse' })).toBeNull()
    fireEvent.click(within(screen.getByRole('tree')).getByText('Default'))
    await waitFor(() => expect(screen.queryByRole('tree')).toBeNull())
    expect(document.activeElement).toBe(toggle)
  })
  it('folds the OU directory tree after a pick', async () => {
    units()
    open('/manage/users?tab=groups&ou=7')
    await screen.findByDisplayValue('Research Desk')
    expect(screen.queryByRole('tree')).toBeNull()
    const toggle = screen.getByRole('button', { name: /Research Desk/ })
    fireEvent.click(toggle)
    expect(screen.queryByRole('button', { name: 'ou.collapse' })).toBeNull()
    fireEvent.click(within(await screen.findByRole('tree')).getByText('Default'))
    await waitFor(() => expect(screen.queryByDisplayValue('Research Desk')).toBeNull())
    expect(screen.queryByRole('tree')).toBeNull()
    const after = screen.getByRole('button', { name: /Default/ })
    expect(after.getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(after)
  })
})
