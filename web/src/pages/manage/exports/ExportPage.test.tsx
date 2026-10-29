import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App, Grid } from 'antd'
import { MemoryRouter } from 'react-router'
import type { ExportFilter, ExportSource } from '../../../api/types'
import { MAX_CONDITIONS, encodeFilter } from '../../../lib/exportFilter'
import ExportPage from './ExportPage'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
// i18next echoes the key; a string second argument is its default-value form.
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const sources: ExportSource[] = [
  {
    key: 'reports',
    formats: ['md_zip', 'csv'],
    time_field: 'date',
    total: 120,
    columns: [
      { key: 'id', default: true },
      { key: 'date', default: true },
      { key: 'title', default: true },
      { key: 'sent_at', default: false },
    ],
    fields: [
      { key: 'kind', type: 'enum', operators: ['is', 'is_not', 'in', 'not_in'], options: ['投资决策', '行业研究'], tree: [{ kind: '投资决策', subtypes: ['汇总', '估值分析'] }, { kind: '行业研究', subtypes: ['白酒'] }] },
      { key: 'rtype', type: 'enum', operators: ['in', 'not_in'], options: ['汇总', '估值分析', '白酒'] },
      { key: 'symbol', type: 'text', operators: ['is', 'contains'] },
      { key: 'date', type: 'date', operators: ['before', 'after', 'between'] },
    ],
  },
  {
    key: 'audit',
    formats: ['csv', 'jsonl'],
    time_field: 'at',
    total: 9000,
    columns: [{ key: 'id', default: true }, { key: 'at', default: true }],
    fields: [{ key: 'action', type: 'enum', operators: ['in'], options: ['report.read'] }, { key: 'at', type: 'datetime', operators: ['between'] }],
  },
]
const rows = [{ id: 1, date: '2026-10-28', title: 'Alpha report' }, { id: 2, date: '2026-10-27', title: 'Beta report' }]
const and = (...items: ExportFilter['items']): ExportFilter => ({ op: 'and', items })

function renderPage(url = '/manage/exports?source=reports') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <App>
        <ExportPage />
      </App>
    </MemoryRouter>,
  )
}
const previewCalls = () => apiMock.post.mock.calls.filter((c) => c[0] === '/api/admin/exports/preview').map((c) => c[1])
const lastPreview = () => previewCalls()[previewCalls().length - 1]

beforeEach(() => {
  localStorage.clear()
  vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ lg: true, md: true } as ReturnType<typeof Grid.useBreakpoint>)
  apiMock.get.mockReset()
  apiMock.post.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/exports/sources') return { sources }
    if (url === '/api/admin/settings') return { timezone: 'Asia/Shanghai' }
    if (url.startsWith('/api/admin/exports')) return { jobs: [] }
    throw new Error('unexpected GET ' + url)
  })
  apiMock.post.mockImplementation(async (url: string) => {
    if (url === '/api/admin/exports/preview') return { total: 2, rows }
    if (url === '/api/admin/exports') return { id: 'job1', source: 'reports', name: 'n', status: 'queued', filter: and(), columns: [] }
    throw new Error('unexpected POST ' + url)
  })
})

