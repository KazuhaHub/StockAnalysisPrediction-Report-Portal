import { describe, it, expect, vi, beforeEach } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter, useLocation } from 'react-router'
import QueueTable from './QueueTable'
import { INPUT_VALUE_MAX } from '../lib/batchUi'
import type { BatchJob } from '../api/types'

// Mutable so a test can inject jobs into the mocked API. vi.hoisted keeps it reachable from
// the hoisted vi.mock factory below. `hold` makes every polled GET hang, which is what a slow
// link looks like for the seconds that matter: the page is up and nothing has answered yet.
const store = vi.hoisted(() => ({ urls: [] as string[], holders: [] as unknown[], jobs: [] as BatchJob[], hold: false, detail404: false, fail: '', unchanged: false, admin: true, jobsMine: null as BatchJob[] | null, pendMine: false, summary: { running: 0, waiting: 0, scheduled: 0, budget: 0 } as Record<string, number> }))
const forgetTags = vi.hoisted(() => vi.fn())

// Smoke test: the queue page must mount without crashing (it renders on load, before any
// interaction). Guards against a render-time bug being mistaken for a caching/blank-page
// issue. Empty data is enough to exercise the initial render path.
vi.mock('../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url.includes('/queue')) return Promise.resolve(store.summary)
      if (/\/jobs\/\d+$/.test(url)) {
        if (store.detail404) return Promise.reject(Object.assign(new Error('not found'), { status: 404 }))
        return Promise.resolve({ job: job({ id: Number(url.split('/').pop()) }), counts: {}, running_in_process: true, items: [] })
      }
      if (url.includes('/targets')) return Promise.resolve({ targets: [] })
      return Promise.resolve({ jobs: store.jobs })
    },
    post: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
  errText: (e: unknown) => String((e as Error)?.message ?? e),
}))
// The two polled endpoints go through the conditional-GET helper (304 on an unchanged queue), so
// that is what a test has to answer. `store.unchanged` turns every answer into UNCHANGED, because
// what the table does with a 304 it has no data for is one of the things this suite pins; the
// helper's own revalidation logic is conditionalGet.test.ts's.
const UNCHANGED = vi.hoisted(() => Symbol('unchanged'))
vi.mock('../lib/conditionalGet', () => ({
  UNCHANGED,
  forgetTags,
  getIfChanged: (url: string, holder?: unknown) => {
    store.urls.push(String(url))
    store.holders.push(holder)
    const mine = String(url).includes('scope=mine')
    if (mine && store.pendMine) return new Promise(() => {})
    if (mine && store.jobsMine && String(url).includes('/jobs')) return Promise.resolve({ jobs: store.jobsMine })
    if (store.hold) return new Promise(() => {})
    if (store.fail) return Promise.reject(new Error(store.fail))
    if (store.unchanged) return Promise.resolve(UNCHANGED)
    return String(url).includes('/queue')
      ? Promise.resolve(store.summary)
      : Promise.resolve({ jobs: store.jobs })
  },
}))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: store.admin, user: 'alice' }) }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const job = (over: Partial<BatchJob>): BatchJob => ({
  id: 1,
  target_id: 1,
  status: 'running',
  concurrency: 1,
  max_retries: 0,
  total: 1,
  succeeded: 0,
  partial: 0,
  failed: 0,
  created_by: 'alice',
  created_at: '2026-07-12 00:00:00',
  started_at: '',
  finished_at: '',
  ...over,
})

const mount = () =>
  render(
    <App>
      <MemoryRouter>
        <QueueTable showStats />
      </MemoryRouter>
    </App>,
  )

