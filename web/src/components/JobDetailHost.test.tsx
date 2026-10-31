import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { Link, MemoryRouter, useLocation } from 'react-router'
import JobDetailHost from './JobDetailHost'
import { closeJobDetail, openJobDetail, useOpenJobDetail } from '../lib/jobDetailStore'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('../lib/visiblePoll', () => ({ startVisiblePoll: (fn: () => unknown) => { void fn(); return () => {} } }))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: true, user: 'alice' }) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  return { useTranslation: () => ({ t, i18n: { language: 'en-US' } }) }
})

const detail = {
  job: { id: 5, target_id: 9, status: 'finished', concurrency: 1, max_retries: 0, total: 1, succeeded: 1, partial: 0, failed: 0, created_by: 'alice', created_at: '', started_at: '', finished_at: '', inputs: '' },
  counts: {}, running_in_process: true,
  items: [{ id: 50, row_index: 0, inputs: JSON.stringify({ symbol: '600000' }), status: 'succeeded', attempts: 1, run_id: '', conversation_id: '', task_id: '', error: '', started_at: '', finished_at: '' }],
}
function Probe() {
  const loc = useLocation()
  const open = useOpenJobDetail()
  return <span data-testid="state">{`${loc.pathname}${loc.search}|${open ?? ''}`}</span>
}
const mount = (entry: string, onNavigate = vi.fn()) =>
  render(
    <App>
      <MemoryRouter initialEntries={[entry]}>
        <Probe />
        <Link to="/elsewhere">elsewhere</Link>
        <JobDetailHost onNavigate={onNavigate} />
      </MemoryRouter>
    </App>,
  )

beforeEach(() => {
  closeJobDetail()
  apiMock.get.mockReset().mockImplementation(async (url: string) => (url.includes('/targets') ? { targets: [] } : detail))
})

describe('JobDetailHost', () => {
  it('closes the detail and the Tasks drawer when the route changes, e.g. after View report', async () => {
    const onNavigate = vi.fn()
    mount('/', onNavigate)
    act(() => openJobDetail(5))
    const link = await screen.findByRole('link', { name: 'queue.viewReport' })
    onNavigate.mockClear()
    fireEvent.click(link)
    await waitFor(() => expect(screen.getByTestId('state').textContent).toBe('/stock/600000|'))
    expect(onNavigate).toHaveBeenCalled()
  })

  it('Open in queue navigates to the run', async () => {
    mount('/')
    act(() => openJobDetail(5))
    fireEvent.click(await screen.findByRole('button', { name: /queue\.openInQueue/ }))
    await waitFor(() => expect(screen.getByTestId('state').textContent).toBe('/queue?job=5|'))
  })

  it('Open in queue only closes when the page is already on that run', async () => {
    const onNavigate = vi.fn()
    mount('/queue?job=5', onNavigate)
    act(() => openJobDetail(5))
    onNavigate.mockClear()
    fireEvent.click(await screen.findByRole('button', { name: /queue\.openInQueue/ }))
    await waitFor(() => expect(screen.getByTestId('state').textContent).toBe('/queue?job=5|'))
    expect(onNavigate).toHaveBeenCalled()
  })
})
