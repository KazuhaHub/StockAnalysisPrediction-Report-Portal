import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter, useLocation } from 'react-router'
import type { BatchJob, TaskItem } from '../api/types'
import QueueDrawer, { type DrawerTab } from './QueueDrawer'

const store = vi.hoisted(() => ({
  urls: [] as string[],
  admin: true,
  jobs: [] as unknown[],
  summary: { running: 0, waiting: 0, scheduled: 0, budget: 3 },
}))
// Counts the live pollers: started minus stopped. The first tick runs at once, as the real one does.
const poll = vi.hoisted(() => ({ live: 0 }))
const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('../lib/conditionalGet', () => ({
  UNCHANGED: Symbol('unchanged'),
  forgetTags: () => {},
  getIfChanged: (url: string) => {
    store.urls.push(String(url))
    return String(url).includes('/queue') ? Promise.resolve(store.summary) : Promise.resolve({ jobs: store.jobs })
  },
}))
vi.mock('../lib/visiblePoll', () => ({
  startVisiblePoll: (fn: () => unknown) => {
    poll.live++
    void fn()
    return () => {
      poll.live--
    }
  },
}))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: store.admin, user: 'alice' }) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const job = (over: Partial<BatchJob> = {}): BatchJob => ({
  id: 1, target_id: 9, status: 'running', concurrency: 1, max_retries: 0, total: 5, succeeded: 2, partial: 0, failed: 0,
  created_by: 'alice', created_at: '2026-10-29 09:00:00', started_at: '', finished_at: '', inputs: JSON.stringify({ symbol: '002432' }), ...over,
})
const exp = (over: Partial<TaskItem> = {}): TaskItem => ({
  kind: 'export', id: 'e1', name: 'Audit Q3', status: 'running', done: 22, total: 34, created_at: Date.now() - 60_000, finished_at: 0, link: '/exports',
  cancel: '/api/exports/e1/cancel', ...over,
})

type Props = Partial<React.ComponentProps<typeof QueueDrawer>>
const refreshTasks = vi.fn()
const onTabChange = vi.fn()
function Where() {
  return <span data-testid="where">{useLocation().pathname}</span>
}
function Harness(props: Props) {
  return (
    <App>
      <MemoryRouter>
        <Where />
        <QueueDrawer open onClose={() => {}} canRun canExport tasks={[]} refreshTasks={refreshTasks} tab="runs" onTabChange={onTabChange} {...props} />
      </MemoryRouter>
    </App>
  )
}
const mount = (props: Props = {}) => render(<Harness {...props} />)
const rows = (section: string) => within(screen.getByRole('list', { name: section })).queryAllByRole('listitem')

beforeEach(() => {
  store.urls = []
  poll.live = 0
  store.admin = true
  store.jobs = []
  store.summary = { running: 0, waiting: 0, scheduled: 0, budget: 3 }
  localStorage.clear()
  apiMock.get.mockReset().mockResolvedValue({ targets: [{ id: 9, name: 'Deep Research' }] })
  apiMock.post.mockReset().mockResolvedValue({})
  refreshTasks.mockReset()
  onTabChange.mockReset()
})

describe('Tasks drawer mine / all (ADR 0047)', () => {
  it('lets an administrator switch to their own runs', async () => {
    mount()
    await waitFor(() => expect(store.urls.length).toBeGreaterThan(0))
    fireEvent.click(screen.getByText('queue.scope.mine'))
    await waitFor(() => expect(store.urls.some((u) => u.includes('/jobs') && u.includes('scope=mine'))).toBe(true))
    await waitFor(() => expect(store.urls.some((u) => u.includes('/queue') && u.includes('scope=mine'))).toBe(true))
  })

  it('offers no toggle to a non-administrator', async () => {
    store.admin = false
    mount()
    await waitFor(() => expect(store.urls.length).toBeGreaterThan(0))
    expect(screen.queryByText('queue.scope.mine')).toBeNull()
  })
})

