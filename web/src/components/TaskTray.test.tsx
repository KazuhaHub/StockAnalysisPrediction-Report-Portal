import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import type { TaskItem } from '../api/types'
import { notifyExportsChanged } from '../lib/exportJobs'
import TaskTray from './TaskTray'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const exp = (over: Partial<TaskItem> = {}): TaskItem => ({
  kind: 'export', id: 'e1', name: 'Audit Q3', status: 'running', done: 40, total: 100, created_at: 1, finished_at: 0, link: '/exports', ...over,
})
const run = (over: Partial<TaskItem> = {}): TaskItem => ({
  kind: 'run', id: '7', name: 'Deep dive', detail: '600000', status: 'running', done: 1, total: 3, created_at: 2, finished_at: 0, link: '/queue',
  counts: { succeeded: 1, partial: 0, failed: 0, cancelled: 0 }, ...over,
})
let tasks: TaskItem[] = []

type Caps = { canRun?: boolean; canExport?: boolean; admin?: boolean }
const renderTray = (caps: Caps = { canRun: true, canExport: true }) =>
  render(
    <MemoryRouter>
      <App>
        <TaskTray canRun={!!caps.canRun} canExport={!!caps.canExport} admin={!!caps.admin} />
      </App>
    </MemoryRouter>,
  )
const openTray = async () => {
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'tasks.title' }))
  return user
}
const settle = () => act(async () => { await Promise.resolve() })

beforeEach(() => {
  tasks = []
  apiMock.get.mockReset()
  apiMock.post.mockReset()
  apiMock.post.mockResolvedValue({})
  apiMock.get.mockImplementation(async () => ({ tasks }))
})

