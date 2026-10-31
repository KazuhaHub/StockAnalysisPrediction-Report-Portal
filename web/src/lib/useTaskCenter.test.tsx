import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter, useLocation } from 'react-router'
import type { TaskItem } from '../api/types'
import { EXPORTS_CHANGED_EVENT, notifyExportsChanged } from './exportJobs'
import NotificationStack from '../components/NotificationStack'
import { useTaskCenter } from './useTaskCenter'
import { CLOSE_TASKS_EVENT, closeJobDetail, openJobDetail, useOpenJobDetail } from './jobDetailStore'

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

function Probe({ enabled = true }: { enabled?: boolean }) {
  const { active } = useTaskCenter(enabled)
  return <span data-testid="active">{active.length}</span>
}
function Where() {
  const loc = useLocation()
  const open = useOpenJobDetail()
  return (
    <>
      <span data-testid="where">{loc.pathname + loc.search}</span>
      <span data-testid="detail">{open ?? ''}</span>
    </>
  )
}
const renderProbe = (enabled = true, entry = '/') =>
  render(
    <App>
      <MemoryRouter initialEntries={[entry]}>
        <Where />
        <NotificationStack />
        <Probe enabled={enabled} />
      </MemoryRouter>
    </App>,
  )
const settle = () => act(async () => { await Promise.resolve() })

beforeEach(() => {
  closeJobDetail()
  tasks = []
  apiMock.get.mockReset()
  apiMock.get.mockImplementation(async () => ({ tasks }))
})

describe('useTaskCenter', () => {
  it('counts the tasks in flight', async () => {
    tasks = [run(), exp(), exp({ id: 'e0', status: 'done' })]
    renderProbe()
    await waitFor(() => expect(screen.getByTestId('active').textContent).toBe('2'))
  })

  it('asks the server nothing when disabled', async () => {
    renderProbe(false)
    await settle()
    expect(apiMock.get).not.toHaveBeenCalled()
  })
})

describe('useTaskCenter completion toasts', () => {
  const watchThen = async (before: TaskItem, after: TaskItem) => {
    tasks = [before]
    renderProbe()
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

  it('never says "0 failed": a run with only partial results gets its own message', async () => {
    await watchThen(run(), run({ status: 'finished', done: 3, counts: { succeeded: 2, partial: 1, failed: 0, cancelled: 0 } }))
    expect(await screen.findByText('tasks.toast.runPartialOnly:{"name":"Deep dive","partial":1}')).toBeTruthy()
    expect(screen.queryByText(/tasks\.toast\.runPartial:/)).toBeNull()
  })

  it('says so when a finished run had failures', async () => {
    await watchThen(run(), run({ status: 'finished', done: 3, counts: { succeeded: 1, partial: 0, failed: 2, cancelled: 0 } }))
    expect(await screen.findByText('tasks.toast.runPartial:{"name":"Deep dive","failed":2}')).toBeTruthy()
  })

  it('reports a run where nothing succeeded as failed', async () => {
    await watchThen(run(), run({ status: 'finished', done: 3, counts: { succeeded: 0, partial: 0, failed: 3, cancelled: 0 } }))
    expect(await screen.findByText('tasks.toast.runFailed:{"name":"Deep dive"}')).toBeTruthy()
  })

  it('opens that job in place on View for a failed run, without navigating, and closes the card', async () => {
    await watchThen(run(), run({ status: 'finished', done: 3, counts: { succeeded: 0, partial: 0, failed: 3, cancelled: 0 } }))
    fireEvent.click(await screen.findByRole('button', { name: 'tasks.toast.view' }))
    expect(screen.getByTestId('detail').textContent).toBe('7')
    expect(screen.getByTestId('where').textContent).toBe('/')
    await waitFor(() => expect(document.querySelector('.rp-notify-card')).toBeNull())
  })

  it('opens a partial run in place too', async () => {
    await watchThen(run({ id: '12' }), run({ id: '12', status: 'finished', done: 3, counts: { succeeded: 1, partial: 0, failed: 2, cancelled: 0 } }))
    fireEvent.click(await screen.findByRole('button', { name: 'tasks.toast.view' }))
    expect(screen.getByTestId('detail').textContent).toBe('12')
    expect(screen.getByTestId('where').textContent).toBe('/')
  })

  it('offers no View on a run the caller cannot open (the server sent no link)', async () => {
    await watchThen(run({ link: '' }), run({ link: '', status: 'finished', done: 3, counts: { succeeded: 0, partial: 0, failed: 3, cancelled: 0 } }))
    expect(await screen.findByText('tasks.toast.runFailed:{"name":"Deep dive"}')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'tasks.toast.view' })).toBeNull()
  })

  it.each(['/queue?job=7', '/apps/batch?job=7'])('does not stack a second drawer when %s already shows that run', async (entry) => {
    tasks = [run()]
    renderProbe(true, entry)
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    await settle()
    tasks = [run({ status: 'finished', done: 3, counts: { succeeded: 0, partial: 0, failed: 3, cancelled: 0 } })]
    act(() => notifyExportsChanged())
    fireEvent.click(await screen.findByRole('button', { name: 'tasks.toast.view' }))
    expect(screen.getByTestId('detail').textContent).toBe('')
  })

  it('puts covering overlays away when the page already shows that run', async () => {
    const closeTasks = vi.fn()
    window.addEventListener(CLOSE_TASKS_EVENT, closeTasks)
    tasks = [run()]
    renderProbe(true, '/queue?job=7')
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    await settle()
    act(() => openJobDetail(3)) // another run's detail is open over the page
    tasks = [run({ status: 'finished', done: 3, counts: { succeeded: 0, partial: 0, failed: 3, cancelled: 0 } })]
    act(() => notifyExportsChanged())
    fireEvent.click(await screen.findByRole('button', { name: 'tasks.toast.view' }))
    expect(screen.getByTestId('detail').textContent).toBe('')
    expect(closeTasks).toHaveBeenCalled()
    window.removeEventListener(CLOSE_TASKS_EVENT, closeTasks)
  })

  it('offers a View action on a partial run', async () => {
    await watchThen(run(), run({ status: 'finished', done: 3, counts: { succeeded: 1, partial: 0, failed: 2, cancelled: 0 } }))
    expect(await screen.findByRole('button', { name: 'tasks.toast.view' })).toBeTruthy()
  })

  it('opens the exports tab from a failed export and closes the card', async () => {
    const opened = vi.fn()
    const onEvent = (e: Event) => { if ((e as CustomEvent).detail?.open) opened() }
    window.addEventListener(EXPORTS_CHANGED_EVENT, onEvent)
    await watchThen(exp(), exp({ status: 'failed', error: 'disk full' }))
    fireEvent.click(await screen.findByRole('button', { name: 'tasks.toast.view' }))
    expect(opened).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(document.querySelector('.rp-notify-card')).toBeNull())
    window.removeEventListener(EXPORTS_CHANGED_EVENT, onEvent)
  })

  it('stays quiet when the person cancelled the task', async () => {
    await watchThen(exp(), exp({ status: 'canceled' }))
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50))
    })
    expect(document.querySelector('.rp-notify-card')).toBeNull()
  })
})
