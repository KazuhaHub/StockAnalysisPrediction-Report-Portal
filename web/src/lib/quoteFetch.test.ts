import { beforeEach, expect, it, vi } from 'vitest'
import { api, ApiError } from '../api/client'
import { fetchQuoteWithHistory } from './quoteFetch'

vi.mock('../api/client', async (original) => ({ ...await original<typeof import('../api/client')>(), api: { get: vi.fn() } }))

beforeEach(() => vi.mocked(api.get).mockReset())

it('requests historical fallback only after a structured live failure and preserves its window', async () => {
  vi.mocked(api.get).mockRejectedValueOnce(new ApiError(503, 'offline', 'quote_unavailable', { historyAvailable: true }))
    .mockResolvedValueOnce({ historyOnly: true, historyThrough: '2026-10-23' })
  expect(await fetchQuoteWithHistory('/api/quote/sh%3A000852?from=2026-10-22&to=2026-10-23')).toMatchObject({ historyOnly: true })
  expect(api.get).toHaveBeenNthCalledWith(2, '/api/quote-history/sh%3A000852?from=2026-10-22&to=2026-10-23')
})

it('does not amplify an ordinary failure or a successful live read', async () => {
  vi.mocked(api.get).mockResolvedValueOnce({ source: 'tencent' })
  await fetchQuoteWithHistory('/api/quote/600081?range=1m')
  expect(api.get).toHaveBeenCalledTimes(1)
  const error = new ApiError(503, 'offline', 'quote_unavailable')
  vi.mocked(api.get).mockRejectedValueOnce(error)
  await expect(fetchQuoteWithHistory('/api/quote/600081?range=1m')).rejects.toBe(error)
  expect(api.get).toHaveBeenCalledTimes(2)
})

it('preserves the original outage and retry advice when history also fails or is mislabelled', async () => {
  const error = new ApiError(503, 'offline', 'quote_unavailable', { historyAvailable: true, refreshAfterSecs: 120 })
  vi.mocked(api.get).mockRejectedValueOnce(error).mockRejectedValueOnce(new Error('history offline'))
  await expect(fetchQuoteWithHistory('/api/quote/600081?range=1m')).rejects.toBe(error)
  vi.mocked(api.get).mockRejectedValueOnce(error).mockResolvedValueOnce({ source: 'sohu' })
  await expect(fetchQuoteWithHistory('/api/quote/600081?range=1m')).rejects.toBe(error)
})

it('uses current live retry advice instead of a cached historical wake time', async () => {
  vi.mocked(api.get).mockRejectedValueOnce(new ApiError(503, 'offline', 'quote_unavailable', { historyAvailable: true, refreshAfterSecs: 120 }))
    .mockResolvedValueOnce({ historyOnly: true, refreshAt: '2026-10-23T09:30:00+08:00' })
  const response = await fetchQuoteWithHistory('/api/quote/600081?range=1m')
  expect(response.refreshAfterSecs).toBe(120)
  expect(response.refreshAt).toBeUndefined()
})
