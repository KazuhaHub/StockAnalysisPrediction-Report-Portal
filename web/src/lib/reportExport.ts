import { ApiError, noteSessionLost } from '../api/client'

// Bulk Markdown export from the storage console. The selection is either an explicit id list or
// the filter set, never both: "everything matching" must stay true for rows beyond the loaded page.
export interface ReportExportFilters {
  q: string
  rtype: string
  generator_version: string
  date_from: string
  date_to: string
}

export type ReportExportSelection = { ids: number[] } | { filters: ReportExportFilters }

// filenameFromDisposition reads the RFC 5987 filename* the server sets; null when absent.
export function filenameFromDisposition(header: string | null): string | null {
  const m = /filename\*=UTF-8''([^;]+)/i.exec(header ?? '')
  if (!m) return null
  try {
    return decodeURIComponent(m[1].replace(/\+/g, ' '))
  } catch {
    return null
  }
}

export async function downloadReportMarkdown(selection: ReportExportSelection): Promise<void> {
  const res = await fetch('/api/admin/report-exports/download', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(selection),
    credentials: 'same-origin',
  })
  if (!res.ok) {
    let data: any = null
    try {
      data = await res.json()
    } catch {
      // Non-JSON error bodies fall through to the status text.
    }
    const code = typeof data?.code === 'string' ? data.code : undefined
    noteSessionLost(res.status, code)
    throw new ApiError(res.status, data?.error || res.statusText || 'export failed', code, data)
  }
  const blob = await res.blob()
  const name = filenameFromDisposition(res.headers.get('Content-Disposition')) || 'reports_markdown.zip'
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}