describe('QueueTable', () => {
  beforeEach(() => {
    store.jobs = []
    store.hold = false
    store.detail404 = false
    store.fail = ''
    store.unchanged = false
    store.admin = true
    store.summary = { running: 0, waiting: 0, scheduled: 0, budget: 0 }
    forgetTags.mockClear()
  })

  // A non-administrator's list holds only their own runs (ADR 0047), so a submitter filter would only
  // ever offer their own name.
  it('offers the submitter filter to an administrator', async () => {
    store.jobs = [job({ status: 'queued' })]
    mount()
    await screen.findByText('queue.source.single')
    expect(Array.from(document.querySelectorAll('[class*="placeholder"]')).some((e) => e.textContent === 'batch.col.createdBy')).toBe(true)
  })

  // The "Max at once" tag compares concurrent runs with the global cap, so it reads the whole queue's
  // running rows, not the caller's own tile (ADR 0047).
  it('shows the whole queue against the cap in the Max-at-once tag for a non-administrator', async () => {
    store.admin = false
    store.summary = { running: 0, running_rows: 0, waiting: 0, scheduled: 0, budget: 4, queue_running_rows: 4, queue_waiting: 1 }
    store.jobs = [job({ status: 'queued' })]
    mount()
    expect(await screen.findByText('4 / 4')).toBeTruthy()
    expect(screen.queryByText('0 / 4')).toBeNull()
  })

  it('hides the submitter filter from a non-administrator', async () => {
    store.admin = false
    store.jobs = [job({ status: 'queued' })]
    mount()
    await screen.findByText('queue.source.single')
    expect(Array.from(document.querySelectorAll('[class*="placeholder"]')).some((e) => e.textContent === 'batch.col.createdBy')).toBe(false)
  })

  it('shows blocked preset runs as waiting with their execution mode', async () => {
    store.jobs = [job({ status: 'queued', run_mode: 'preset', avoid_window: true, window_blocked: true, priority: 'urgent' })]
    mount()
    expect(await screen.findByText('queue.avoidingWindow')).toBeTruthy()
    expect(screen.getByText('queue.mode.preset')).toBeTruthy()
    expect(screen.queryByText('queue.avoidWindow')).toBeNull()
    expect(screen.queryByText('batch.aheadNext')).toBeNull()
  })

  it('replaces running with the preset-wait status between batch rows', async () => {
    store.jobs = [job({ status: 'running', run_mode: 'preset', avoid_window: true, window_blocked: true, succeeded: 1, total: 2 })]
    mount()
    expect(await screen.findByText('queue.avoidingWindow')).toBeTruthy()
    expect(screen.queryByText('batch.status.running')).toBeNull()
    expect(screen.queryByText('queue.avoidWindow')).toBeNull()
  })

  it('keeps wrapped execution-mode tags visually separated', async () => {
    store.jobs = [job({ status: 'queued', run_mode: 'preset', avoid_window: true, priority: 'urgent' })]
    mount()
    const mode = await screen.findByText('queue.mode.preset')
    const group = mode.closest('.ant-space') as HTMLElement | null
    expect(group?.style.columnGap).toBe('6px')
    expect(group?.style.rowGap).toBe('6px')
  })

  it('keeps execution mode visible after a scheduled normal run finishes', async () => {
    store.jobs = [job({ status: 'finished', run_mode: 'scheduled', priority: '30', succeeded: 1 })]
    mount()
    expect(await screen.findByText('queue.mode.scheduled')).toBeTruthy()
    expect(screen.getByText('queue.normal')).toBeTruthy()
  })

  it('distinguishes submission source from execution timing', async () => {
    store.jobs = [
      { ...job({ id: 1, run_mode: 'now' }), surface: 'run' },
      { ...job({ id: 2, run_mode: 'scheduled', run_at: '2099-01-01 12:00:00' }), surface: 'batch' },
      { ...job({ id: 3, run_mode: 'now' }), surface: 'recurring' },
    ] as BatchJob[]
    mount()
    expect(await screen.findByText('queue.source.single')).toBeTruthy()
    expect(screen.getByText('queue.source.batch')).toBeTruthy()
    expect(screen.getByText('queue.source.recurring')).toBeTruthy()
    expect(screen.getByText('queue.mode.scheduled')).toBeTruthy()
  })

  it('mounts and renders the queue card without crashing', async () => {
    render(
      <App>
        <MemoryRouter>
          <QueueTable showStats />
        </MemoryRouter>
      </App>,
    )
    expect(await screen.findByText('queue.title')).toBeTruthy()
  })

  // A chat/agent run carries a full `query=<prompt>` in its inputs; the workflow column must
  // clamp it so one long run can't stretch the row to an unbounded height. The value is now
  // cut in the markup itself, not only by CSS — that is also what keeps the hover tooltip
  // from growing past the viewport. Regression guard for the "infinitely long queue row" bug.
  it('clamps a long chat query in the workflow column instead of rendering it full-height', async () => {
    const longQuery = 'Q'.repeat(1200)
    store.jobs = [job({ inputs: JSON.stringify({ query: longQuery, symbol: '301539' }) })]
    render(
      <App>
        <MemoryRouter>
          <QueueTable showStats />
        </MemoryRouter>
      </App>,
    )
    // …and the clamp is per value, so the entry after the runaway prompt is still visible.
    // (the query normalizes the entry separator's double space down to one)
    const preview = await screen.findByText(`query=${'Q'.repeat(INPUT_VALUE_MAX)}… symbol=301539`)
    expect(preview.className).toMatch(/ant-typography-ellipsis/)
  })

  // The bug this guards: on a slow link the page arrived fully drawn and said "Queue is empty"
  // over five tiles reading zero, seconds before anything had been asked of the server. Both
  // halves are statements about the queue, and neither was one the server had made.
  it('says it is loading instead of declaring the queue empty', async () => {
    store.hold = true
    const { container } = mount()
    expect(await screen.findByText('common.loading')).toBeTruthy()
    expect(screen.queryByText('queue.empty')).toBeNull()
    expect(container.querySelector('.ant-table')).toBeNull()
    // …and the stat tiles hold a dash rather than a zero they cannot vouch for.
    expect(screen.queryAllByText('—').length).toBe(5)
    expect(screen.queryByText('0')).toBeNull()
  })

  it('shows the empty state once the server has actually answered with no runs', async () => {
    mount()
    expect(await screen.findByText('queue.empty')).toBeTruthy()
    expect(screen.queryByText('common.loading')).toBeNull()
  })

  // A 304 means "unchanged since the tag you sent", and the header badge and the Tasks drawer poll
  // the same summary URL. A view that revalidated against their tags could be told "nothing
  // changed" about data it has never had. Two guards, both pinned here.
  it('revalidates only against tags of its own, kept apart for each mount', async () => {
    store.holders = []
    mount()
    await screen.findByText('queue.empty')
    const first = store.holders[0]
    expect(first).toBeTypeOf('object')
    expect(store.holders.every((h) => h === first)).toBe(true)
    cleanup()
    store.holders = []
    mount()
    await screen.findByText('queue.empty')
    expect(store.holders[0]).not.toBe(first)
    // A mount lets its tags go when it goes.
    expect(forgetTags).toHaveBeenCalledWith(undefined, first)
  })

  it('does not open the gate on a 304 for rows it never received', async () => {
    store.unchanged = true
    mount()
    // The answer said "unchanged since the tag you sent" — and this mount sent nothing it holds.
    // Declaring the queue empty off that is the original bug wearing a 304.
    expect(await screen.findByText('common.loading')).toBeTruthy()
    expect(screen.queryByText('queue.empty')).toBeNull()
    expect(screen.queryAllByText('—').length).toBe(5)
  })

  // A first load that never lands must not fall through to the empty state either — with
  // auto-refresh off nothing would ever correct it.
  it('reports a failed first load, with a retry, rather than an empty queue', async () => {
    store.fail = 'boom'
    mount()
    expect(await screen.findByText('common.loadFailedContent')).toBeTruthy()
    expect(screen.getByText('boom')).toBeTruthy()
    expect(screen.getByText('common.retry')).toBeTruthy()
    expect(screen.queryByText('queue.empty')).toBeNull()
  })

  describe('mine / all (ADR 0047)', () => {
    beforeEach(() => {
      store.urls = []
      store.jobsMine = null
      store.pendMine = false
      localStorage.clear()
    })

    it("shows a spinner, not the other scope's runs, until the picked scope has answered", async () => {
      store.admin = true
      store.jobs = [job({ id: 1, target_id: 71 })]
      store.pendMine = true
      mount()
      await screen.findAllByText('#71')
      fireEvent.click(screen.getByText('queue.scope.mine'))
      expect(screen.queryAllByText('#71')).toHaveLength(0)
      expect(document.querySelector('.ant-spin-spinning')).not.toBeNull()
    })

    it('shows a scope it has already loaded at once when switched back', async () => {
      store.admin = true
      store.jobs = [job({ id: 1, target_id: 71 })]
      store.jobsMine = [job({ id: 2, target_id: 33 })]
      mount()
      await screen.findAllByText('#71')
      fireEvent.click(screen.getByText('queue.scope.mine'))
      await screen.findAllByText('#33')
      store.hold = true // the refresh on switching back has not answered yet
      fireEvent.click(screen.getByText('queue.scope.all'))
      expect(screen.getAllByText('#71').length).toBeGreaterThan(0)
      expect(screen.queryAllByText('#33')).toHaveLength(0)
    })

    it('lets an administrator switch to their own runs, for the list and the tiles', async () => {
      store.admin = true
      mount()
      await waitFor(() => expect(store.urls.some((u) => u.includes('/jobs'))).toBe(true))
      expect(store.urls.every((u) => !u.includes('scope=mine'))).toBe(true)
      fireEvent.click(screen.getByText('queue.scope.mine'))
      await waitFor(() => expect(store.urls.some((u) => u.includes('/jobs') && u.includes('scope=mine'))).toBe(true))
      await waitFor(() => expect(store.urls.some((u) => u.includes('/queue') && u.includes('scope=mine'))).toBe(true))
    })

    it('offers no toggle to a non-administrator', async () => {
      store.admin = false
      mount()
      await waitFor(() => expect(store.urls.length).toBeGreaterThan(0))
      expect(screen.queryByText('queue.scope.mine')).toBeNull()
      expect(store.urls.every((u) => !u.includes('scope='))).toBe(true)
    })
  })

  describe('job deep link (?job=id)', () => {
    const Search = () => <span data-testid="search">{useLocation().search}</span>
    const mountAt = (entry: string) =>
      render(
        <App>
          <MemoryRouter initialEntries={[entry]}>
            <Search />
            <QueueTable showStats />
          </MemoryRouter>
        </App>,
      )

    it('opens the detail drawer of the job named in the url', async () => {
      store.jobs = [job({ id: 5 })]
      mountAt('/queue?job=5')
      expect(await screen.findByText('batch.jobTitle:{"id":5}')).toBeTruthy()
    })

    it("says so, without crashing, when the job is unknown or not the reader's", async () => {
      store.detail404 = true
      mountAt('/queue?job=999')
      expect(await screen.findByText('batch.jobNotFound')).toBeTruthy()
    })

    it('clears the parameter when the drawer closes and keeps other parameters', async () => {
      mountAt('/queue?job=5&tab=x')
      await screen.findByText('batch.jobTitle:{"id":5}')
      fireEvent.click(document.querySelector('.ant-drawer-close')!)
      await waitFor(() => expect(screen.getByTestId('search').textContent).toBe('?tab=x'))
    })

    it('puts the job in the url when a row is opened', async () => {
      store.jobs = [job({ id: 8 })]
      mountAt('/queue')
      fireEvent.click(await screen.findByTitle('batch.view'))
      await waitFor(() => expect(screen.getByTestId('search').textContent).toBe('?job=8'))
    })

    it('ignores a job parameter that is not a number', async () => {
      mountAt('/queue?job=abc')
      await waitFor(() => expect(store.urls.length).toBeGreaterThan(0))
      expect(document.querySelector('.ant-drawer-open')).toBeNull()
    })
  })
})
