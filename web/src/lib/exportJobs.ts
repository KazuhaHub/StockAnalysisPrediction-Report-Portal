import type { ExportJob } from '../api/types'

// Helpers shared by the Export page's history table and the task tray.

export const isActiveJob = (j: Pick<ExportJob, 'status'>): boolean => j.status === 'queued' || j.status === 'running'

/** Whole-number progress for the bar; null while the server has no estimate to measure against. */
export function jobProgress(j: Pick<ExportJob, 'status' | 'rows_done' | 'rows_estimate'>): number | null {
  if (j.status === 'done') return 100
  if (j.rows_estimate <= 0) return null
  return Math.max(0, Math.min(100, Math.floor((j.rows_done / j.rows_estimate) * 100)))
}

export function jobActions(j: Pick<ExportJob, 'status'>): { download: boolean; cancel: boolean } {
  return { download: j.status === 'done', cancel: isActiveJob(j) }
}

export const exportDownloadUrl = (id: string): string => `/api/admin/exports/${encodeURIComponent(id)}/file`

/** Active tasks first (oldest first, so the next to finish is on top), then the rest newest first. */
export function sortJobsForTray(jobs: ExportJob[]): ExportJob[] {
  const active = jobs.filter(isActiveJob).sort((a, b) => a.created_at - b.created_at)
  const rest = jobs.filter((j) => !isActiveJob(j)).sort((a, b) => b.created_at - a.created_at)
  return [...active, ...rest]
}

export function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

// A page that submits or cancels a task tells the tray (which lives in the shell, not the page).
export const EXPORTS_CHANGED_EVENT = 'rp:exports-changed'
export interface ExportsChangedDetail {
  /** A task the browser should watch for completion. */
  job?: ExportJob
  /** Open the tray. */
  open?: boolean
}
export function notifyExportsChanged(detail: ExportsChangedDetail = {}): void {
  window.dispatchEvent(new CustomEvent<ExportsChangedDetail>(EXPORTS_CHANGED_EVENT, { detail }))
}
