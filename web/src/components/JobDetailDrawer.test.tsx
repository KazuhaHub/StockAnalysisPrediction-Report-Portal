import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import type { BatchItem, BatchJob, BatchJobDetail } from '../api/types'
import JobDetailDrawer from './JobDetailDrawer'

const store = vi.hoisted(() => ({ detail: null as unknown, admin: true, user: 'alice' }))
const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('../lib/visiblePoll', () => ({ startVisiblePoll: (fn: () => unknown) => { void fn(); return () => {} } }))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: store.admin, user: store.user }) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  return { useTranslation: () => ({ t, i18n: { language: 'en-US' } }) }
})

const job = (over: Partial<BatchJob> = {}): BatchJob => ({
  id: 7, target_id: 9, status: 'running', concurrency: 1, max_retries: 0, total: 1, succeeded: 0, partial: 0, failed: 0,
  created_by: 'alice', created_at: '2026-10-29 09:00:00', started_at: '2026-10-29 09:01:00', finished_at: '', inputs: '', ...over,
})
const item = (over: Partial<BatchItem> = {}): BatchItem => ({
  id: 70, row_index: 0, inputs: JSON.stringify({ symbol: '002432', depth: 'full' }), status: 'running', attempts: 1, run_id: '', conversation_id: '',
  task_id: '', error: '', started_at: '2026-10-29 09:01:00', finished_at: '', ...over,
})
const detail = (j: Partial<BatchJob> = {}, items: BatchItem[] = [item()]): BatchJobDetail => ({
  job: job(j), counts: { queued: 0, running: 1, succeeded: 0, partial: 0, failed: 0, cancelled: 0 }, running_in_process: true, items,
})
const mount = (props: Partial<React.ComponentProps<typeof JobDetailDrawer>> = {}) =>
  render(
    <App>
      <MemoryRouter>
        <JobDetailDrawer jobId={7} onClose={() => {}} {...props} />
      </MemoryRouter>
    </App>,
  )

beforeEach(() => {
  store.admin = true
  store.user = 'alice'
  apiMock.get.mockReset().mockImplementation(async (url: string) => {
    if (url.includes('/targets')) return { targets: [{ id: 9, name: 'Deep Research' }] }
    return store.detail
  })
  apiMock.post.mockReset().mockResolvedValue({})
  store.detail = detail()
})

describe('single-run layout', () => {
  it('shows key-value inputs and times instead of the row table', async () => {
    mount()
    expect(await screen.findByText('Deep Research')).toBeTruthy()
    expect(screen.getByText('symbol')).toBeTruthy()
    expect(screen.getByText('002432')).toBeTruthy()
    expect(screen.getByText('depth')).toBeTruthy()
    for (const k of ['queue.detail.submitted', 'queue.startedAt', 'queue.finishedAt', 'queue.detail.duration']) expect(screen.getByText(k)).toBeTruthy()
    expect(document.querySelector('.ant-table')).toBeNull()
    expect(document.querySelector('.ant-checkbox')).toBeNull()
    expect(document.querySelector('.ant-pagination')).toBeNull()
  })

  it('shows the error of a failed run and links the report of a succeeded one', async () => {
    store.detail = detail({ status: 'finished', failed: 1 }, [item({ status: 'failed', error: 'model timeout', finished_at: '2026-10-29 09:05:00' })])
    const first = mount()
    expect(await screen.findByText('model timeout')).toBeTruthy()
    first.unmount()
    store.detail = detail({ status: 'finished', succeeded: 1 }, [item({ status: 'succeeded', finished_at: '2026-10-29 09:05:00' })])
    mount()
    const link = await screen.findByRole('link', { name: 'queue.viewReport' })
    expect(link.getAttribute('href')).toBe('/stock/002432')
  })

  it('says how many are ahead of a queued run', async () => {
    store.detail = detail({ status: 'queued', ahead: 2 }, [item({ status: 'queued', started_at: '' })])
    mount()
    expect(await screen.findByText('tasks.queuedAhead:{"n":2}')).toBeTruthy()
  })

  it('cancels the run after confirming, through the existing endpoint', async () => {
    mount()
    fireEvent.click(await screen.findByRole('button', { name: /tasks\.cancel/ }))
    expect(apiMock.post).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole('button', { name: 'OK' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/batch/jobs/7/items/cancel', { item_ids: [70] }))
  })

  it('offers no cancel to a reader who does not own the run', async () => {
    store.admin = false
    store.user = 'bob'
    mount()
    await screen.findByText('Deep Research')
    expect(screen.queryByRole('button', { name: /tasks\.cancel/ })).toBeNull()
  })

  it('reconciles a failed run with a Dify handle, for an administrator', async () => {
    store.detail = detail({ status: 'finished', failed: 1 }, [item({ status: 'failed', run_id: 'r1', finished_at: '2026-10-29 09:05:00' })])
    mount()
    fireEvent.click(await screen.findByRole('button', { name: /queue\.reconcile/ }))
    fireEvent.click(await screen.findByRole('button', { name: 'OK' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/batch/items/70/reconcile'))
  })
})

describe('batch layout', () => {
  const two = () => detail({ total: 2 }, [item(), item({ id: 71, row_index: 1 })])

  it('keeps the row table for a job with several rows', async () => {
    store.detail = two()
    mount()
    await waitFor(() => expect(document.querySelector('.ant-table')).not.toBeNull())
    expect(document.querySelector('.ant-checkbox')).not.toBeNull()
  })

  it('draws the per-row cancel as a close-circle, never the forbidden-looking stop icon', async () => {
    store.detail = two()
    mount()
    await waitFor(() => expect(document.querySelector('.ant-table')).not.toBeNull())
    expect(document.querySelector('.anticon-close-circle')).not.toBeNull()
    expect(document.querySelector('.anticon-stop')).toBeNull()
  })

  it('says so when the job is unknown', async () => {
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.includes('/targets')) return { targets: [] }
      throw Object.assign(new Error('nf'), { status: 404 })
    })
    mount()
    expect(await screen.findByText('batch.jobNotFound')).toBeTruthy()
  })
})

describe('footer', () => {
  it('offers Open in queue only when the host asks for it', async () => {
    const onOpen = vi.fn()
    const first = mount()
    await screen.findByText('Deep Research')
    expect(screen.queryByText('queue.openInQueue')).toBeNull()
    first.unmount()
    mount({ onOpenInQueue: onOpen })
    fireEvent.click(await screen.findByRole('button', { name: /queue\.openInQueue/ }))
    expect(onOpen).toHaveBeenCalled()
  })
})
