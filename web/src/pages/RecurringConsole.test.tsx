import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import RecurringConsole from './RecurringConsole'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
}))

vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: true }) }))

const targets = { targets: [{ id: 1, plugin_slug: 'dify', name: 'Daily Review', created_at: '', inputs: [{ key: 'code' }] }] }
const tasks = {
  tasks: [
    {
      id: 7,
      name: 'Close review',
      target_id: 1,
      target_name: 'Daily Review',
      concurrency: 1,
      priority: '',
      max_retries: 2,
      freq: 'daily',
      at_time: '09:30',
      weekday: 1,
      monthday: 1,
      enabled: true,
      created_by: 'alice',
      created_at: '2026-07-13 08:00:00',
      last_fired: '2026-07-12',
      row_count: 3,
      next_run: '2026-07-14 09:30:00',
    },
  ],
}

function Where() {
  const loc = useLocation()
  return <div data-testid="where" data-crumb={(loc.state as { crumb?: string } | null)?.crumb ?? ''}>{loc.pathname}</div>
}

function renderPage() {
  return render(
    <App>
      <MemoryRouter initialEntries={['/apps/recurring']}>
        <Where />
        <Routes>
          <Route path="/apps/recurring" element={<RecurringConsole />} />
          <Route path="/apps/recurring/new" element={<div>new-page</div>} />
          <Route path="/apps/recurring/:id" element={<div>edit-page</div>} />
        </Routes>
      </MemoryRouter>
    </App>,
  )
}

describe('RecurringConsole', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.put.mockReset()
    apiMock.del.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/batch/targets')) return Promise.resolve({ ...targets })
      if (url === '/api/admin/batch/recurring') return Promise.resolve({ ...tasks })
      if (url === '/api/admin/batch/recurring/7')
        return Promise.resolve({ ...tasks.tasks[0], rows: [{ code: '600000' }, { code: '000001' }], history: [{ id: 1, task_id: 7, job_id: 42, fired_at: '2026-07-12 09:30:00', job_status: 'done' }] })
      return Promise.resolve({})
    })
    apiMock.post.mockResolvedValue({ ok: true, job_id: 42 })
  })

  it('lists tasks with their cadence, next run, and target', async () => {
    renderPage()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/admin/batch/recurring'))
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/batch/targets')
    expect(await screen.findByText('Close review')).toBeTruthy()
    expect(screen.getByText('recurring.cadence.daily')).toBeTruthy() // daily label key
    expect(screen.getByText('2026-07-14 09:30:00')).toBeTruthy() // next run
    expect(screen.getByText('recurring.priorityNormal')).toBeTruthy() // '' → normal tag
  })

  it('toggling the enable switch posts to /enable', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByRole('switch'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/batch/recurring/7/enable', { enabled: false }))
  })

  it('the new-task button opens the new-task page rather than a dialog', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByRole('button', { name: /recurring\.new/ }))
    expect(await screen.findByText('new-page')).toBeTruthy()
    expect(screen.getByTestId('where').textContent).toBe('/apps/recurring/new')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('the edit action opens that task on its own page, carrying its name for the breadcrumb', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByLabelText('common.edit'))
    expect(await screen.findByText('edit-page')).toBeTruthy()
    expect(screen.getByTestId('where').textContent).toBe('/apps/recurring/7')
    expect(screen.getByTestId('where').dataset.crumb).toBe('Close review')
    // Opening the page fetches nothing here: the page loads the task itself.
    expect(apiMock.get).not.toHaveBeenCalledWith('/api/admin/batch/recurring/7')
  })

  it('keeps the history drawer', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByLabelText('recurring.history'))
    expect(await screen.findByText('#42')).toBeTruthy()
  })

  it('does not enable New until it knows there is a workflow to run', async () => {
    apiMock.get.mockImplementation((url: string) => (url.includes('/batch/targets') ? Promise.resolve({ targets: [] }) : Promise.resolve({ ...tasks })))
    renderPage()
    expect(await screen.findByText('recurring.noTargets')).toBeTruthy()
    expect((screen.getByRole('button', { name: /recurring\.new/ }) as HTMLButtonElement).disabled).toBe(true)
  })
})
