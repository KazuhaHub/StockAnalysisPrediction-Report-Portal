import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import AppAdministratorsCard from './AppAdministratorsCard'
import { ApiError } from '../../api/client'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), del: vi.fn() }))
const notify = vi.hoisted(() => vi.fn())
vi.mock('../../api/client', async original => ({
  ...await original<typeof import('../../api/client')>(),
  api: apiMock,
  errText: (failure: unknown, t: (key: string) => string) => {
    const code = (failure as { code?: string }).code
    return code ? t(`err.${code}`) : 'failed'
  },
}))
vi.mock('../../lib/notify', () => ({ notifyError: notify }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
const translate = vi.hoisted(() => (key: string, options?: Record<string, unknown>) => options ? `${key}:${JSON.stringify(options)}` : key)
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: translate }) }))

const id = '11111111-1111-4111-8111-111111111111'
const url = `/api/admin/app-platform/installations/${id}/app-admins`
const ada = { username: 'ada', displayName: 'Ada Lovelace', grantedBy: 'root', grantedAt: 1767268800000 }
const users = { users: [
  { username: 'ada', display_name: 'Ada Lovelace', role: 'user', active: true, primary_group: 0 },
  { username: 'grace', display_name: 'Grace Hopper', role: 'user', active: true, primary_group: 0 },
  { username: 'old', display_name: 'Old Account', role: 'user', active: false, primary_group: 0 },
], me: 'root', roles: [], groups: [] }

function mount(adminPage?: boolean) {
  return render(<App><AppAdministratorsCard installation={id} adminPage={adminPage} /></App>)
}
function serve(admins: unknown[]) {
  apiMock.get.mockImplementation(async (path: string) => path === url ? { admins } : users)
}
beforeEach(() => { apiMock.get.mockReset(); apiMock.post.mockReset(); apiMock.del.mockReset(); notify.mockReset() })
afterEach(cleanup)

describe('app administrators card', () => {
  it('lists the delegated users with who granted it and when, and says Portal administrators always manage the app', async () => {
    serve([ada])
    mount()
    expect(await screen.findByText('Ada Lovelace')).toBeTruthy()
    expect(screen.getByText('ada')).toBeTruthy()
    expect(screen.getByText('root')).toBeTruthy()
    expect(screen.getByText(/2026/)).toBeTruthy()
    expect(screen.getByText('appAdmin.admins.portalAdminsNote')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledWith(url, expect.objectContaining({ signal: expect.any(AbortSignal) }))
  })

  it('for a version without an admin page says so, still lists stale delegations, lets them be revoked, and offers no add', async () => {
    serve([ada])
    apiMock.del.mockResolvedValue(undefined)
    mount(false)
    expect(await screen.findByText('Ada Lovelace')).toBeTruthy()
    expect(screen.getByText('appAdmin.admins.noAdminPage')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /appAdmin.admins.add/ })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /appAdmin.admins.remove/ }))
    await userEvent.click(await screen.findByRole('button', { name: 'appAdmin.admins.removeAction' }))
    await waitFor(() => expect(apiMock.del).toHaveBeenCalledWith(`${url}/ada`))
  })

  it('with an admin page does not show the no-admin-page note', async () => {
    serve([ada])
    mount(true)
    await screen.findByText('Ada Lovelace')
    expect(screen.queryByText('appAdmin.admins.noAdminPage')).toBeNull()
    expect(screen.getByRole('button', { name: /appAdmin.admins.add/ })).toBeTruthy()
  })

  it('shows a loading state, then an empty state when nobody was delegated', async () => {
    let answer!: (value: unknown) => void
    apiMock.get.mockImplementation(() => new Promise(resolve => { answer = resolve }))
    mount()
    expect(screen.getByText('common.loading')).toBeTruthy()
    expect(screen.queryByText('appAdmin.admins.empty')).toBeNull()
    answer({ admins: [] })
    expect(await screen.findByText('appAdmin.admins.empty')).toBeTruthy()
  })

  it('says the list failed to load and retries', async () => {
    apiMock.get.mockImplementationOnce(async () => { throw new ApiError(500, 'down') })
    mount()
    expect(await screen.findByText('common.loadFailed')).toBeTruthy()
    serve([ada])
    await userEvent.click(screen.getByRole('button', { name: /common.retry/ }))
    expect(await screen.findByText('Ada Lovelace')).toBeTruthy()
  })

  it('adds an active user who is not an administrator yet, through the user picker', async () => {
    serve([ada])
    apiMock.post.mockResolvedValue({ admin: { username: 'grace', displayName: 'Grace Hopper', grantedBy: 'root', grantedAt: 1767268900000 } })
    mount()
    await screen.findByText('Ada Lovelace')
    await userEvent.click(screen.getByRole('button', { name: /appAdmin.admins.add/ }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('combobox'))
    // Already an administrator, and a disabled account, are not offered.
    await waitFor(() => expect(screen.getAllByText('Grace Hopper (grace)').length).toBeGreaterThan(0))
    expect(screen.queryByText('Old Account (old)')).toBeNull()
    expect(document.querySelectorAll('.ant-select-item-option').length).toBe(1)
    await userEvent.click(screen.getAllByText('Grace Hopper (grace)').pop()!)
    await userEvent.click(within(dialog).getByRole('button', { name: 'appAdmin.admins.addAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(url, { username: 'grace' }))
    expect(await screen.findByText('Grace Hopper')).toBeTruthy()
  })

  it('names the missing account when the server answers user-not-found, and keeps the dialog open', async () => {
    serve([])
    apiMock.post.mockImplementation(async () => { throw new ApiError(404, 'user not found', 'app_platform_user_not_found') })
    mount()
    await screen.findByText('appAdmin.admins.empty')
    await userEvent.click(screen.getByRole('button', { name: /appAdmin.admins.add/ }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('combobox'))
    await userEvent.click((await screen.findAllByText('Grace Hopper (grace)')).pop()!)
    await userEvent.click(within(dialog).getByRole('button', { name: 'appAdmin.admins.addAction' }))
    expect(await within(dialog).findByText('err.app_platform_user_not_found')).toBeTruthy()
    expect(screen.getByRole('dialog')).toBeTruthy()
  })

  it('removes an administrator only through a confirmation that names the user', async () => {
    serve([ada])
    apiMock.del.mockResolvedValue(undefined)
    mount()
    await screen.findByText('Ada Lovelace')
    await userEvent.click(screen.getByRole('button', { name: /appAdmin.admins.remove/ }))
    expect(apiMock.del).not.toHaveBeenCalled()
    expect(await screen.findByText(/appAdmin.admins.removeTitle:.*Ada Lovelace/)).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'appAdmin.admins.removeAction' }))
    await waitFor(() => expect(apiMock.del).toHaveBeenCalledWith(`${url}/ada`))
    await waitFor(() => expect(screen.queryByText('Ada Lovelace')).toBeNull())
    expect(screen.getByText('appAdmin.admins.empty')).toBeTruthy()
  })

  it('reports a failed removal and keeps the row', async () => {
    serve([ada])
    apiMock.del.mockImplementation(async () => { throw new ApiError(500, 'boom') })
    mount()
    await screen.findByText('Ada Lovelace')
    await userEvent.click(screen.getByRole('button', { name: /appAdmin.admins.remove/ }))
    await userEvent.click(await screen.findByRole('button', { name: 'appAdmin.admins.removeAction' }))
    await waitFor(() => expect(notify).toHaveBeenCalled())
    expect(screen.getByText('Ada Lovelace')).toBeTruthy()
  })
})
