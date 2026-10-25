import { App, ConfigProvider } from 'antd'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AdminDraftBoundary from './AdminDraftBoundary'
import { PolicyPage } from './OUPolicyWorkspace'

const api = vi.hoisted(() => ({ get: vi.fn(), patch: vi.fn() }))
vi.mock('../../api/client', () => ({ api, errText: (error: unknown) => String(error) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
beforeEach(() => {
  api.get.mockReset().mockResolvedValue({ groups: [
    { id: 1, name: 'Default', is_default: true, members: 0 },
    { id: 7, name: 'Research', parent_id: 1, members: 2 },
  ] })
  api.patch.mockReset().mockResolvedValue({})
})
function mount(entry = '/manage/security?scope=ou&ou=7') {
  const router = createMemoryRouter([{ path: '*', element: <ConfigProvider theme={{ token: { motion: false } }}><App>
    <AdminDraftBoundary><PolicyPage section="security"><div>Global security controls</div></PolicyPage></AdminDraftBoundary>
  </App></ConfigProvider> }], { initialEntries: [entry] })
  render(<RouterProvider router={router} />)
  return router
}
describe('Feature-owned organizational policy', () => {
  it('edits only security fields without loading or writing the directory and run settings', async () => {
    mount()
    await screen.findByText('ou.sectionSecurity')
    expect(screen.queryByText('ou.sectionRuns')).toBeNull()
    expect(screen.queryByRole('button', { name: 'ou.add' })).toBeNull()
    expect(screen.queryByRole('textbox', { name: 'users.groupName' })).toBeNull()
    fireEvent.click(screen.getAllByRole('button', { name: 'ou.override' })[0])
    fireEvent.click(screen.getByRole('switch'))
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(api.patch).toHaveBeenCalledWith('/api/admin/groups/7', { totp_enroll: false, passkey_enroll: null, require_2fa: null }))
    expect(api.get.mock.calls.every(([url]) => url === '/api/admin/groups')).toBe(true)
  })
  it('guards an OU scope change and discards only after explicit confirmation', async () => {
    const router = mount()
    await screen.findByText('ou.sectionSecurity')
    fireEvent.click(screen.getAllByRole('button', { name: 'ou.override' })[0])
    fireEvent.click(within(screen.getByRole('tree')).getByText('Default'))
    fireEvent.click(await screen.findByRole('button', { name: 'manage.stay' }))
    expect(router.state.location.search).toContain('ou=7')
    await act(async () => { await router.navigate('/manage/security?scope=ou&ou=1') })
    fireEvent.click(await screen.findByRole('button', { name: 'manage.leave' }))
    await waitFor(() => expect(router.state.location.search).toContain('ou=1'))
    expect(api.patch).not.toHaveBeenCalled()
  })
  it('does not substitute Default for an unknown OU link', async () => {
    mount('/manage/security?scope=ou&ou=999')
    await screen.findByText('ou.scopeMissing')
    expect(screen.queryByRole('button', { name: 'common.save' })).toBeNull()
  })
})
