import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import ReportExportPage from './ReportExportPage'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
const downloadMock = vi.hoisted(() => vi.fn())

vi.mock('../../api/client', () => ({
  api: apiMock,
  qs: (p: Record<string, unknown>) => {
    const sp = new URLSearchParams()
    for (const [k, v] of Object.entries(p)) if (v !== undefined && v !== null && v !== '') sp.set(k, String(v))
    const s = sp.toString()
    return s ? `?${s}` : ''
  },
  errText: () => 'Request failed',
}))
vi.mock('../../lib/reportExport', () => ({ downloadReportMarkdown: downloadMock }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const page = {
  reports: [
    { id: 11, title: '600001 Alpha 投资决策建议', symbol: '600001', name: 'Alpha', rtype: '投资决策建议', date: '2026-10-18', generator_version: 'V3.17', sent_at: '' },
    { id: 12, title: '600002 Beta 投资决策建议', symbol: '600002', name: 'Beta', rtype: '投资决策建议', date: '2026-10-19', generator_version: 'V3.17', sent_at: '' },
  ],
  total: 2,
  page: 1,
  page_size: 20,
  types: ['投资决策建议'],
  generator_versions: ['V3.16.2', 'V3.17'],
}

function renderPanel() {
  return render(
    <App>
      <ReportExportPage />
    </App>,
  )
}

describe('ReportExportPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    downloadMock.mockReset()
    apiMock.get.mockResolvedValue(page)
    downloadMock.mockResolvedValue(undefined)
  })

  it('lists matching reports from the export endpoint', async () => {
    renderPanel()
    expect(await screen.findByText('600001 Alpha 投资决策建议')).toBeTruthy()
    expect(apiMock.get.mock.calls[0][0]).toMatch(/^\/api\/admin\/report-exports\?/)
  })

  it('downloads the whole filtered result by filters, not by the visible page', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByText('600001 Alpha 投资决策建议')
    await user.click(screen.getByRole('button', { name: /reportExport\.downloadAll/ }))
    await waitFor(() => expect(downloadMock).toHaveBeenCalledTimes(1))
    const body = downloadMock.mock.calls[0][0]
    expect(body.ids).toBeUndefined()
    expect(body.filters).toEqual({ q: '', rtype: '', generator_version: '', date_from: '', date_to: '' })
  })

  it('downloads only the selected rows by id', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByText('600002 Beta 投资决策建议')
    const boxes = screen.getAllByRole('checkbox')
    // [0] is the select-all header; rows follow in order.
    await user.click(boxes[2])
    const selected = screen.getByRole('button', { name: /reportExport\.downloadSelected/ })
    expect((selected as HTMLButtonElement).disabled).toBe(false)
    await user.click(selected)
    await waitFor(() => expect(downloadMock).toHaveBeenCalledTimes(1))
    expect(downloadMock.mock.calls[0][0]).toEqual({ ids: [12] })
  })

  it('disables the selected-download button with nothing selected', async () => {
    renderPanel()
    await screen.findByText('600001 Alpha 投资决策建议')
    expect((screen.getByRole('button', { name: /reportExport\.downloadSelected/ }) as HTMLButtonElement).disabled).toBe(true)
  })
})
