import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client'
import { useLoadFailure, useOnReconnect } from './useOnReconnect'

const restore = () => act(() => { window.dispatchEvent(new Event('rp:connection-restored')) })
const down = () => new ApiError(0, 'network_unavailable', 'network_unavailable')

describe('useOnReconnect', () => {
  it('runs the latest callback when the connection returns, and not after unmount', () => {
    const first = vi.fn()
    const second = vi.fn()
    const { rerender, unmount } = renderHook(({ fn }) => useOnReconnect(fn), { initialProps: { fn: first } })
    rerender({ fn: second })
    restore()
    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledTimes(1)
    unmount()
    restore()
    expect(second).toHaveBeenCalledTimes(1)
  })
})

describe('useLoadFailure', () => {
  it('keeps quiet on a connection loss and bumps the retry key once the connection returns', () => {
    const { result } = renderHook(() => useLoadFailure())
    const before = result.current.retryKey
    let text: string | null = 'unset'
    act(() => { text = result.current.failure(down()) })
    expect(text).toBeNull()
    expect(result.current.retryKey).toBe(before)
    restore()
    expect(result.current.retryKey).toBe(before + 1)
    restore()
    expect(result.current.retryKey).toBe(before + 1)
  })
  it('does not retry for a failure that is not a connection loss', () => {
    const { result } = renderHook(() => useLoadFailure())
    const before = result.current.retryKey
    let text: string | null = null
    act(() => { text = result.current.failure(new ApiError(500, 'boom', 'internal')) })
    expect(text).toBe('boom')
    restore()
    expect(result.current.retryKey).toBe(before)
  })
})
