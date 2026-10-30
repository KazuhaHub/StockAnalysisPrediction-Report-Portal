import { act, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, expect, it, vi } from 'vitest'
import AdminOverviewPage from './AdminOverviewPage'
import { defaultDashboardBlocks } from './dashboardLayout'

const api = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('../../api/client', () => ({ api, errText: (e: unknown) => String((e as Error).message) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })
const layout = { blocks: defaultDashboardBlocks(), revision: 'initial' }

beforeEach(() => { api.get.mockReset(); api.put.mockReset() })

it('keeps a card that had data and shows no alert for it while the connection is down', async () => {
  let down = false
  api.get.mockImplementation((url: string) => {
    if (url.endsWith('/dashboard/layout')) return Promise.resolve(layout)
    if (down) return Promise.reject(lost())
    if (url.endsWith('/queue')) return Promise.resolve({ running_rows: 3 })
    return Promise.reject(Object.assign(new Error('forecast broke'), { code: 'internal' }))
  })
  vi.useFakeTimers()
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await act(async () => { await Promise.resolve() })
  expect(within(screen.getByRole('region', { name: 'queue.title' })).getByText('3')).toBeTruthy()
  // A failure that is not the connection still reports itself.
  expect(within(screen.getByRole('region', { name: 'nav.forecast' })).getByRole('alert')).toBeTruthy()
  try {
    down = true
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    const queue = screen.getByRole('region', { name: 'queue.title' })
    expect(within(queue).getByText('3')).toBeTruthy()
    expect(within(queue).queryByRole('alert')).toBeNull()
    expect(screen.queryByText(/network_unavailable/)).toBeNull()
  } finally { vi.useRealTimers() }
})

it('shows no alert for a connection loss before any data arrived, then loads once the connection returns', async () => {
  let down = true
  api.get.mockImplementation((url: string) => {
    if (down) return Promise.reject(lost())
    if (url.endsWith('/dashboard/layout')) return Promise.resolve(layout)
    if (url.endsWith('/queue')) return Promise.resolve({ running_rows: 3 })
    if (url.endsWith('/reports/overview')) return Promise.resolve({ total: 1, today: 0, recent: 0, days: [], kinds: [] })
    return Promise.resolve({ mode: '0' })
  })
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await act(async () => { await Promise.resolve() })
  expect(screen.queryAllByRole('alert')).toHaveLength(0)
  expect(screen.queryByText(/network_unavailable/)).toBeNull()
  down = false
  await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
  expect(await screen.findByText('3')).toBeTruthy()
})
