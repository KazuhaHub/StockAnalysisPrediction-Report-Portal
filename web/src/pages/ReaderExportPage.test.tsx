import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import type { ExportPolicy, ExportSource } from '../api/types'
import ReaderExportPage from './ReaderExportPage'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const policy = (over: Partial<ExportPolicy> = {}): ExportPolicy => ({ allowed: true, max_reports: 200, daily_quota: 1000, used_today: 120, remaining: 880, unlimited: false, ...over })
const reports: ExportSource = {
  key: 'reports', formats: ['md_zip'], time_field: 'date', total: 30,
  columns: [{ key: 'id', default: true }, { key: 'title', default: true }],
  fields: [{ key: 'symbol', type: 'text', operators: ['is', 'contains'] }],
}
let current: ExportPolicy

const renderPage = () => render(<MemoryRouter initialEntries={['/exports']}><App><ReaderExportPage /></App></MemoryRouter>)

beforeEach(() => {
  localStorage.clear()
  current = policy()
  apiMock.get.mockReset()
  apiMock.post.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/exports/policy') return current
    if (url === '/api/exports/sources') return { sources: [reports] }
    if (url.startsWith('/api/exports?')) return { jobs: [] }
    throw new Error(`unexpected ${url}`)
  })
  apiMock.post.mockResolvedValue({ total: 30, rows: [{ id: 1, title: 'Alpha' }] })
})

describe('ReaderExportPage', () => {
  it('says export is not enabled instead of showing controls the server would refuse', async () => {
    current = policy({ allowed: false })
    renderPage()
    expect(await screen.findByText('export.reader.disabledTitle')).toBeTruthy()
    expect(apiMock.get).not.toHaveBeenCalledWith('/api/exports/sources')
  })

  it('shows the usage line and talks only to the reader endpoints', async () => {
    renderPage()
    expect(await screen.findByText('export.reader.usage:{"used":"120","quota":"1,000","max":"200"}')).toBeTruthy()
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/exports/preview', expect.objectContaining({ source: 'reports' })))
    const urls = [...apiMock.get.mock.calls, ...apiMock.post.mock.calls].map((c) => String(c[0]))
    expect(urls.every((u) => !u.startsWith('/api/admin/'))).toBe(true)
    // No source list and no column gear: a reader has one source and no column choice.
    expect(screen.queryByRole('navigation', { name: 'export.sources' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'export.results.columns' })).toBeNull()
  })
})
