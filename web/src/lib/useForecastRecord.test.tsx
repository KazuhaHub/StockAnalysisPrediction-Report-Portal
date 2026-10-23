import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client'
import { useForecastRecord } from './useForecastRecord'
import type { ForecastGroupsResponse } from '../api/types'

const get = vi.hoisted(() => vi.fn())
vi.mock('../api/client', async (original) => ({ ...await original<typeof import('../api/client')>(), api: { get } }))
const response = { ok: true, groups: [], mode: 'month' } as unknown as ForecastGroupsResponse

describe('useForecastRecord', () => {
  beforeEach(() => { get.mockReset() })
  afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })

  it('makes no request for denied or unresolved scope, including after cached success', async () => {
    get.mockResolvedValue(response)
    const view = renderHook(({ allowed }) => useForecastRecord('mode=month', 'reader:all', allowed), { initialProps: { allowed: false } })
    expect(view.result.current.kind).toBe('unavailable')
    expect(get).not.toHaveBeenCalled()
    view.rerender({ allowed: true })
    await waitFor(() => expect(view.result.current.kind).toBe('ready'))
    expect(get).toHaveBeenCalledWith('/api/forecast/groups?mode=month')
    view.rerender({ allowed: false })
    expect(view.result.current.kind).toBe('unavailable')
    expect('data' in view.result.current).toBe(false)
    expect(get).toHaveBeenCalledTimes(1)
    get.mockImplementationOnce(() => new Promise(() => undefined))
    view.rerender({ allowed: true })
    expect(view.result.current.kind).toBe('loading')
  })

  it.each([403, 429])('does not automatically retry status %s', async (status) => {
    get.mockRejectedValue(new ApiError(status, 'refused', status === 403 ? 'forecast_unavailable' : 'forecast_rate_limited'))
    const view = renderHook(() => useForecastRecord('mode=month', 'reader:own', true))
    await waitFor(() => expect(view.result.current.kind).toBe(status === 403 ? 'unavailable' : 'error'))
    expect(get).toHaveBeenCalledTimes(1)
    if (status === 403) {
      act(() => view.result.current.retry())
      expect(get).toHaveBeenCalledTimes(1)
    }
  })

  it('limits busy responses to three extra retries with increasing bounded jitter', async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const timers = vi.spyOn(globalThis, 'setTimeout')
    get.mockRejectedValue(new ApiError(503, 'busy', 'forecast_busy'))
    const view = renderHook(() => useForecastRecord('mode=month', 'reader:all', true))
    await act(async () => { await vi.runAllTimersAsync() })
    expect(get).toHaveBeenCalledTimes(4)
    expect(timers.mock.calls.map((call) => call[1]).filter((delay) => Number(delay) >= 250)).toEqual([375, 750, 1500])
    expect(view.result.current.kind).toBe('error')
    act(() => view.result.current.retry())
    await act(async () => { await vi.runAllTimersAsync() })
    expect(get).toHaveBeenCalledTimes(8)
  })

  it('discards in-flight data across identity changes and cancels pending retries on unmount', async () => {
    let resolve!: (value: ForecastGroupsResponse) => void
    get.mockImplementationOnce(() => new Promise<ForecastGroupsResponse>((done) => { resolve = done }))
    get.mockResolvedValueOnce(response)
    const view = renderHook(({ identity }) => useForecastRecord('mode=month', identity, true), { initialProps: { identity: 'first:all' } })
    view.rerender({ identity: 'second:own' })
    await waitFor(() => expect(view.result.current.kind).toBe('ready'))
    await act(async () => resolve({ ...response, groups: [{ label: 'private-first' }] } as ForecastGroupsResponse))
    expect(view.result.current.kind === 'ready' && view.result.current.data.groups).toEqual([])
    view.unmount()

    vi.useFakeTimers()
    get.mockReset().mockRejectedValue(new ApiError(503, 'busy', 'forecast_busy'))
    const pending = renderHook(() => useForecastRecord('mode=month', 'reader', true))
    await act(async () => { await Promise.resolve() })
    pending.unmount()
    await act(async () => { await vi.runAllTimersAsync() })
    expect(get).toHaveBeenCalledTimes(1)
  })
})
