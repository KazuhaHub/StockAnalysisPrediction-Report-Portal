import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import type { ExportJob } from '../api/types'
import { notifyExportsChanged } from '../lib/exportJobs'
import ExportTaskTray from './ExportTaskTray'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('../auth', () => ({ useAuth: () => ({ user: 'admin' }) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const job = (over: Partial<ExportJob> = {}): ExportJob => ({
  id: 'j1', source: 'audit', format: 'csv', name: 'Audit Q3', requested_by: 'admin', status: 'running', rows_estimate: 100, rows_done: 40,
  bytes: 0, file_name: '', error: '', created_at: 1, started_at: 1, finished_at: 0, expires_at: 0, filter: { op: 'and', items: [] }, columns: [], ...over,
})
let activeJobs: ExportJob[] = []
let recentJobs: ExportJob[] = []
let detail: ExportJob | null = null

const renderTray = () => render(<MemoryRouter><App><ExportTaskTray /></App></MemoryRouter>)

beforeEach(() => {
  activeJobs = []
  recentJobs = []
  detail = null
  apiMock.get.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url.includes('active=1')) return { jobs: activeJobs }
    if (url.includes('limit=')) return { jobs: recentJobs }
    if (detail) return detail
    throw new Error('no job')
  })
})

describe('ExportTaskTray', () => {
  it('says what appears there when there are no tasks', async () => {
    const user = userEvent.setup()
    renderTray()
    await user.click(screen.getByRole('button', { name: 'export.tray.title' }))
    expect(await screen.findByText('export.tray.empty')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'export.tray.close' }))
    await waitFor(() => expect(screen.queryByText('export.tray.empty')).toBeNull())
  })

  it('badges active tasks and lists them with progress ahead of finished ones', async () => {
    const user = userEvent.setup()
    activeJobs = [job()]
    recentJobs = [job({ id: 'old', name: 'Old one', status: 'done', bytes: 1024, created_at: 99 })]
    renderTray()
    expect(await screen.findByText('1', { selector: '.ant-scroll-number-only-unit' })).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'export.tray.title' }))
    const items = await screen.findAllByRole('listitem')
    expect(items[0].textContent).toContain('Audit Q3')
    expect(items[1].textContent).toContain('Old one')
    expect(screen.getByRole('link', { name: /export\.tray\.download/ }).getAttribute('href')).toBe('/api/admin/exports/old/file')
  })

  it('toasts once when a watched task finishes, with a download action', async () => {
    activeJobs = [job()]
    renderTray()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/admin/exports?active=1'))
    await act(async () => {
      await Promise.resolve()
    })
    activeJobs = []
    detail = job({ status: 'done', bytes: 10 })
    act(() => notifyExportsChanged())
    expect(await screen.findByText('export.toast.done:{"name":"Audit Q3"}')).toBeTruthy()
    expect(screen.getAllByRole('link', { name: /export\.tray\.download/ }).some((a) => a.getAttribute('href') === '/api/admin/exports/j1/file')).toBe(true)
    act(() => notifyExportsChanged())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50))
    })
    expect(screen.getAllByText('export.toast.done:{"name":"Audit Q3"}')).toHaveLength(1)
  })

  it('toasts a failure with its reason', async () => {
    activeJobs = [job()]
    renderTray()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/admin/exports?active=1'))
    await act(async () => {
      await Promise.resolve()
    })
    activeJobs = []
    detail = job({ status: 'failed', error: 'disk full' })
    act(() => notifyExportsChanged())
    expect(await screen.findByText('export.toast.failed:{"name":"Audit Q3"}')).toBeTruthy()
    expect(await screen.findByText('disk full')).toBeTruthy()
  })

  it('opens when the page asks it to', async () => {
    renderTray()
    act(() => notifyExportsChanged({ open: true }))
    expect(await screen.findByText('export.tray.empty')).toBeTruthy()
  })

  it("stays quiet when another administrator's task finishes", async () => {
    activeJobs = [job({ requested_by: 'someone-else' })]
    renderTray()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/admin/exports?active=1'))
    await act(async () => {
      await Promise.resolve()
    })
    activeJobs = []
    detail = job({ requested_by: 'someone-else', status: 'failed', error: 'disk full' })
    act(() => notifyExportsChanged())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50))
    })
    expect(screen.queryByText('export.toast.failed:{"name":"Audit Q3"}')).toBeNull()
  })
})

describe('ExportTaskTray for a reader', () => {
  it('reads and cancels through the reader endpoints and links to their own export page', async () => {
    const user = userEvent.setup()
    activeJobs = [job({ requested_by: 'admin' })]
    recentJobs = [job({ id: 'old', name: 'Old one', status: 'done', bytes: 1024, created_at: 99 })]
    render(
      <MemoryRouter>
        <App>
          <ExportTaskTray mode="reader" />
        </App>
      </MemoryRouter>,
    )
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/exports?active=1'))
    expect(apiMock.get.mock.calls.every((c) => !String(c[0]).includes('/api/admin/'))).toBe(true)
    await user.click(screen.getByRole('button', { name: 'export.tray.title' }))
    expect((await screen.findByRole('link', { name: /export\.tray\.download/ })).getAttribute('href')).toBe('/api/exports/old/file')
    expect(screen.getByRole('link', { name: 'export.tray.viewAll' }).getAttribute('href')).toBe('/exports')
    await user.click(screen.getByRole('button', { name: 'export.history.cancel' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/exports/j1/cancel'))
  })
})