describe('Tasks drawer tabs', () => {
  it('shows Runs and Exports tabs with their in-progress counts', async () => {
    store.jobs = [job(), job({ id: 2, status: 'queued', ahead: 1 }), job({ id: 3, status: 'finished', finished_at: '2026-10-29 09:30:00' })]
    mount({ tasks: [exp()] })
    expect(await screen.findByRole('tab', { name: /tasks\.tab\.runs/ })).toBeTruthy()
    await waitFor(() => expect(screen.getByRole('tab', { name: /tasks\.tab\.runs/ }).textContent).toContain('2'))
    expect(screen.getByRole('tab', { name: /tasks\.tab\.exports/ }).textContent).toContain('1')
  })

  it('lays the four figures out as a grid, one column each', async () => {
    const { container } = mount()
    await screen.findByText('queue.budget')
    const grid = container.ownerDocument.querySelector('.rp-task-stats')
    expect(grid).not.toBeNull()
    expect(grid!.children).toHaveLength(4)
  })

  it('shows the Mine / All switch only while the Runs tab is on screen', async () => {
    const { rerender } = mount()
    expect(await screen.findByText('queue.scope.mine')).toBeTruthy()
    rerender(<Harness tab="exports" />)
    await waitFor(() => expect(screen.queryByText('queue.scope.mine')).toBeNull())
  })

  it('shows only Exports, without a tab bar, to a reader who cannot run', async () => {
    store.admin = false
    mount({ canRun: false, canExport: true, tab: 'exports', tasks: [exp()] })
    expect(await screen.findByText('Audit Q3')).toBeTruthy()
    expect(screen.queryByRole('tab')).toBeNull()
    expect(store.urls).toEqual([]) // the run queue is not asked for
    expect(screen.queryByText('queue.running')).toBeNull()
  })

  it('shows only Runs, without a tab bar, to a runner whose export is not allowed', async () => {
    store.admin = false
    store.jobs = [job()]
    mount({ canRun: true, canExport: false })
    expect(await screen.findByText('Deep Research')).toBeTruthy()
    expect(screen.queryByRole('tab')).toBeNull()
  })

  it('reports a tab change to its owner', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(await screen.findByRole('tab', { name: /tasks\.tab\.exports/ }))
    expect(onTabChange).toHaveBeenCalledWith('exports' satisfies DrawerTab)
  })
})

