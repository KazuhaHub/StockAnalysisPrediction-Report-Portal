import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import type { TaskItem } from '../api/types'
import { notifyExportsChanged } from './exportJobs'
import { ACTIVE_POLL_MS, IDLE_POLL_MS, useMyTasks } from './useMyTasks'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock }))

const task = (over: Partial<TaskItem> = {}): TaskItem => ({
  kind: 'export', id: 'e1', name: 'Audit', status: 'running', done: 10, total: 100, created_at: 1, finished_at: 0, link: '/exports', ...over,
})

let tasks: TaskItem[] = []
const calls = () => apiMock.get.mock.calls.length

beforeEach(() => {
  vi.useFakeTimers()
  tasks = []
  apiMock.get.mockReset()
  apiMock.get.mockImplementation(async () => ({ tasks }))
})
afterEach(() => vi.useRealTimers())

const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

describe('useMyTasks polling', () => {
  it('asks for the caller\'s own tasks, ten at a time', async () => {
    renderHook(() => useMyTasks({ enabled: true }))
    await tick(0)
    expect(apiMock.get).toHaveBeenCalledWith('/api/tasks?limit=10')
  })

  it('polls slowly while idle', async () => {
    renderHook(() => useMyTasks({ enabled: true }))
    await tick(0)
    expect(calls()).toBe(1)
    await tick(IDLE_POLL_MS - 1000)
    expect(calls()).toBe(1)
    await tick(1000)
    expect(calls()).toBe(2)
  })

  it('speeds up while a task is active and slows down again after', async () => {
    tasks = [task()]
    const { result } = renderHook(() => useMyTasks({ enabled: true }))
    await tick(0)
    expect(result.current.active).toHaveLength(1)
    const before = calls()
    await tick(ACTIVE_POLL_MS * 3)
    expect(calls()).toBe(before + 3)
    tasks = [task({ status: 'done' })]
    await tick(ACTIVE_POLL_MS)
    const idleStart = calls()
    await tick(ACTIVE_POLL_MS * 5)
    expect(calls()).toBe(idleStart)
  })

  it('does nothing while disabled', async () => {
    renderHook(() => useMyTasks({ enabled: false }))
    await tick(IDLE_POLL_MS * 2)
    expect(apiMock.get).not.toHaveBeenCalled()
  })
})

describe('useMyTasks completion', () => {
  it('reports a task seen active that is now terminal, exactly once', async () => {
    const onFinished = vi.fn()
    tasks = [task()]
    renderHook(() => useMyTasks({ enabled: true, onFinished }))
    await tick(0)
    tasks = [task({ status: 'done' })]
    await tick(ACTIVE_POLL_MS)
    expect(onFinished).toHaveBeenCalledTimes(1)
    expect(onFinished.mock.calls[0][0]).toMatchObject({ id: 'e1', status: 'done' })
    await tick(ACTIVE_POLL_MS * 3)
    expect(onFinished).toHaveBeenCalledTimes(1)
  })

  it('does not announce tasks that were already finished when first seen', async () => {
    const onFinished = vi.fn()
    tasks = [task({ status: 'done' }), task({ kind: 'run', id: '7', status: 'finished' })]
    renderHook(() => useMyTasks({ enabled: true, onFinished }))
    await tick(IDLE_POLL_MS * 2)
    expect(onFinished).not.toHaveBeenCalled()
  })

  it('tells a run from an export that share an id', async () => {
    const onFinished = vi.fn()
    tasks = [task({ kind: 'run', id: '1', status: 'running' }), task({ kind: 'export', id: '1', status: 'running' })]
    renderHook(() => useMyTasks({ enabled: true, onFinished }))
    await tick(0)
    tasks = [task({ kind: 'run', id: '1', status: 'finished' }), task({ kind: 'export', id: '1', status: 'running' })]
    await tick(ACTIVE_POLL_MS)
    expect(onFinished).toHaveBeenCalledTimes(1)
    expect(onFinished.mock.calls[0][0]).toMatchObject({ kind: 'run' })
  })

  it('watches an export announced by the page even if it finishes before the first poll sees it', async () => {
    const onFinished = vi.fn()
    renderHook(() => useMyTasks({ enabled: true, onFinished }))
    await tick(0)
    tasks = [task({ status: 'done' })]
    act(() => notifyExportsChanged({ job: { id: 'e1' } as never }))
    await tick(0)
    expect(onFinished).toHaveBeenCalledTimes(1)
  })

  it('refreshes at once when an export is submitted', async () => {
    renderHook(() => useMyTasks({ enabled: true }))
    await tick(0)
    const before = calls()
    act(() => notifyExportsChanged())
    await tick(0)
    expect(calls()).toBe(before + 1)
  })

  it('refreshes when the window regains focus', async () => {
    renderHook(() => useMyTasks({ enabled: true }))
    await tick(0)
    const before = calls()
    act(() => {
      window.dispatchEvent(new Event('focus'))
    })
    await tick(0)
    expect(calls()).toBe(before + 1)
  })
})
