import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, expect, it, vi } from 'vitest'
import AdminOverviewPage from './AdminOverviewPage'
import { ADMIN_GROUPS } from './adminNavigation'
import { defaultDashboardBlocks, type DashboardLayout } from './dashboardLayout'

const api = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('../../api/client', () => ({ api, errText: (e: unknown) => String(e) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
beforeEach(() => { api.get.mockReset(); api.put.mockReset(); api.get.mockImplementation((url: string) => {
  if (url.endsWith('/dashboard/layout')) return Promise.resolve({ blocks: defaultDashboardBlocks(), revision: 'initial' })
  if (url.endsWith('/queue')) return Promise.resolve({ waiting: 4, running: 2, running_rows: 5, scheduled: 3, budget: 8, done_today: 9 })
  if (url.endsWith('/reports/overview')) return Promise.resolve({ total: 15, today: 2, recent: 9, days: Array.from({ length: 7 }, (_, i) => ({ key: `2026-10-${19+i}`, count: i === 6 ? 2 : 0 })), kinds: [{ key: 'Research', count: 15 }] })
  return Promise.resolve({ mode: '0', readiness: [{ key: 'alerts', ok: false }], due_sets: 7, pending: 12 })
}) })

it('shows actual queue, report and evaluator snapshots with actionable links', async () => {
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  const queue = screen.getByRole('region', { name: 'queue.title' })
  await waitFor(() => expect(within(queue).getByText('5')).toBeTruthy())
  expect(within(queue).getByRole('link').getAttribute('href')).toBe('/queue')
  expect(await screen.findByText('forecast.eval.status.off')).toBeTruthy()
  expect(screen.queryByText('manage.dashboard.attention')).toBeNull()
  expect(screen.queryByText('forecast.eval.readiness.alerts')).toBeNull()
  const shortcuts = screen.getByRole('region', { name: 'manage.dashboard.shortcuts' })
  expect(within(shortcuts).getByRole('link', { name: /nav.batchAdmin/ }).getAttribute('href')).toBe('/manage/batch')
  expect(api.get.mock.calls.map(([url]) => url).sort()).toEqual(['/api/admin/batch/queue', '/api/admin/dashboard/layout', '/api/admin/forecast', '/api/admin/reports/overview'])
})

it('keeps successful cards visible when one source fails and refreshes automatically', async () => {
  api.get.mockImplementation((url: string) => url.endsWith('/dashboard/layout') ? Promise.resolve({ blocks: defaultDashboardBlocks(), revision: 'initial' }) : url.endsWith('/queue') ? Promise.resolve({ running_rows: 3 }) : Promise.reject(new Error('Unavailable')))
  vi.useFakeTimers()
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await act(async () => { await Promise.resolve() })
  expect(screen.getByText('3')).toBeTruthy()
  expect(within(screen.getByRole('region', { name: 'manage.dashboard.reports' })).getByRole('alert')).toBeTruthy()
  expect(screen.queryByText('forecast.eval.status.healthy')).toBeNull()
  expect(screen.queryByRole('button', { name: 'common.refresh' })).toBeNull()
  try {
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(api.get).toHaveBeenCalledTimes(8)
  } finally { vi.useRealTimers() }
})

it('charts report dates and categories, with no account chart, and handles empty reports', async () => {
  const { unmount } = render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  const trend = await screen.findByRole('img', { name: 'manage.dashboard.reportTrend' })
  expect(trend.textContent).toContain('2026-10-25: 2')
  const kinds = await screen.findByRole('img', { name: 'manage.dashboard.reportDonut' })
  expect(kinds.textContent).toContain('Research: 15')
  expect(screen.queryByRole('img', { name: 'manage.dashboard.accountChart' })).toBeNull()
  expect(screen.queryByRole('progressbar')).toBeNull()
  expect(api.get.mock.calls.some(([url]) => url === '/api/admin/users')).toBe(false)
  unmount()
  api.get.mockImplementation((url: string) => Promise.resolve(url.endsWith('/dashboard/layout') ? { blocks: defaultDashboardBlocks(), revision: 'initial' } : url.endsWith('/reports/overview')
    ? { total: 0, today: 0, recent: 0, days: [], kinds: [] } : { mode: '0' }))
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  expect(await screen.findByText('manage.dashboard.noReports')).toBeTruthy()
})

it('includes newly registered navigation pages automatically in the full directory', async () => {
  ADMIN_GROUPS.push({ key: 'new-group', label: 'new.group', pages: [{ key: 'new-feature', label: 'new.feature', icon: null, related: [] }] })
  try {
    render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
    fireEvent.click(screen.getByRole('button', { name: 'manage.dashboard.directory' }))
    expect((await screen.findByRole('link', { name: /new.feature/ })).getAttribute('href')).toBe('/manage/new-feature')
  } finally { ADMIN_GROUPS.pop() }
})

it('pauses hidden snapshots, refreshes on return and cleans up on unmount', async () => {
  vi.useFakeTimers()
  const visibility = vi.spyOn(document, 'visibilityState', 'get')
  visibility.mockReturnValue('visible')
  try {
    const { unmount } = render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
    await act(async () => { await Promise.resolve() })
    expect(api.get).toHaveBeenCalledTimes(4)
    visibility.mockReturnValue('hidden')
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
    expect(api.get).toHaveBeenCalledTimes(4)
    visibility.mockReturnValue('visible')
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(api.get).toHaveBeenCalledTimes(8)
    unmount()
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.advanceTimersByTimeAsync(60_000)
    })
    expect(api.get).toHaveBeenCalledTimes(8)
  } finally {
    visibility.mockRestore()
    vi.useRealTimers()
  }
})

it('edits widgets on the canvas and saves visibility and shortcut tiles globally', async () => {
  let stored: DashboardLayout = { blocks: defaultDashboardBlocks(), revision: 'initial' }
  const original = api.get.getMockImplementation()!
  api.get.mockImplementation((url: string) => url.endsWith('/dashboard/layout') ? Promise.resolve(stored) : original(url))
  api.put.mockImplementation((_url: string, next: DashboardLayout) => {
    stored = { ...next, revision: 'saved' }; return Promise.resolve(stored)
  })
  const { unmount } = render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await waitFor(() => expect(screen.getByRole('button', { name: /manage.dashboard.customize/ }).hasAttribute('disabled')).toBe(false))
  fireEvent.click(screen.getByRole('button', { name: /manage.dashboard.customize/ }))
  const queueWidget = document.querySelector('[data-dashboard-block="queue"]') as HTMLElement
  fireEvent.click(within(queueWidget).getByRole('button', { name: 'manage.dashboard.removeBlock' }))
  fireEvent.click(screen.getByRole('button', { name: /manage.dashboard.addBlock/ }))
  fireEvent.click(await screen.findByRole('button', { name: /manage.dashboard.shortcutBlocks.*nav.group.content/ }))
  fireEvent.click(await screen.findByRole('button', { name: /nav.types/ }))
  fireEvent.click(screen.getByRole('button', { name: 'manage.dashboard.saveGlobal' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  expect(stored.blocks.find((block) => block.id === 'shortcut:types')).toEqual({ id: 'shortcut:types', visible: true, width: 1 })
  expect(stored.blocks.find((block) => block.id === 'queue')?.visible).toBe(false)
  unmount()
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await screen.findByText('manage.dashboard.reportDateHint')
  expect(screen.queryByRole('region', { name: 'queue.title' })).toBeNull()
})

it('offers sample chart previews and optional chart widgets without adding them by default', async () => {
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await waitFor(() => expect(screen.getByRole('button', { name: /manage.dashboard.customize/ }).hasAttribute('disabled')).toBe(false))
  expect(document.querySelector('[data-dashboard-block="report-bars"]')).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: /manage.dashboard.customize/ }))
  fireEvent.click(screen.getByRole('button', { name: /manage.dashboard.addBlock/ }))
  expect((await screen.findAllByText('manage.dashboard.example')).length).toBeGreaterThan(0)
  fireEvent.click(screen.getByRole('button', { name: /manage.dashboard.reportBars/ }))
  expect(document.querySelector('[data-dashboard-block="report-bars"]')).toBeTruthy()
})

it('lets administrators choose report metrics and persists them with the layout', async () => {
  let saved: DashboardLayout | undefined
  api.put.mockImplementation((_url:string,body:DashboardLayout)=>{saved={...body,revision:'saved'};return Promise.resolve(saved)})
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await waitFor(()=>expect(screen.getByRole('button',{name:/manage.dashboard.customize/}).hasAttribute('disabled')).toBe(false))
  fireEvent.click(screen.getByRole('button',{name:/manage.dashboard.customize/}))
  const report=document.querySelector('[data-dashboard-block="reports"]') as HTMLElement
  fireEvent.click(within(report).getByRole('button',{name:'manage.dashboard.chooseMetrics'}))
  fireEvent.click(screen.getByRole('checkbox',{name:'manage.dashboard.yesterdayReports'}))
  fireEvent.click(screen.getByRole('checkbox',{name:'manage.dashboard.yearReports'}))
  fireEvent.click(screen.getByRole('checkbox',{name:'manage.dashboard.totalReports'}))
  fireEvent.click(screen.getByRole('button',{name:'common.done'}))
  expect(within(report).queryByText('manage.dashboard.totalReports')).toBeNull()
  expect(within(report).getByText('manage.dashboard.yesterdayReports')).toBeTruthy()
  fireEvent.click(screen.getByRole('button',{name:'manage.dashboard.saveGlobal'}))
  await waitFor(()=>expect(saved?.blocks.find((block)=>block.id==='reports')?.metrics).toEqual(['today','recent','yesterday','year']))
})

it('allows keyboard dragging to be cancelled from the drag handle without saving', async () => {
  render(<div className="ant-app"><MemoryRouter><AdminOverviewPage /></MemoryRouter></div>)
  await waitFor(() => expect(screen.getByRole('button', { name: /manage.dashboard.customize/ }).hasAttribute('disabled')).toBe(false))
  fireEvent.click(screen.getByRole('button', { name: /manage.dashboard.customize/ }))
  const handle = document.querySelector('[data-dashboard-block="reports"] .rp-dashboard-drag-handle') as HTMLElement
  handle.focus()
  fireEvent.keyDown(handle, { code: 'Space', key: ' ' })
  await waitFor(() => expect(document.querySelector('.rp-dashboard-drag-preview')).not.toBeNull())
  expect(document.querySelector('.rp-dashboard-drag-preview')?.closest('.ant-app')).not.toBeNull()
  fireEvent.keyDown(handle, { code: 'Escape', key: 'Escape' })
  await waitFor(() => expect(document.querySelector('.rp-dashboard-drag-preview')).toBeNull())
  expect(api.put).not.toHaveBeenCalled()
})

it('persists independent chart ranges, grouping and cumulative display', async () => {
  let saved: DashboardLayout | undefined
  api.put.mockImplementation((_url: string, body: DashboardLayout) => { saved = {...body, revision:'saved'}; return Promise.resolve(saved) })
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await waitFor(() => expect(screen.getByRole('button', {name:/manage.dashboard.customize/}).hasAttribute('disabled')).toBe(false))
  fireEvent.click(screen.getByRole('button', {name:/manage.dashboard.customize/}))
  const trend = document.querySelector('[data-dashboard-block="trend"]') as HTMLElement
  fireEvent.click(within(trend).getByRole('button', {name:'manage.dashboard.customizeChart'}))
  fireEvent.mouseDown(screen.getByRole('combobox', {name:'manage.dashboard.chartRange'}))
  fireEvent.click(await screen.findByText('manage.dashboard.range.365', {selector:'.ant-select-item-option-content'}))
  fireEvent.click(screen.getByRole('switch', {name:'manage.dashboard.cumulative'}))
  fireEvent.click(screen.getByRole('button', {name:'common.done'}))
  fireEvent.click(screen.getByRole('button', {name:'manage.dashboard.saveGlobal'}))
  await waitFor(() => expect(saved?.blocks.find(b=>b.id==='trend')?.chart).toEqual({range:365,interval:'auto',cumulative:true,showValues:true,topN:5}))
  expect(saved?.blocks.find(b=>b.id==='kinds')?.chart).toBeUndefined()
})

it.each([['trend', 'column', 'reportBars'], ['kinds', 'donut', 'reportDonut'], ['report-bars', 'line', 'reportTrend'], ['report-donut', 'bar', 'reportKinds']])('switches %s chart presentation and retains the selection after reload', async (id, type, image) => {
  const blocks = defaultDashboardBlocks().map(block => ({ ...block, visible: block.visible || block.id === id }))
  api.get.mockImplementation((url: string) => Promise.resolve(url.endsWith('/dashboard/layout') ? { blocks, revision: 'initial' } : url.endsWith('/reports/overview') ? { days: [{key:'2026-10-25',count:4}], kinds:[{key:'Research',count:4}] } : {mode:'0'}))
  let saved: DashboardLayout | undefined
  api.put.mockImplementation((_url: string, body: DashboardLayout) => { saved = {...body, revision:'saved'}; return Promise.resolve(saved) })
  const view = render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await waitFor(() => expect(screen.getByRole('button', {name:/manage.dashboard.customize/}).hasAttribute('disabled')).toBe(false))
  fireEvent.click(screen.getByRole('button', {name:/manage.dashboard.customize/}))
  const block = document.querySelector(`[data-dashboard-block="${id}"]`) as HTMLElement
  fireEvent.click(within(block).getByRole('button', {name:'manage.dashboard.customizeChart'}))
  const range = screen.getByRole('combobox', {name:'manage.dashboard.chartRange'}).closest('.ant-select') as HTMLElement
  expect(range.style.display).not.toBe('block')
  if (id === 'kinds') {
    fireEvent.mouseDown(screen.getByRole('combobox', {name:'manage.dashboard.chartType'}))
    fireEvent.click(await screen.findByText('manage.dashboard.chartType.bar', {selector:'.ant-select-item-option-content'}))
  }
  fireEvent.mouseDown(screen.getByRole('combobox', {name:'manage.dashboard.chartType'}))
  fireEvent.click(await screen.findByText(`manage.dashboard.chartType.${type}`, {selector:'.ant-select-item-option-content'}))
  fireEvent.click(screen.getByRole('button', {name:'common.done'}))
  expect(within(block).getByRole('img', {name:`manage.dashboard.${image}`})).toBeTruthy()
  fireEvent.click(screen.getByRole('button', {name:'manage.dashboard.saveGlobal'}))
  await waitFor(() => expect(saved?.blocks.find(block=>block.id===id)?.chart?.type).toBe(type))
  view.unmount()
  api.get.mockImplementation((url: string) => Promise.resolve(url.endsWith('/dashboard/layout') ? saved : url.endsWith('/reports/overview') ? {days:[], kinds:[]} : {mode:'0'}))
  render(<MemoryRouter><AdminOverviewPage /></MemoryRouter>)
  await waitFor(() => expect(within(document.querySelector(`[data-dashboard-block="${id}"]`) as HTMLElement).getByRole('img', {name:`manage.dashboard.${image}`})).toBeTruthy())
})
