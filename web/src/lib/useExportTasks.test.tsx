import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import type { ExportJob } from '../api/types'
import { notifyExportsChanged } from './exportJobs'
import { ACTIVE_POLL_MS, IDLE_POLL_MS, useExportTasks } from './useExportTasks'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock }))

const job = (over: Partial<ExportJob> = {}): ExportJob => ({
  id: 'j1', source: 'audit', format: 'csv', name: 'Audit', requested_by: 'admin', status: 'running',
  rows_estimate: 100, rows_done: 10, bytes: 0, file_name: '', error: '', created_at: 1, started_at: 1,
  finished_at: 0, expires_at: 0, filter: { op: 'and', items: [] }, columns: [], ...over,
})

let activeJobs: ExportJob[] = []
let byId: Record<string, ExportJob> = {}
const activeCalls = () => apiMock.get.mock.calls.filter((c) => String(c[0]).includes('active=1')).length

beforeEach(() => {
  vi.useFakeTimers()
  activeJobs = []
  byId = {}
  apiMock.get.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url.includes('active=1')) return { jobs: activeJobs }
    if (url.includes('limit=')) return { jobs: [] }
    const id = decodeURIComponent(url.split('/').pop() ?? '')
    if (byId[id]) return byId[id]
    throw Object.assign(new Error('nope'), { status: 404 })
  })
})
afterEach(() => vi.useRealTimers())

const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

describe('useExportTasks polling', () => {
  it('polls slowly while idle', async () => {
    renderHook(() => useExportTasks({ enabled: true }))
    await tick(0)
    expect(activeCalls()).toBe(1)
    await tick(IDLE_POLL_MS - 1000)
    expect(activeCalls()).toBe(1)
    await tick(1000)
    expect(activeCalls()).toBe(2)
  })

  it('speeds up while a task is active and slows down again after', async () => {
    activeJobs = [job()]
    const { result } = renderHook(() => useExportTasks({ enabled: true }))
    await tick(0)
    expect(result.current.active).toHaveLength(1)
    const before = activeCalls()
    await tick(ACTIVE_POLL_MS * 3)
    expect(activeCalls()).toBe(before + 3)
    activeJobs = []
    byId = { j1: job({ status: 'done' }) }
    await tick(ACTIVE_POLL_MS)
    const idleStart = activeCalls()
    await tick(ACTIVE_POLL_MS * 5)
    expect(activeCalls()).toBe(idleStart)
  })

  it('does nothing for non-admins', async () => {
    renderHook(() => useExportTasks({ enabled: false }))
    await tick(IDLE_POLL_MS * 2)
    expect(apiMock.get).not.toHaveBeenCalled()
  })
})

describe('useExportTasks completion', () => {
  it('reports a watched task finishing exactly once', async () => {
    const onFinished = vi.fn()
    activeJobs = [job()]
    renderHook(() => useExportTasks({ enabled: true, onFinished }))
    await tick(0)
    activeJobs = []
    byId = { j1: job({ status: 'done', rows_done: 100 }) }
    await tick(ACTIVE_POLL_MS)
    expect(onFinished).toHaveBeenCalledTimes(1)
    expect(onFinished.mock.calls[0][0].status).toBe('done')
    await tick(IDLE_POLL_MS * 2)
    expect(onFinished).toHaveBeenCalledTimes(1)
  })

  it('reports failure but stays quiet for a cancel', async () => {
    const onFinished = vi.fn()
    activeJobs = [job({ id: 'a' }), job({ id: 'b' })]
    renderHook(() => useExportTasks({ enabled: true, onFinished }))
    await tick(0)
    activeJobs = []
    byId = { a: job({ id: 'a', status: 'failed', error: 'boom' }), b: job({ id: 'b', status: 'canceled' }) }
    await tick(ACTIVE_POLL_MS)
    expect(onFinished).toHaveBeenCalledTimes(1)
    expect(onFinished.mock.calls[0][0].id).toBe('a')
  })

  it('watches a task announced by the page even if it finishes before the first poll sees it', async () => {
    const onFinished = vi.fn()
    renderHook(() => useExportTasks({ enabled: true, onFinished }))
    await tick(0)
    byId = { j9: job({ id: 'j9', status: 'done' }) }
    act(() => notifyExportsChanged({ job: job({ id: 'j9', status: 'queued' }) }))
    await tick(0)
    expect(onFinished).toHaveBeenCalledTimes(1)
  })

  it("announces only the current user's own tasks, while the tray still lists everyone's", async () => {
    const onFinished = vi.fn()
    activeJobs = [job({ id: 'mine', requested_by: 'alice' }), job({ id: 'theirs', requested_by: 'bob' })]
    const { result } = renderHook(() => useExportTasks({ enabled: true, onFinished, currentUser: 'alice' }))
    await tick(0)
    expect(result.current.active).toHaveLength(2)
    activeJobs = []
    byId = { mine: job({ id: 'mine', requested_by: 'alice', status: 'done' }), theirs: job({ id: 'theirs', requested_by: 'bob', status: 'failed', error: 'x' }) }
    await tick(ACTIVE_POLL_MS)
    expect(onFinished).toHaveBeenCalledTimes(1)
    expect(onFinished.mock.calls[0][0].id).toBe('mine')
  })

  it('announces nothing while the current user is not yet known', async () => {
    const onFinished = vi.fn()
    activeJobs = [job()]
    renderHook(() => useExportTasks({ enabled: true, onFinished, currentUser: null }))
    await tick(0)
    activeJobs = []
    byId = { j1: job({ status: 'done' }) }
    await tick(ACTIVE_POLL_MS)
    expect(onFinished).not.toHaveBeenCalled()
  })
})