describe('TaskTray', () => {
  it('says what appears there when there are no tasks', async () => {
    renderTray()
    await openTray()
    expect(await screen.findByText('tasks.empty')).toBeTruthy()
  })

  it('lists runs and exports together, tagged by kind, active ones first', async () => {
    tasks = [run(), exp(), exp({ id: 'e0', name: 'Old one', status: 'done', download: '/api/exports/e0/file' })]
    renderTray()
    await openTray()
    const items = await screen.findAllByRole('listitem')
    expect(items[0].textContent).toContain('Deep dive')
    expect(items[0].textContent).toContain('tasks.kind.run')
    expect(items[1].textContent).toContain('Audit Q3')
    expect(items[1].textContent).toContain('tasks.kind.export')
    expect(items[2].textContent).toContain('Old one')
  })

  it('badges the number of active tasks', async () => {
    tasks = [run(), exp(), exp({ id: 'e0', status: 'done' })]
    renderTray()
    expect(await screen.findByText('2', { selector: '.ant-scroll-number-only-unit' })).toBeTruthy()
  })

  it('shows how many tasks are ahead of a queued one, and scheduled for a deferred run', async () => {
    tasks = [exp({ status: 'queued', ahead: 3 }), run({ id: '8', name: 'Later', status: 'queued', scheduled: true })]
    renderTray()
    await openTray()
    expect(await screen.findByText('tasks.ahead:{"n":3}')).toBeTruthy()
    expect(screen.getByText('tasks.scheduled')).toBeTruthy()
  })

  it('uses the singular for one task ahead', async () => {
    tasks = [exp({ status: 'queued', ahead: 1 })]
    renderTray()
    await openTray()
    expect(await screen.findByText('tasks.aheadOne')).toBeTruthy()
  })

  it('says a queued task has nothing ahead of it when the count is zero', async () => {
    tasks = [exp({ status: 'queued', ahead: 0 })]
    renderTray()
    await openTray()
    expect(await screen.findByText('tasks.next')).toBeTruthy()
  })

  it("downloads through the item's own url", async () => {
    tasks = [exp({ id: 'a', status: 'done', download: '/api/admin/exports/a/file' }), exp({ id: 'b', name: 'B', status: 'done', download: '/api/exports/b/file' })]
    renderTray({ canExport: true, admin: true })
    await openTray()
    const links = await screen.findAllByRole('link', { name: /tasks\.download/ })
    expect(links.map((l) => l.getAttribute('href'))).toEqual(['/api/admin/exports/a/file', '/api/exports/b/file'])
  })

  it("cancels through the item's own url", async () => {
    tasks = [run({ cancel: '/api/admin/batch/jobs/7/cancel' })]
    renderTray()
    const user = await openTray()
    await user.click(await screen.findByRole('button', { name: 'tasks.cancel' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/batch/jobs/7/cancel'))
  })

  it('offers no cancel or download where the server gave no url', async () => {
    tasks = [exp({ status: 'failed', error: 'disk full' })]
    renderTray()
    await openTray()
    expect(await screen.findByText('disk full')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'tasks.cancel' })).toBeNull()
    expect(screen.queryByRole('link', { name: /tasks\.download/ })).toBeNull()
  })

  it('opens a task at the page the server named', async () => {
    tasks = [run()]
    renderTray()
    await openTray()
    expect((await screen.findByRole('link', { name: 'tasks.open' })).getAttribute('href')).toBe('/queue')
  })

  it('links to the queue and to the reader export page for a reader who can do both', async () => {
    renderTray({ canRun: true, canExport: true })
    await openTray()
    expect((await screen.findByRole('link', { name: 'tasks.viewQueue' })).getAttribute('href')).toBe('/queue')
    expect(screen.getByRole('link', { name: 'tasks.viewExports' }).getAttribute('href')).toBe('/exports')
  })

  it("sends an administrator's export link to the console", async () => {
    renderTray({ canRun: true, canExport: false, admin: true })
    await openTray()
    expect((await screen.findByRole('link', { name: 'tasks.viewExports' })).getAttribute('href')).toBe('/manage/exports')
  })

  it('offers only the links that apply', async () => {
    renderTray({ canRun: false, canExport: true })
    await openTray()
    await screen.findByText('tasks.empty')
    expect(screen.queryByRole('link', { name: 'tasks.viewQueue' })).toBeNull()
  })

  it('renders nothing for a user with no task capability and asks the server nothing', async () => {
    renderTray({})
    expect(screen.queryByRole('button', { name: 'tasks.title' })).toBeNull()
    await settle()
    expect(apiMock.get).not.toHaveBeenCalled()
  })

  it('opens when a page asks it to', async () => {
    renderTray()
    act(() => notifyExportsChanged({ open: true }))
    expect(await screen.findByText('tasks.empty')).toBeTruthy()
  })
})

describe('TaskTray completion toasts', () => {
  const watchThen = async (before: TaskItem, after: TaskItem) => {
    tasks = [before]
    renderTray()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    await settle()
    tasks = [after]
    act(() => notifyExportsChanged())
  }

  it('toasts an export once when it finishes, with a download action on its own url', async () => {
    await watchThen(exp(), exp({ status: 'done', download: '/api/exports/e1/file' }))
    expect(await screen.findByText('tasks.toast.exportDone:{"name":"Audit Q3"}')).toBeTruthy()
    expect(screen.getAllByRole('link', { name: /tasks\.download/ }).some((a) => a.getAttribute('href') === '/api/exports/e1/file')).toBe(true)
    act(() => notifyExportsChanged())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50))
    })
    expect(screen.getAllByText('tasks.toast.exportDone:{"name":"Audit Q3"}')).toHaveLength(1)
  })

  it('toasts an export failure with its reason', async () => {
    await watchThen(exp(), exp({ status: 'failed', error: 'disk full' }))
    expect(await screen.findByText('tasks.toast.exportFailed:{"name":"Audit Q3"}')).toBeTruthy()
    expect(await screen.findByText('disk full')).toBeTruthy()
  })

  it('toasts a clean run finish', async () => {
    await watchThen(run(), run({ status: 'finished', done: 3, counts: { succeeded: 3, partial: 0, failed: 0, cancelled: 0 } }))
    expect(await screen.findByText('tasks.toast.runFinished:{"name":"Deep dive"}')).toBeTruthy()
  })

  it('says so when a finished run had failures', async () => {
    await watchThen(run(), run({ status: 'finished', done: 3, counts: { succeeded: 1, partial: 0, failed: 2, cancelled: 0 } }))
    expect(await screen.findByText('tasks.toast.runPartial:{"name":"Deep dive","failed":2}')).toBeTruthy()
  })

  it('reports a run where nothing succeeded as failed', async () => {
    await watchThen(run(), run({ status: 'finished', done: 3, counts: { succeeded: 0, partial: 0, failed: 3, cancelled: 0 } }))
    expect(await screen.findByText('tasks.toast.runFailed:{"name":"Deep dive"}')).toBeTruthy()
  })

  it('stays quiet when the person cancelled the task', async () => {
    await watchThen(exp(), exp({ status: 'canceled' }))
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50))
    })
    expect(document.querySelector('.ant-notification-notice')).toBeNull()
  })
})
