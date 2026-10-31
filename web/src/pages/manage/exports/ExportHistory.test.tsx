import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import type { ExportJob } from '../../../api/types'
import ExportHistory from './ExportHistory'
import { useExportLabels } from './labels'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const job = (over: Partial<ExportJob>): ExportJob => ({
  id: 'x', source: 'audit', format: 'csv', name: 'job', requested_by: 'admin', status: 'done', rows_estimate: 10, rows_done: 10,
  bytes: 2048, file_name: 'a.csv', error: '', created_at: 1_790_000_000_000, started_at: 0, finished_at: 0, expires_at: 1_790_600_000_000,
  filter: { op: 'and', items: [] }, columns: [], ...over,
})

function Harness({ onReuse = () => {} }: { onReuse?: (j: ExportJob) => void }) {
  const labels = useExportLabels()
  return <ExportHistory tz="UTC" labels={labels} onReuse={onReuse} />
}
const renderIt = (onReuse?: (j: ExportJob) => void) => render(<App><Harness onReuse={onReuse} /></App>)

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.post.mockReset()
  apiMock.get.mockResolvedValue({
    jobs: [
      job({ id: 'd1', name: 'Done one' }),
      job({ id: 'r1', name: 'Running one', status: 'running', rows_done: 5, bytes: 0, expires_at: 0 }),
      job({ id: 'f1', name: 'Failed one', status: 'failed', error: 'disk full', bytes: 0, expires_at: 0 }),
    ],
  })
  apiMock.post.mockResolvedValue({})
})

describe('ExportHistory', () => {
  it('loads the 50 most recent tasks', async () => {
    renderIt()
    expect(await screen.findByText('Done one')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/exports?limit=50')
  })

  it('offers a download link only for a finished task', async () => {
    renderIt()
    await screen.findByText('Done one')
    const links = screen.getAllByRole('link', { name: /export\.history\.download/ })
    expect(links).toHaveLength(1)
    expect(links[0].getAttribute('href')).toBe('/api/admin/exports/d1/file')
  })

  it('offers cancel only while active and posts to the cancel endpoint after confirming', async () => {
    const user = userEvent.setup()
    renderIt()
    await screen.findByText('Running one')
    const cancels = screen.getAllByRole('button', { name: 'export.history.cancel' })
    expect(cancels).toHaveLength(1)
    await user.click(cancels[0])
    await user.click(await screen.findByRole('button', { name: /^OK$/ }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/exports/r1/cancel'))
  })

  it('shows why a task failed', async () => {
    renderIt()
    expect(await screen.findByText('disk full')).toBeTruthy()
  })

  it('hands a task back so its filter can be reused', async () => {
    const user = userEvent.setup()
    const onReuse = vi.fn()
    renderIt(onReuse)
    await screen.findByText('Done one')
    await user.click(screen.getAllByRole('button', { name: 'export.history.reuse' })[0])
    expect(onReuse.mock.calls[0][0].id).toBe('d1')
  })
})

// The task list is a flat data table like the audit log and the queue: it fits the card's width rather
// than scrolling sideways, and its columns are chosen and sized through the shared display control.
describe('ExportHistory table', () => {
  beforeEach(() => localStorage.clear())

  it('uses the shared adaptive table with its display control', async () => {
    const { container } = renderIt()
    await screen.findByText('Done one')
    expect(container.querySelector('.ant-table-wrapper.rp-adaptive-table')).not.toBeNull()
    expect(screen.getByRole('button', { name: /table\.display/ })).toBeTruthy()
  })

  it('fits every column into the width by default, so an empty list has no sideways scroll', async () => {
    apiMock.get.mockResolvedValue({ jobs: [] })
    const { container } = renderIt()
    await screen.findByText('export.history.empty')
    const widths = [...container.querySelectorAll('col')].map((c) => (c as HTMLElement).style.width).filter(Boolean)
    expect(widths.length).toBeGreaterThan(0)
    expect(widths.every((w) => w.endsWith('%'))).toBe(true)
  })

  it('keeps the requester column, and its saved layout, to the admin view', async () => {
    const labels = () => [...document.querySelectorAll('th')].map((th) => th.textContent)
    const admin = renderIt()
    await screen.findByText('Done one')
    expect(labels().some((l) => l?.includes('export.history.requester'))).toBe(true)
    admin.unmount()

    function Reader() {
      return <ExportHistory tz="UTC" labels={useExportLabels()} onReuse={() => {}} mode="reader" />
    }
    render(<App><Reader /></App>)
    await screen.findByText('Done one')
    expect(labels().some((l) => l?.includes('export.history.requester'))).toBe(false)
  })

  it('takes no horizontal scroll width beyond the fitted columns', async () => {
    const { container } = renderIt()
    await screen.findByText('Done one')
    const table = container.querySelector('.ant-table-content table, .ant-table-body table') as HTMLElement | null
    expect(table?.style.minWidth ?? '').not.toBe('max-content')
  })
})
