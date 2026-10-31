import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { Link, MemoryRouter, useLocation } from 'react-router'
import type { BatchJob, TaskItem } from '../api/types'
import QueueDrawer, { type DrawerTab } from './QueueDrawer'
import JobDetailHost from './JobDetailHost'
import { closeJobDetail } from '../lib/jobDetailStore'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const store = vi.hoisted(() => ({
  urls: [] as string[],
  admin: true,
  jobs: [] as unknown[],
  // What an administrator's own scope answers; null means the same as everyone's.
  jobsMine: null as unknown[] | null,
  // Leave every scope=mine request unanswered.
  pendMine: false,
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
    const mine = String(url).includes('scope=mine')
    if (mine && store.pendMine) return new Promise(() => {})
    if (mine && store.jobsMine && String(url).includes('/jobs')) return Promise.resolve({ jobs: store.jobsMine })
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
  const t = (k: string, o?: unknown) => {
    const unit = /^tasks\.elapsed\.([hms])$/.exec(k)
    if (unit) return `${(o as { n: number }).n}${unit[1]}`
    return typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`
  }
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
  const loc = useLocation()
  return (
    <>
      <span data-testid="where">{loc.pathname}</span>
      <span data-testid="search">{loc.search}</span>
    </>
  )
}
function Harness({ entry, ...props }: Props & { entry?: string }) {
  return (
    <App>
      <MemoryRouter initialEntries={[entry ?? '/']}>
        <Where />
        <Link to="/elsewhere">go-elsewhere</Link>
        <JobDetailHost />
        <QueueDrawer open onClose={() => {}} canRun canExport tasks={[]} refreshTasks={refreshTasks} tab="runs" onTabChange={onTabChange} {...props} />
      </MemoryRouter>
    </App>
  )
}
const mount = (props: Props & { entry?: string } = {}) => render(<Harness {...props} />)
const rows = (section: string) => within(screen.getByRole('list', { name: section })).queryAllByRole('listitem')

beforeEach(() => {
  store.urls = []
  poll.live = 0
  store.admin = true
  store.jobs = []
  store.jobsMine = null
  store.pendMine = false
  store.summary = { running: 0, waiting: 0, scheduled: 0, budget: 3 }
  localStorage.clear()
  closeJobDetail()
  apiMock.get.mockReset().mockImplementation(async (url: string) => {
    if (/\/batch\/jobs\/\d+$/.test(url)) {
      const id = Number(url.split('/').pop())
      return { job: job({ id }), counts: {}, running_in_process: true, items: [{ id: 1, row_index: 0, inputs: '{}', status: 'running', attempts: 1, run_id: '', conversation_id: '', task_id: '', error: '', started_at: '', finished_at: '' }] }
    }
    return { targets: [{ id: 9, name: 'Deep Research' }] }
  })
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

  it('loads both scopes when it opens, so switching shows the other at once', async () => {
    store.jobs = [job({ id: 1, inputs: JSON.stringify({ symbol: '111111' }) })]
    store.jobsMine = [job({ id: 2, inputs: JSON.stringify({ symbol: '222222' }) })]
    mount()
    await screen.findByText(/111111/)
    await waitFor(() => expect(store.urls.some((u) => u.includes('/jobs') && u.includes('scope=mine'))).toBe(true))
    await act(async () => {})
    fireEvent.click(screen.getByText('queue.scope.mine'))
    // No wait: the answer is already held.
    expect(screen.getByText(/222222/)).toBeTruthy()
    expect(screen.queryByText(/111111/)).toBeNull()
  })

  it("never shows one scope's runs under the other while the other is still loading", async () => {
    store.jobs = [job({ id: 1, inputs: JSON.stringify({ symbol: '111111' }) })]
    store.pendMine = true
    mount()
    await screen.findByText(/111111/)
    fireEvent.click(screen.getByText('queue.scope.mine'))
    expect(screen.queryByText(/111111/)).toBeNull()
    expect(screen.queryByText('tasks.emptyRuns')).toBeNull()
  })

  it('asks for the other scope only once per open, not on every poll', async () => {
    mount()
    await waitFor(() => expect(store.urls.some((u) => u.includes('scope=mine'))).toBe(true))
    await act(async () => {})
    const mineAsks = store.urls.filter((u) => u.includes('/jobs') && u.includes('scope=mine')).length
    expect(mineAsks).toBe(1)
  })

  it('offers no toggle to a non-administrator', async () => {
    store.admin = false
    mount()
    await waitFor(() => expect(store.urls.length).toBeGreaterThan(0))
    expect(screen.queryByText('queue.scope.mine')).toBeNull()
  })
})

describe('Tasks drawer tabs', () => {
  // The two views switch with a full-width segmented control (like Mine / All), not small
  // left-aligned tabs; an in-progress count is a small pill, shown only when there is one.
  it('switches Runs and Exports with a full-width segmented control carrying count pills', async () => {
    store.jobs = [job(), job({ id: 2, status: 'queued', ahead: 1 }), job({ id: 3, status: 'finished', finished_at: '2026-10-29 09:30:00' })]
    mount({ tasks: [exp()] })
    const views = await waitFor(() => {
      const el = document.querySelector('.rp-task-views')
      expect(el).not.toBeNull()
      return el as HTMLElement
    })
    expect(document.querySelector('.ant-drawer .ant-tabs')).toBeNull()
    expect(views.classList.contains('ant-segmented-block')).toBe(true)
    await waitFor(() => expect(within(views).getByRole('radio', { name: /tasks\.tab\.runs/ }).closest('label')?.querySelector('.rp-task-views__count')?.textContent).toBe('2'))
    expect(within(views).getByRole('radio', { name: /tasks\.tab\.exports/ }).closest('label')?.querySelector('.rp-task-views__count')?.textContent).toBe('1')
  })

  it('shows no count pill on a view with nothing in progress', async () => {
    store.jobs = [job({ id: 3, status: 'finished', finished_at: '2026-10-29 09:30:00' })]
    mount({ tasks: [] })
    const views = await waitFor(() => document.querySelector('.rp-task-views') as HTMLElement)
    await screen.findByText('Deep Research')
    expect(views.querySelectorAll('.rp-task-views__count')).toHaveLength(0)
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
    expect(document.querySelector('.rp-task-views')).toBeNull()
    expect(store.urls).toEqual([]) // the run queue is not asked for
    expect(screen.queryByText('queue.running')).toBeNull()
  })

  it('shows only Runs, without a tab bar, to a runner whose export is not allowed', async () => {
    store.admin = false
    store.jobs = [job()]
    mount({ canRun: true, canExport: false })
    expect(await screen.findByText('Deep Research')).toBeTruthy()
    expect(document.querySelector('.rp-task-views')).toBeNull()
  })

  it('reports a tab change to its owner', async () => {
    const user = userEvent.setup()
    mount()
    // The radio itself is pointer-events: none in antd's Segmented; people click its label.
    await user.click((await screen.findByRole('radio', { name: /tasks\.tab\.exports/ })).closest('label') as HTMLElement)
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

function stubHover(hover: boolean) {
  vi.stubGlobal('matchMedia', (query: string): MediaQueryList => ({
    matches: !hover && query.includes('(hover: none)'),
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }))
}

const rowButton = (row: HTMLElement) => row.querySelector<HTMLElement>('.rp-task-row__main[role="button"]')!

describe('Tasks drawer row layout', () => {
  beforeEach(() => stubHover(true))
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

  it('keeps the actions reachable on a device that hovers, and drops the arrow action', async () => {
    store.jobs = [job({ id: 1 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    expect(row.querySelector('.rp-task-row__acts')!.hasAttribute('inert')).toBe(false)
    expect(within(row).getByRole('button', { name: 'tasks.cancel' })).toBeTruthy()
    expect(within(row).queryByRole('button', { name: 'tasks.open' })).toBeNull()
  })

  it('describes the row by its second line and time, keeping the name short', async () => {
    store.jobs = [job({ id: 1, total: 5 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    const ids = (rowButton(row).getAttribute('aria-describedby') ?? '').split(' ').filter(Boolean)
    expect(ids.length).toBeGreaterThan(0)
    const text = ids.map((id) => document.getElementById(id)?.textContent ?? '').join(' ')
    expect(text).toContain('tasks.rows:{"done":2,"total":5}')
  })

  it('is a button named after the task and its status', async () => {
    store.jobs = [job({ id: 1 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    expect(rowButton(row).getAttribute('aria-label')).toBe('Deep Research, batch.status.running')
    expect(rowButton(row).tabIndex).toBe(0)
    expect(rowButton(row).hasAttribute('aria-expanded')).toBe(false)
  })
})

describe('Tasks drawer rows open the run in place', () => {
  beforeEach(() => stubHover(true))
  afterEach(() => vi.unstubAllGlobals())
  const title = (id: number) => `batch.jobTitle:{"id":${id}}`

  it('opens the detail of that run when the row is clicked, without leaving the page', async () => {
    store.jobs = [job({ id: 41 })]
    mount()
    await screen.findByText('Deep Research')
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    expect(await screen.findByText(title(41))).toBeTruthy()
    expect(screen.getByTestId('where').textContent).toBe('/')
    expect(screen.getByTestId('search').textContent).toBe('')
    expect(screen.getByRole('list', { name: 'tasks.section.active' })).toBeTruthy() // the list stays underneath
  })

  it('opens from the time cell too, anywhere on the row', async () => {
    store.jobs = [job({ id: 41 })]
    mount()
    await screen.findByText('Deep Research')
    fireEvent.click(rows('tasks.section.active')[0].querySelector('.rp-task-row__time')!)
    expect(await screen.findByText(title(41))).toBeTruthy()
  })

  it('opens from the keyboard with Enter and Space', async () => {
    store.jobs = [job({ id: 41 }), job({ id: 42 })]
    mount()
    await screen.findAllByText('Deep Research')
    const [a, b] = rows('tasks.section.active')
    fireEvent.keyDown(rowButton(a), { key: 'Enter' })
    expect(await screen.findByText(title(41))).toBeTruthy()
    closeJobDetail()
    await waitFor(() => expect(screen.queryByText(title(41))).toBeNull())
    fireEvent.keyDown(rowButton(b), { key: ' ' })
    expect(await screen.findByText(title(42))).toBeTruthy()
  })

  it('does not open when an action button is pressed', async () => {
    store.jobs = [job({ id: 41 })]
    mount()
    await screen.findByText('Deep Research')
    fireEvent.click(within(rows('tasks.section.active')[0]).getByRole('button', { name: 'tasks.cancel' }))
    await screen.findByRole('button', { name: 'OK' }) // its own confirmation opened
    expect(screen.queryByText(/batch\.jobTitle|batch\.jobDetail/)).toBeNull()
  })

  it('opens on a plain tap where hover does not exist, with no reveal step', async () => {
    vi.unstubAllGlobals()
    stubHover(false)
    store.jobs = [job({ id: 41 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    expect(row.querySelector('.rp-task-row__acts')!.hasAttribute('inert')).toBe(true)
    fireEvent.click(rowButton(row))
    expect(await screen.findByText(title(41))).toBeTruthy()
    expect(row.classList.contains('is-open')).toBe(false)
  })

  it("does not stack a second drawer when the page's own drawer already shows that run", async () => {
    store.jobs = [job({ id: 41 })]
    const onClose = vi.fn()
    mount({ entry: '/queue?job=41', onClose })
    await screen.findByText('Deep Research')
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    expect(onClose).toHaveBeenCalled() // the Tasks drawer steps aside for the page's detail
    expect(screen.queryByText(/batch\.jobTitle|batch\.jobDetail/)).toBeNull()
  })

  it("also steps aside on the batch console's own ?job= drawer", async () => {
    store.jobs = [job({ id: 41 })]
    const onClose = vi.fn()
    mount({ entry: '/apps/batch?job=41', onClose })
    await screen.findByText('Deep Research')
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    expect(onClose).toHaveBeenCalled()
    expect(screen.queryByText(/batch\.jobTitle|batch\.jobDetail/)).toBeNull()
  })

  it('returns to the Tasks list on close, which stays mounted', async () => {
    store.jobs = [job({ id: 41 })]
    mount()
    await screen.findByText('Deep Research')
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    await screen.findByText(title(41))
    act(() => closeJobDetail())
    await waitFor(() => expect(screen.queryByText(title(41))).toBeNull())
    expect(rows('tasks.section.active')).toHaveLength(1)
  })

  it('offers Open in queue in the detail, which goes to that run on the queue page', async () => {
    store.jobs = [job({ id: 41 })]
    mount()
    await screen.findByText('Deep Research')
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    fireEvent.click(await screen.findByRole('button', { name: /queue\.openInQueue/ }))
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/queue'))
    expect(screen.getByTestId('search').textContent).toBe('?job=41')
  })
})

describe('Tasks drawer running row of a single run', () => {
  it('says how long it has been running, from the start instant, with no progress bar', async () => {
    // The zoneless wall-clock string is deliberately far off: only the instant may be used.
    store.jobs = [job({ id: 1, total: 1, succeeded: 0, started_at: '2020-01-01 00:00:00', started_at_ms: Date.now() - 133_000 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    expect(row.textContent).toMatch(/tasks\.runningFor:\{"elapsed":"2m 1\ds"\}/)
    expect(row.textContent).not.toContain('tasks.rows')
    expect(row.querySelector('.ant-progress')).toBeNull()
  })

  it('keeps the bar and the row count on a multi-row run', async () => {
    store.jobs = [job({ id: 1, total: 5 })]
    mount()
    await screen.findByText('Deep Research')
    const row = rows('tasks.section.active')[0]
    expect(row.querySelector('.ant-progress')).not.toBeNull()
    expect(row.textContent).toContain('tasks.rows:{"done":2,"total":5}')
  })
})

describe('Tasks drawer rows open the export in place', () => {
  beforeEach(() => stubHover(true))
  afterEach(() => vi.unstubAllGlobals())
  const exportJob = (over: Record<string, unknown> = {}) => ({
    id: 'e1', source: 'reports', format: 'csv', name: 'Audit Q3', requested_by: 'alice', status: 'running', rows_estimate: 34, rows_done: 22,
    bytes: 0, file_name: '', error: '', created_at: Date.now() - 60_000, started_at: Date.now() - 50_000, finished_at: 0, expires_at: 0, columns: [],
    filter: { op: 'and', items: [{ field: 'symbol', operator: 'is', values: ['1'] }, { field: 'kind', operator: 'is', values: ['x'] }] }, ...over,
  })
  const modalEl = () => document.querySelector('.ant-modal') as HTMLElement
  // antd keeps a closed modal's DOM until its leave animation ends (never, under jsdom): closed = leaving.
  const modalShut = () => !document.querySelector('.ant-modal') || modalEl().classList.contains('ant-zoom-leave')
  const withExport = (job: Record<string, unknown>) => {
    const base = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string) => (/\/exports\/e\d$/.test(url) ? job : base(url)))
  }

  it('opens a compact detail with the filter summary, and cancels after confirming', async () => {
    withExport(exportJob())
    mount({ tab: 'exports', tasks: [exp({ link: '/exports' })] })
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    expect(await screen.findByText('tasks.detail.title')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledWith('/api/exports/e1')
    expect(await screen.findByText('export.source.reports')).toBeTruthy()
    expect(screen.getByText('tasks.detail.conditions:{"n":2}')).toBeTruthy()
    expect(within(modalEl()).getByText('tasks.rows:{"done":22,"total":34}')).toBeTruthy()
    expect(screen.getByTestId('where').textContent).toBe('/')
    fireEvent.click(within(modalEl()).getByRole('button', { name: /tasks\.cancel/ }))
    expect(apiMock.post).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole('button', { name: 'OK' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/exports/e1/cancel'))
  })

  it("reads an administrator's export from the admin family and offers Download when done", async () => {
    withExport(exportJob({ status: 'done', bytes: 2048, finished_at: Date.now(), expires_at: Date.now() + 86_400_000 }))
    mount({ tab: 'exports', tasks: [exp({ status: 'done', link: '/manage/exports', download: '/api/admin/exports/e1/file', cancel: undefined, finished_at: Date.now() })] })
    fireEvent.click(rowButton(rows('tasks.section.finished')[0]))
    await screen.findByText('tasks.detail.title')
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/exports/e1')
    const dl = await within(modalEl()).findByRole('link', { name: /tasks\.download/ })
    expect(dl.getAttribute('href')).toBe('/api/admin/exports/e1/file')
    expect(within(modalEl()).queryByRole('button', { name: /tasks\.cancel/ })).toBeNull()
  })

  it('closes the export detail when the Tasks drawer closes', async () => {
    withExport(exportJob())
    const view = mount({ tab: 'exports', tasks: [exp({ link: '/exports' })] })
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    await screen.findByText('tasks.detail.title')
    view.rerender(<Harness tab="exports" tasks={[exp({ link: '/exports' })]} open={false} />)
    await waitFor(() => expect(modalShut()).toBe(true))
    view.rerender(<Harness tab="exports" tasks={[exp({ link: '/exports' })]} open />)
    await screen.findAllByText('Audit Q3')
    expect(modalShut()).toBe(true) // reopening does not resurrect it
  })

  it('closes the export detail on a route change', async () => {
    withExport(exportJob())
    mount({ tab: 'exports', tasks: [exp({ link: '/exports' })] })
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    await screen.findByText('tasks.detail.title')
    fireEvent.click(screen.getByRole('link', { name: 'go-elsewhere' }))
    await waitFor(() => expect(modalShut()).toBe(true))
  })

  it('says so with a retry when the export record cannot be read, and recovers', async () => {
    let fail = true
    const base = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string) => {
      if (/\/exports\/e\d$/.test(url)) {
        if (fail) throw Object.assign(new Error('boom'), { status: 500 })
        return exportJob()
      }
      return base(url)
    })
    mount({ tab: 'exports', tasks: [exp({ link: '/exports' })] })
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    const retry = await within(modalEl()).findByRole('button', { name: /common\.retry/ })
    fail = false
    fireEvent.click(retry)
    expect(await within(modalEl()).findByText('export.source.reports')).toBeTruthy()
  })

  it('keeps a spinner, with no error, while the connection is down', async () => {
    const base = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string) => {
      if (/\/exports\/e\d$/.test(url)) throw Object.assign(new Error('offline'), { code: 'network_unavailable' })
      return base(url)
    })
    mount({ tab: 'exports', tasks: [exp({ link: '/exports' })] })
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    await screen.findByText('tasks.detail.title')
    await new Promise((r) => setTimeout(r, 20))
    expect(within(modalEl()).queryByRole('button', { name: /common\.retry/ })).toBeNull()
    expect(modalEl().querySelector('.ant-spin')).not.toBeNull()
  })

  it('loads the detail once the connection is back, after a connection loss', async () => {
    let down = true
    const base = apiMock.get.getMockImplementation()!
    apiMock.get.mockImplementation(async (url: string) => {
      if (/\/exports\/e\d$/.test(url)) {
        if (down) throw Object.assign(new Error('offline'), { code: 'network_unavailable' })
        return exportJob()
      }
      return base(url)
    })
    mount({ tab: 'exports', tasks: [exp({ link: '/exports' })] })
    fireEvent.click(rowButton(rows('tasks.section.active')[0]))
    await screen.findByText('tasks.detail.title')
    await waitFor(() => expect(modalEl().querySelector('.ant-spin')).not.toBeNull())
    down = false
    act(() => {
      window.dispatchEvent(new Event('rp:connection-restored'))
    })
    expect(await within(modalEl()).findByText('export.source.reports')).toBeTruthy()
    expect(modalEl().querySelector('.ant-spin')).toBeNull()
  })

  it('shows the failure reason', async () => {
    withExport(exportJob({ status: 'failed', error: 'over the limit', finished_at: Date.now() }))
    mount({ tab: 'exports', tasks: [exp({ status: 'failed', error: 'over the limit', cancel: undefined, finished_at: Date.now() })] })
    fireEvent.click(rowButton(rows('tasks.section.finished')[0]))
    await screen.findByText('tasks.detail.title')
    expect((await within(modalEl()).findAllByText('over the limit')).length).toBeGreaterThan(0)
  })
})

describe('Tasks drawer row styles', () => {
  const css = readFileSync(resolve(__dirname, '../index.css'), 'utf8')
  it('reveals the actions from the keyboard only, so a mouse click never pins them', () => {
    expect(css).not.toMatch(/\.rp-task-row:focus-within/)
    expect(css).toMatch(/\.rp-task-row:has\(:focus-visible\) \.rp-task-row__acts/)
  })
  it('looks clickable: a pointer and a hover background', () => {
    expect(css).toMatch(/\.rp-task-row__main\[role='button'\] \{ cursor: pointer; \}/)
    expect(css).toMatch(/\.rp-task-row:hover[^{]*\{\s*background/)
  })
})
