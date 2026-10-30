import { App, ConfigProvider } from 'antd'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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
  it('keeps the unit rail visible alongside global settings instead of hiding it in a tab', async () => {
    const router = mount('/manage/security')
    expect(screen.getByText('Global security controls')).toBeTruthy()
    expect(screen.queryByRole('tab')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /ou.globalScope/ }))
    expect(router.state.location.search).toBe('')
    fireEvent.click(within(await screen.findByRole('tree')).getByText('Research'))
    await waitFor(() => expect(router.state.location.search).toContain('ou=7'))
    expect(await screen.findByText('ou.sectionSecurity')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /ou.globalScope/ }))
    expect(await screen.findByText('Global security controls')).toBeTruthy()
  })
  it('collapses the whole scope rail and keeps both scope controls in one header', async () => {
    mount('/manage/security')
    await screen.findByRole('tree')
    fireEvent.click(screen.getByRole('button', {name:'ou.collapse'}))
    const expand = screen.getByRole('button', {name:'ou.expand'})
    expect(expand.parentElement).toBe(screen.getByRole('button', {name:'ou.globalScope'}).parentElement)
    expect(screen.queryByRole('tree')).toBeNull()
    expect(expand.closest('.rp-policy-workspace--collapsed')).toBeTruthy()
    fireEvent.click(expand)
    expect(await screen.findByRole('tree')).toBeTruthy()
  })
  it('does not hide global settings when the unit directory cannot be read', async () => {
    api.get.mockRejectedValue(new Error('Directory unavailable'))
    mount('/manage/security')
    await screen.findByText('common.loadFailed')
    expect(screen.getByText('Global security controls')).toBeTruthy()
    expect(screen.getByRole('button', { name: /common.retry/ })).toBeTruthy()
  })
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

describe('Scope rail across breakpoints', () => {
  const realMatchMedia = window.matchMedia
  let listeners: Array<() => void> = []
  let matches = true
  beforeEach(() => {
    matches = true
    listeners = []
    Object.defineProperty(window, 'matchMedia', { configurable: true, value: (query: string) => ({
      get matches() { return matches && query.includes('767') }, media: query,
      addEventListener: (_: string, fn: () => void) => { listeners.push(fn) }, removeEventListener: () => {},
    }) })
  })
  afterEach(() => { Object.defineProperty(window, 'matchMedia', { configurable: true, value: realMatchMedia }) })
  const toggle = () => screen.getByRole('button', { name: /Research|Default|ou.unitScope/ })

  it('starts compact, names the current unit, and reports its state with aria-expanded', async () => {
    mount('/manage/security?scope=ou&ou=7')
    await screen.findByText('ou.sectionSecurity')
    expect(screen.queryByRole('tree')).toBeNull()
    expect(toggle().getAttribute('aria-expanded')).toBe('false')
    expect(toggle().getAttribute('aria-controls')).toBe(document.querySelector('.rp-policy-scope [hidden]')?.id)
    fireEvent.click(toggle())
    expect(toggle().getAttribute('aria-expanded')).toBe('true')
    expect(await screen.findByRole('tree')).toBeTruthy()
  })
  it('folds back after choosing a unit, including the one already shown', async () => {
    const router = mount('/manage/security?scope=ou&ou=1')
    await screen.findByText('ou.sectionSecurity')
    fireEvent.click(toggle())
    fireEvent.click(within(await screen.findByRole('tree')).getByText('Research'))
    await waitFor(() => expect(router.state.location.search).toContain('ou=7'))
    await waitFor(() => expect(screen.queryByRole('tree')).toBeNull())
    fireEvent.click(toggle())
    fireEvent.click(within(await screen.findByRole('tree')).getByText('Research'))
    await waitFor(() => expect(screen.queryByRole('tree')).toBeNull())
  })
  it('switches to the desktop rail when the viewport widens while open, and desktop collapse still works', async () => {
    mount('/manage/security?scope=ou&ou=7')
    await screen.findByText('ou.sectionSecurity')
    fireEvent.click(toggle())
    expect(await screen.findByRole('tree')).toBeTruthy()
    matches = false
    act(() => { listeners.forEach((fn) => fn()) })
    expect(await screen.findByRole('tree')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'ou.collapse' }))
    expect(screen.queryByRole('tree')).toBeNull()
    expect(document.querySelector('.rp-policy-workspace--collapsed')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'ou.expand' }))
    expect(await screen.findByRole('tree')).toBeTruthy()
  })
  it('hands focus back to the toggle after a pick, even though the pick remounts the workspace', async () => {
    mount('/manage/security?scope=ou&ou=1')
    await screen.findByText('ou.sectionSecurity')
    fireEvent.click(toggle())
    fireEvent.click(within(await screen.findByRole('tree')).getByText('Research'))
    await waitFor(() => expect(screen.queryByRole('tree')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(toggle()))
  })
  it('keeps the rail mounted across a pick and leaves focus on Global when Global is chosen', async () => {
    mount('/manage/security?scope=ou&ou=1')
    await screen.findByText('ou.sectionSecurity')
    const before = toggle()
    fireEvent.click(before)
    fireEvent.click(within(await screen.findByRole('tree')).getByText('Research'))
    await waitFor(() => expect(screen.queryByRole('tree')).toBeNull())
    expect(toggle()).toBe(before)
    const global = screen.getByRole('button', { name: /ou.globalScope/ })
    global.focus()
    fireEvent.click(global)
    expect(await screen.findByText('Global security controls')).toBeTruthy()
    expect(document.activeElement).toBe(global)
  })
})