describe('Tasks drawer Runs tab', () => {
  it('keeps the four summary figures', async () => {
    store.summary = { running: 1, waiting: 2, scheduled: 0, budget: 3 }
    mount()
    expect(await screen.findByText('queue.budget')).toBeTruthy()
    await waitFor(() => expect(screen.getByText('2')).toBeTruthy())
    for (const k of ['queue.running', 'queue.waiting', 'queue.scheduled', 'queue.budget']) expect(screen.getByText(k)).toBeTruthy()
  })

  it('draws a running run on two lines: name with inputs, then a bar and its row count', async () => {
    store.jobs = [job()]
    mount()
    const [row] = await waitFor(() => {
      const r = rows('tasks.section.active')
      expect(r).toHaveLength(1)
      return r
    })
    expect(row.textContent).toContain('Deep Research')
    expect(row.textContent).toContain('002432')
    expect(row.querySelector('[role="progressbar"], .ant-progress')).not.toBeNull()
    expect(row.textContent).toContain('tasks.rows:{"done":2,"total":5}')
  })

  it('says how many are ahead of a queued run, and who is next', async () => {
    store.jobs = [job({ id: 2, status: 'queued', ahead: 2 }), job({ id: 3, status: 'queued', ahead: 0 })]
    mount()
    expect(await screen.findByText('tasks.queuedAhead:{"n":2}')).toBeTruthy()
    expect(screen.getByText('tasks.next')).toBeTruthy()
  })

  it('says when a deferred run starts', async () => {
    store.jobs = [job({ id: 2, status: 'queued', scheduled: true, run_at: '2026-12-01 08:00:00' })]
    mount()
    expect(await screen.findByText(/tasks\.scheduledAt/)).toBeTruthy()
  })

  it('summarises finished runs by outcome', async () => {
    store.jobs = [
      job({ id: 4, status: 'finished', total: 1, succeeded: 1, finished_at: '2026-10-29 09:30:00' }),
      job({ id: 5, status: 'finished', total: 4, succeeded: 3, failed: 1, finished_at: '2026-10-28 22:36:00' }),
      job({ id: 6, status: 'finished', total: 2, succeeded: 0, failed: 2, finished_at: '2026-10-28 18:02:00' }),
    ]
    mount()
    expect(await screen.findByText('tasks.result.done:{"ok":1}')).toBeTruthy()
    expect(screen.getByText('tasks.result.partly:{"ok":3,"failed":1}')).toBeTruthy()
    expect(screen.getByText('tasks.result.failed:{"failed":2}')).toBeTruthy()
  })

  it('treats an expired run as finished: muted, labelled, and with no cancel', async () => {
    store.jobs = [job({ id: 8, status: 'expired', total: 1, succeeded: 0, finished_at: '2026-10-29 09:30:00' })]
    mount()
    await waitFor(() => expect(rows('tasks.section.finished')).toHaveLength(1))
    expect(screen.queryByRole('list', { name: 'tasks.section.active' })).toBeNull()
    expect(rows('tasks.section.finished')[0].textContent).toContain('batch.status.expired')
    expect(screen.queryByRole('button', { name: 'tasks.cancel' })).toBeNull()
  })

  it('stops polling the queue while the Exports tab is shown, and polls at once on return', async () => {
    const { rerender } = mount()
    await waitFor(() => expect(poll.live).toBe(1))
    rerender(<Harness tab="exports" />)
    await waitFor(() => expect(poll.live).toBe(0))
    store.urls = []
    rerender(<Harness tab="runs" />)
    await waitFor(() => expect(poll.live).toBe(1))
    await waitFor(() => expect(store.urls.some((u) => u.includes('/jobs'))).toBe(true))
  })

  it('lists at most five recently finished runs, newest first', async () => {
    store.jobs = Array.from({ length: 8 }, (_, i) =>
      job({ id: 10 + i, status: 'finished', succeeded: 5, finished_at: `2026-10-2${i} 10:00:00`, inputs: JSON.stringify({ n: `run-${i}` }) }),
    )
    mount()
    await waitFor(() => expect(rows('tasks.section.finished')).toHaveLength(5))
    const names = rows('tasks.section.finished').map((r) => r.textContent ?? '')
    expect(names[0]).toContain('run-7')
    expect(names[4]).toContain('run-3')
  })

  it('cancels a run through its own url after confirming', async () => {
    const user = userEvent.setup()
    store.jobs = [job({ id: 77 })]
    mount()
    await user.click(await screen.findByRole('button', { name: 'tasks.cancel' }))
    await user.click(await screen.findByRole('button', { name: 'OK' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/batch/jobs/77/cancel'))
  })

  it('offers no cancel on a finished run', async () => {
    store.jobs = [job({ id: 4, status: 'finished', succeeded: 5, finished_at: '2026-10-29 09:30:00' })]
    mount()
    await screen.findByText('tasks.result.done:{"ok":5}')
    expect(screen.queryByRole('button', { name: 'tasks.cancel' })).toBeNull()
  })

  it('draws cancel as a close-circle, not the forbidden-looking stop icon', async () => {
    store.jobs = [job({ id: 77 })]
    mount()
    const cancel = await screen.findByRole('button', { name: 'tasks.cancel' })
    expect(cancel.querySelector('.anticon-close-circle')).not.toBeNull()
    expect(cancel.querySelector('.anticon-stop')).toBeNull()
  })

  it('puts the count in each section header', async () => {
    store.jobs = [job(), job({ id: 2, status: 'queued', ahead: 1 }), job({ id: 3, status: 'finished', finished_at: '2026-10-29 09:30:00' })]
    mount()
    expect(await screen.findByText('tasks.section.count:{"label":"tasks.section.active","count":2}')).toBeTruthy()
    // The finished list is capped, so a count there would read as the total.
    expect(screen.getByText('tasks.section.finished')).toBeTruthy()
    expect(screen.queryByText(/tasks\.section\.count:.*finished/)).toBeNull()
  })

  it('keeps a row marked active while its cancel confirmation is open', async () => {
    const user = userEvent.setup()
    store.jobs = [job({ id: 77 })]
    mount()
    await user.click(await screen.findByRole('button', { name: 'tasks.cancel' }))
    await screen.findByRole('button', { name: 'OK' })
    expect(rows('tasks.section.active')[0].classList.contains('is-active')).toBe(true)
  })

  it('renders Mine / All in the drawer header extra area, not beside the title', async () => {
    mount()
    const mine = await screen.findByText('queue.scope.mine')
    expect(mine.closest('.ant-drawer-extra')).not.toBeNull()
    expect(screen.getByText('tasks.title').closest('.ant-drawer-title')?.querySelector('.ant-segmented')).toBeNull()
  })

  it('links the footer to the full queue', async () => {
    mount()
    fireEvent.click(await screen.findByRole('button', { name: /tasks\.openQueue/ }))
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/queue'))
  })

  it('says the queue is empty', async () => {
    mount()
    expect(await screen.findByText('tasks.emptyRuns')).toBeTruthy()
  })
})

describe('Tasks drawer Exports tab', () => {
  it('draws a running export with its bar and row count, and a cancel on its own url', async () => {
    const user = userEvent.setup()
    mount({ tab: 'exports', tasks: [exp({ cancel: '/api/admin/exports/e1/cancel' })] })
    const [row] = rows('tasks.section.active')
    expect(row.textContent).toContain('Audit Q3')
    expect(row.textContent).toContain('tasks.rows:{"done":22,"total":34}')
    await user.click(within(row).getByRole('button', { name: 'tasks.cancel' }))
    expect(apiMock.post).not.toHaveBeenCalled() // one tap is not enough
    await user.click(await screen.findByRole('button', { name: 'OK' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/exports/e1/cancel'))
    expect(refreshTasks).toHaveBeenCalled()
  })

  it('shows queue position for a queued export', async () => {
    mount({ tab: 'exports', tasks: [exp({ status: 'queued', ahead: 3 })] })
    expect(screen.getByText('tasks.queuedAhead:{"n":3}')).toBeTruthy()
  })

  it("downloads a finished export through the item's own url, and shows a failure's reason", () => {
    mount({
      tab: 'exports',
      tasks: [
        exp({ id: 'a', name: 'A', status: 'done', done: 120, total: 120, download: '/api/exports/a/file', finished_at: Date.now() - 1000, cancel: undefined }),
        exp({ id: 'b', name: 'B', status: 'failed', error: 'over the limit', finished_at: Date.now() - 2000, cancel: undefined }),
      ],
    })
    const [done, failed] = rows('tasks.section.finished')
    expect(within(done).getByRole('link', { name: 'tasks.download' }).getAttribute('href')).toBe('/api/exports/a/file')
    expect(failed.textContent).toContain('tasks.result.failedWhy:{"error":"over the limit"}')
    expect(within(failed).queryByRole('link', { name: 'tasks.download' })).toBeNull()
  })

  it('caps the finished exports at five and ignores runs in the shared feed', () => {
    const finished = Array.from({ length: 7 }, (_, i) =>
      exp({ id: `f${i}`, name: `E${i}`, status: 'done', finished_at: 1000 + i, cancel: undefined, download: `/d/${i}` }),
    )
    mount({ tab: 'exports', tasks: [...finished, { ...exp({ id: '5', name: 'A run' }), kind: 'run' }] })
    expect(rows('tasks.section.finished')).toHaveLength(5)
    expect(rows('tasks.section.finished')[0].textContent).toContain('E6')
    expect(screen.queryByText('A run')).toBeNull()
  })

  it('names the export format beside the name, and a finished export its reports and size', () => {
    mount({
      tab: 'exports',
      tasks: [
        exp({ id: 'a', status: 'done', done: 120, total: 120, format: 'md_zip', bytes: 3_400_000, download: '/d', cancel: undefined, finished_at: 9 }),
        exp({ id: 'b', name: 'Live', format: 'csv' }),
      ],
    })
    const [live] = rows('tasks.section.active')
    expect(live.textContent).toContain('export.format.csv')
    const [done] = rows('tasks.section.finished')
    expect(done.textContent).toContain('export.format.md_zip')
    expect(done.textContent).toContain('tasks.result.exportDone:{"n":120,"size":"3.2 MB"}')
  })

  it('notes how long finished files are kept', () => {
    mount({ tab: 'exports', tasks: [exp({ status: 'done', download: '/d', cancel: undefined, finished_at: 5 })] })
    expect(screen.getByText('tasks.keptDays:{"n":7}')).toBeTruthy()
  })

  it("points a reader to the reader export page and an administrator to the console's", async () => {
    store.admin = false
    const reader = mount({ tab: 'exports' })
    fireEvent.click(screen.getByRole('button', { name: /tasks\.openExports/ }))
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/exports'))
    reader.unmount()
    store.admin = true
    mount({ tab: 'exports' })
    fireEvent.click(screen.getByRole('button', { name: /tasks\.openExports/ }))
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/manage/exports'))
  })

  it('says there are no exports', () => {
    mount({ tab: 'exports' })
    expect(screen.getByText('tasks.emptyExports')).toBeTruthy()
  })
})

function stubPointer(coarse: boolean) {
  vi.stubGlobal('matchMedia', (query: string): MediaQueryList => ({
    matches: coarse && query.includes('any-pointer: coarse'),
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }))
}
const toggleOf = (row: HTMLElement) => row.querySelector<HTMLElement>('[aria-expanded]')

describe('Tasks drawer row layout', () => {
  beforeEach(() => stubPointer(false))
  afterEach(() => vi.unstubAllGlobals())

  it('ends every row with the time, in a trailing cell that also holds the actions', async () => {
    store.jobs = [job({ id: 1 }), job({ id: 2, status: 'finished', finished_at: '2026-10-29 09:30:00' })]
    mount()
    await screen.findAllByText('Deep Research')
    const all = [...rows('tasks.section.active'), ...rows('tasks.section.finished')]
    expect(all.length).toBe(2)
    for (const row of all) {
      const end = row.lastElementChild as HTMLElement
      expect(end.classList.contains('rp-task-row__end')).toBe(true)
      expect(end.querySelector('.rp-task-row__time')).not.toBeNull()
    }
  })

  it('keeps the actions hidden by default and offers no tap toggle on a hover device', async () => {
    store.jobs = [job({ id: 1 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    expect(row.classList.contains('is-open')).toBe(false)
    expect(toggleOf(row)).toBeNull()
    expect(row.querySelector('.rp-task-row__acts')).not.toBeNull()
  })
})

describe('Tasks drawer row on a touch device', () => {
  beforeEach(() => stubPointer(true))
  afterEach(() => vi.unstubAllGlobals())

  it('reveals the actions when the row is tapped and hides them on a second tap', async () => {
    store.jobs = [job({ id: 1 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    const toggle = toggleOf(row)!
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(row.classList.contains('is-open')).toBe(false)
    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(row.classList.contains('is-open')).toBe(true)
    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(row.classList.contains('is-open')).toBe(false)
  })

  it('keeps one row open: tapping another closes the first', async () => {
    store.jobs = [job({ id: 1 }), job({ id: 2 })]
    mount()
    await screen.findAllByText('Deep Research')
    const [a, b] = rows('tasks.section.active')
    fireEvent.click(toggleOf(a)!)
    expect(a.classList.contains('is-open')).toBe(true)
    fireEvent.click(toggleOf(b)!)
    expect(a.classList.contains('is-open')).toBe(false)
    expect(b.classList.contains('is-open')).toBe(true)
  })

  it('toggles from the keyboard with Enter and Space', async () => {
    store.jobs = [job({ id: 1 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    const toggle = toggleOf(row)!
    expect(toggle.tabIndex).toBe(0)
    fireEvent.keyDown(toggle, { key: 'Enter' })
    expect(row.classList.contains('is-open')).toBe(true)
    fireEvent.keyDown(toggle, { key: ' ' })
    expect(row.classList.contains('is-open')).toBe(false)
  })

  it('hides the open row when the person taps outside the list', async () => {
    store.jobs = [job({ id: 1 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    fireEvent.click(toggleOf(row)!)
    expect(row.classList.contains('is-open')).toBe(true)
    fireEvent.pointerDown(screen.getByText('queue.budget'))
    expect(row.classList.contains('is-open')).toBe(false)
  })

  it('does not close the row when an action inside it is pressed', async () => {
    store.jobs = [job({ id: 1 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    fireEvent.click(toggleOf(row)!)
    fireEvent.pointerDown(within(row).getByRole('button', { name: 'tasks.cancel' }))
    expect(row.classList.contains('is-open')).toBe(true)
  })
})
