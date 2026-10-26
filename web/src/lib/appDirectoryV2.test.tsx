import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'
import { parseAppDirectory, useAppDirectory } from './appDirectoryV2'

const first = '00000000-0000-4000-8000-000000000001'
const second = '00000000-0000-4000-8000-000000000002'
const item = (installationId = first) => ({ installationId, applicationId: 'notes-sample', state: 'installed', openable: true })
const response = (items: unknown[], nextCursor = '') => new Response(JSON.stringify({ items, nextCursor }), { status: 200 })
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

test('directory parser refuses aliases, duplicate/backward IDs, impossible states and dangling cursors', () => {
  expect(parseAppDirectory({ items: [item()], nextCursor: first })?.items).toHaveLength(1)
  for (const page of [
    { items: [item(), item()], nextCursor: '' },
    { items: [item(second), item()], nextCursor: '' },
    { items: [{ ...item(), state: 'healthy' }], nextCursor: '' },
    { items: [{ ...item(), state: 'disabled' }], nextCursor: '' },
    { items: [{ ...item(), applicationId: '../private' }], nextCursor: '' },
    { items: [item()], nextCursor: second },
    { items: [], nextCursor: first },
    { items: Array.from({ length: 33 }, () => item()), nextCursor: '' },
  ]) expect(parseAppDirectory(page)).toBeNull()
})

test('pagination appends a later authorized page and uses the returned cursor', async () => {
  const fetcher = vi.fn(async input => String(input).includes('?after=') ? response([item(second)]) : response([item()], first))
  vi.stubGlobal('fetch', fetcher)
  const { result } = renderHook(() => useAppDirectory('reader'))
  await waitFor(() => expect(result.current.loading).toBe(false))
  act(() => { result.current.load(result.current.nextCursor) })
  await waitFor(() => expect(result.current.items).toHaveLength(2))
  expect(fetcher.mock.calls[1][0]).toBe(`/api/app-platform/apps?after=${first}`)
  expect(result.current.nextCursor).toBe('')
})

test('non-advancing page fails without overwriting earlier cards and retry starts fresh', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => response([item()], first)))
  const { result } = renderHook(() => useAppDirectory('reader'))
  await waitFor(() => expect(result.current.loading).toBe(false))
  act(() => { result.current.load(first) })
  await waitFor(() => expect(result.current.failed).toBe(true))
  expect(result.current.items).toHaveLength(1)
  vi.stubGlobal('fetch', vi.fn(async () => response([])))
  act(() => { result.current.load() })
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current.failed).toBe(false)
  expect(result.current.items).toHaveLength(0)
})

test('account switch aborts stale work and immediately hides the previous audience', async () => {
  let resolveOld: (value: Response) => void = () => {}
  let signal: AbortSignal | undefined
  vi.stubGlobal('fetch', vi.fn((_input, options) => {
    signal = options.signal
    return new Promise<Response>(resolve => { resolveOld = resolve })
  }))
  const { result, rerender, unmount } = renderHook(({ owner }) => useAppDirectory(owner), { initialProps: { owner: 'old-reader' } })
  const oldSignal = signal
  vi.stubGlobal('fetch', vi.fn(async () => response([item(second)])))
  rerender({ owner: 'new-reader' })
  expect(oldSignal?.aborted).toBe(true)
  expect(result.current.items).toHaveLength(0)
  await waitFor(() => expect(result.current.items[0]?.installationId).toBe(second))
  await act(async () => { resolveOld(response([item()])); await Promise.resolve() })
  expect(result.current.items.map(i => i.installationId)).toEqual([second])
  unmount()
})

test('unmount cancels the current pagination request', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => response([item()], first)))
  const { result, unmount } = renderHook(() => useAppDirectory('reader'))
  await waitFor(() => expect(result.current.loading).toBe(false))
  let signal: AbortSignal | undefined
  vi.stubGlobal('fetch', vi.fn((_input, options) => {
    signal = options.signal
    return new Promise<Response>(() => {})
  }))
  act(() => { result.current.load(first) })
  unmount()
  expect(signal?.aborted).toBe(true)
})
