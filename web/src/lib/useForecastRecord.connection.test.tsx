import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client'
import { useForecastRecord } from './useForecastRecord'

const get = vi.hoisted(() => vi.fn())
vi.mock('../api/client', async (original) => ({ ...await original<typeof import('../api/client')>(), api: { get } }))

describe('useForecastRecord on a lost connection', () => {
  it('stays loading instead of reporting an error, and asks again when the connection returns', async () => {
    get.mockReset()
      .mockRejectedValueOnce(new ApiError(0, 'network_unavailable', 'network_unavailable'))
      .mockResolvedValueOnce({ groups: [] })
    const { result } = renderHook(() => useForecastRecord('mode=monthly', 'me', true))
    await waitFor(() => expect(get).toHaveBeenCalledTimes(1))
    await act(async () => { await Promise.resolve() })
    expect(result.current.kind).toBe('loading')
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    await waitFor(() => expect(result.current.kind).toBe('ready'))
    expect(get).toHaveBeenCalledTimes(2)
  })

  it('still reports a server error', async () => {
    get.mockReset().mockRejectedValue(new ApiError(500, 'boom', 'internal'))
    const { result } = renderHook(() => useForecastRecord('mode=monthly', 'me', true))
    await waitFor(() => expect(result.current.kind).toBe('error'))
  })
})