describe('ExportPage', () => {
  it('lists the sources with counts and previews the default columns with an empty filter', async () => {
    renderPage()
    expect(await screen.findByText('Alpha report')).toBeTruthy()
    expect(screen.getByText('120')).toBeTruthy()
    expect(screen.getByText('9,000')).toBeTruthy()
    expect(lastPreview()).toEqual({ source: 'reports', filter: and(), columns: ['id', 'date', 'title'], page: 1, page_size: 50 })
    expect(screen.getByText('export.results.showing:{"from":1,"to":2,"total":2}')).toBeTruthy()
  })

  it('restores the filter from the URL as chips and previews with it', async () => {
    const f = and({ field: 'symbol', operator: 'is', values: ['600519'] })
    renderPage(`/manage/exports?source=reports&f=${encodeFilter(f)}`)
    expect(await screen.findByTestId('chip-symbol')).toBeTruthy()
    await waitFor(() => expect(lastPreview()?.filter).toEqual(f))
  })

  it('checking a whole kind in the type tree adds a kind-in chip and filters the preview', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Alpha report')
    await user.click(screen.getByLabelText('投资决策'))
    expect(await screen.findByTestId('chip-kind')).toBeTruthy()
    await waitFor(() => expect(lastPreview()?.filter).toEqual(and({ field: 'kind', operator: 'in', values: ['投资决策'] })))
    expect(lastPreview()?.page).toBe(1)
  })

  it('switching source resets the filter and columns', async () => {
    const user = userEvent.setup()
    renderPage(`/manage/exports?source=reports&f=${encodeFilter(and({ field: 'symbol', operator: 'is', values: ['1'] }))}`)
    await screen.findByTestId('chip-symbol')
    await user.click(screen.getByRole('button', { name: /export\.source\.audit/ }))
    await waitFor(() => expect(lastPreview()).toEqual({ source: 'audit', filter: and(), columns: ['id', 'at'], page: 1, page_size: 50 }))
    expect(screen.queryByTestId('chip-symbol')).toBeNull()
  })

  it('submits an export with the filter, columns, format and report options', async () => {
    const user = userEvent.setup()
    const f = and({ field: 'symbol', operator: 'is', values: ['600519'] })
    renderPage(`/manage/exports?source=reports&f=${encodeFilter(f)}`)
    await screen.findByText('Alpha report')
    await user.click(screen.getByRole('button', { name: /export\.results\.exportAll/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('export.dialog.rows:{"n":2}')).toBeTruthy()
    await user.click(within(dialog).getByRole('button', { name: 'export.dialog.submit' }))
    await waitFor(() => expect(apiMock.post.mock.calls.some((c) => c[0] === '/api/admin/exports')).toBe(true))
    const body = apiMock.post.mock.calls.find((c) => c[0] === '/api/admin/exports')![1]
    expect(body).toMatchObject({
      source: 'reports',
      format: 'md_zip',
      filter: f,
      columns: ['id', 'date', 'title'],
      tz: 'panel',
      options: { front_matter: true, index_csv: true, layout: 'flat' },
    })
    expect(body.name).toContain('export.source.reports')
  })

  it('keeps Advanced and explains why when the tree is not chip-expressible', async () => {
    const user = userEvent.setup()
    const f: ExportFilter = { op: 'or', items: [{ field: 'symbol', operator: 'is', values: ['1'] }, { field: 'symbol', operator: 'is', values: ['2'] }] }
    renderPage(`/manage/exports?source=reports&f=${encodeFilter(f)}`)
    expect((await screen.findAllByTestId('builder-row')).length).toBe(2)
    await user.click(screen.getByText('export.mode.filter'))
    expect(await screen.findByText('export.mode.advancedOnly')).toBeTruthy()
    expect(screen.getAllByTestId('builder-row').length).toBe(2)
  })

  it('shows the condition limit and disables Add condition at 20', async () => {
    const f = and(...Array.from({ length: MAX_CONDITIONS }, (_, i) => ({ field: 'symbol', operator: 'is' as const, values: [String(i)] })))
    renderPage(`/manage/exports?source=reports&f=${encodeFilter(f)}`)
    expect((await screen.findAllByTestId('builder-row')).length).toBe(MAX_CONDITIONS)
    const add = screen.getByRole('button', { name: /export\.builder\.addCondition/ }) as HTMLButtonElement
    expect(add.disabled).toBe(true)
    expect(screen.getByText('export.builder.limitReached:{"max":20}')).toBeTruthy()
  })

  it('adds a condition in the builder and nests a row', async () => {
    const user = userEvent.setup()
    const f: ExportFilter = { op: 'or', items: [{ field: 'symbol', operator: 'is', values: ['1'] }, { field: 'symbol', operator: 'is', values: ['2'] }] }
    renderPage(`/manage/exports?source=reports&f=${encodeFilter(f)}`)
    await screen.findAllByTestId('builder-row')
    await user.click(screen.getByRole('button', { name: /export\.builder\.addCondition/ }))
    expect(screen.getAllByTestId('builder-row').length).toBe(3)
    await user.click(screen.getAllByRole('button', { name: 'export.builder.nest' })[0])
    expect(screen.getAllByTestId('builder-group').length).toBe(2)
  })

  it('remembers the chosen columns per source and previews with them', async () => {
    localStorage.setItem('rp.export.columns.reports', JSON.stringify(['title', 'id']))
    renderPage()
    await screen.findByText('Alpha report')
    expect(lastPreview()?.columns).toEqual(['title', 'id'])
  })
})
