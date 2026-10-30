import { describe, expect, it } from 'vitest'
import { fmtMs, formatCell, panelToday } from './exportTime'
import { exportDownloadUrl, jobActions, jobProgress } from './exportJobs'
import type { ExportJob } from '../api/types'

describe('panel time', () => {
  it('decides the civil date in the panel zone', () => {
    const now = new Date('2026-10-29T18:30:00Z')
    expect(panelToday('Asia/Shanghai', now)).toBe('2026-10-30')
    expect(panelToday('UTC', now)).toBe('2026-10-29')
  })
  it('renders instants in the panel zone and leaves other cells alone', () => {
    expect(formatCell('at', '2026-10-29T16:00:00Z', 'Asia/Shanghai')).toBe('2026-10-30 00:00:00')
    expect(formatCell('created_at', 1788019200000, 'UTC')).toBe(fmtMs(1788019200000, 'UTC'))
    expect(formatCell('symbol', '600519', 'UTC')).toBe('600519')
    expect(formatCell('date', '2026-10-29', 'UTC', 'date')).toBe('2026-10-29')
    expect(formatCell('detail', null, 'UTC')).toBe('')
  })
})

const job = (over: Partial<ExportJob>): ExportJob => ({
  id: 'x', source: 'audit', format: 'csv', name: 'n', requested_by: 'a', status: 'queued', rows_estimate: 0, rows_done: 0,
  bytes: 0, file_name: '', error: '', created_at: 0, started_at: 0, finished_at: 0, expires_at: 0, filter: { op: 'and', items: [] }, columns: [], ...over,
})

describe('export jobs', () => {
  it('offers download only when done and cancel only while active', () => {
    expect(jobActions({ status: 'done' })).toEqual({ download: true, cancel: false })
    expect(jobActions({ status: 'queued' })).toEqual({ download: false, cancel: true })
    expect(jobActions({ status: 'running' })).toEqual({ download: false, cancel: true })
    for (const s of ['failed', 'canceled', 'expired'] as const) expect(jobActions({ status: s })).toEqual({ download: false, cancel: false })
  })
  it('computes progress without dividing by an unknown estimate', () => {
    expect(jobProgress(job({ status: 'running', rows_done: 25, rows_estimate: 100 }))).toBe(25)
    expect(jobProgress(job({ status: 'running', rows_done: 5, rows_estimate: 0 }))).toBeNull()
    expect(jobProgress(job({ status: 'done', rows_done: 7, rows_estimate: 0 }))).toBe(100)
    expect(jobProgress(job({ status: 'running', rows_done: 500, rows_estimate: 100 }))).toBe(100)
  })
  it('builds the download url', () => {
    expect(exportDownloadUrl('abc')).toBe('/api/admin/exports/abc/file')
  })
})
