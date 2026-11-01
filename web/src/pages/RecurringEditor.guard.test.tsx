import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App, ConfigProvider } from 'antd'
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router'
import RecurringEditor from './RecurringEditor'
import AdminDraftBoundary from './manage/AdminDraftBoundary'

// The editor with the real draft boundary around it, wired the way App.tsx wires the route: the
// unit tests mock the boundary away, so this is where leaving with unsaved edits is checked end to end.

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: true }) }))

const task = { id: 7, name: 'Close review', target_id: 1, concurrency: 1, priority: '', max_retries: 2, freq: 'daily', at_time: '09:30', weekday: 1, monthday: 1, enabled: true }

function mount() {
  const router = createMemoryRouter(
    [
      {
        path: '/apps/recurring',
        element: (
          <ConfigProvider theme={{ token: { motion: false } }}>
            <App>
              <AdminDraftBoundary>
                <Outlet />
              </AdminDraftBoundary>
            </App>
          </ConfigProvider>
        ),
        children: [
          { index: true, element: <div>list-page</div> },
          { path: ':id', element: <RecurringEditor /> },
        ],
      },
    ],
    { initialEntries: ['/apps/recurring', '/apps/recurring/7'], initialIndex: 1 },
  )
  render(<RouterProvider router={router} />)
  return router
}
const nameField = () => screen.findByPlaceholderText('recurring.namePlaceholder') as Promise<HTMLInputElement>

beforeEach(() => {
  for (const fn of Object.values(apiMock)) fn.mockReset()
  apiMock.get.mockImplementation((url: string) => {
    if (url.includes('/batch/targets')) return Promise.resolve({ targets: [{ id: 1, plugin_slug: 'dify', name: 'Daily Review', created_at: '', inputs: [{ key: 'code' }] }] })
    if (url === '/api/admin/batch/recurring') return Promise.resolve({ tasks: [], maxConcurrency: 8 })
    return Promise.resolve({ ...task, rows: [{ code: '600000' }], history: [] })
  })
  apiMock.put.mockResolvedValue({ ok: true })
})

describe('RecurringEditor leave guard', () => {
  it('leaves a clean editor without asking', async () => {
    const router = mount()
    await nameField()
    fireEvent.click(screen.getByRole('button', { name: /common\.back/ }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/apps/recurring'))
    expect(screen.queryByRole('button', { name: 'manage.stay' })).toBeNull()
  })

  it('asks before leaving an edited task, keeps the edits on stay and leaves on confirm', async () => {
    const router = mount()
    const name = await nameField()
    fireEvent.change(name, { target: { value: 'Renamed review' } })
    fireEvent.click(screen.getByRole('button', { name: /common\.back/ }))
    fireEvent.click(await screen.findByRole('button', { name: 'manage.stay' }))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'manage.stay' })).toBeNull())
    expect(router.state.location.pathname).toBe('/apps/recurring/7')
    expect(name.value).toBe('Renamed review')

    fireEvent.click(screen.getByRole('button', { name: /common\.back/ }))
    fireEvent.click(await screen.findByRole('button', { name: 'manage.leave' }))
    expect(await screen.findByText('list-page')).toBeTruthy()
  })

  it('guards the browser back button as well', async () => {
    const router = mount()
    fireEvent.change(await nameField(), { target: { value: 'Renamed review' } })
    router.navigate(-1).catch(() => {})
    expect(await screen.findByRole('button', { name: 'manage.stay' })).toBeTruthy()
  })

  it('returns to the list after a successful save without asking about the edits it just saved', async () => {
    const router = mount()
    fireEvent.change(await nameField(), { target: { value: 'Renamed review' } })
    await waitFor(() => expect(screen.getAllByRole('textbox', { name: /batch\.editor\.cellLabel/ })).toHaveLength(2))
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('list-page')).toBeTruthy()
    expect(router.state.location.pathname).toBe('/apps/recurring')
    expect(screen.queryByRole('button', { name: 'manage.stay' })).toBeNull()
  })
})
