import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import DashboardChart from './DashboardChart'
import DashboardChartCard from './DashboardChartCard'
import { downloadCSV } from '../../lib/csv'

const chart = vi.hoisted(() => ({ resize: vi.fn(), setOption: vi.fn(), dispose: vi.fn() }))
const engine = vi.hoisted(() => ({ init: vi.fn(), use: vi.fn() }))
vi.mock('echarts/core', () => engine)
vi.mock('../../lib/csv', async importOriginal => ({...await importOriginal<typeof import('../../lib/csv')>(), downloadCSV:vi.fn()}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string, options?: unknown) => `${key}:${JSON.stringify(options)}` }) }))
let resize: ResizeObserverCallback
const disconnect = vi.fn()
beforeEach(() => {
  vi.clearAllMocks()
  engine.init.mockReturnValue(chart)
  vi.stubGlobal('ResizeObserver', class { constructor(callback: ResizeObserverCallback) { resize = callback } observe() {} disconnect = disconnect })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width:600,height:280 } as DOMRect)
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })
const renderChart = (node: React.ReactNode) => render(node, {wrapper:({children}) => <DashboardChartCard title="Reports">{children}</DashboardChartCard>})
const props = { label:'Reports', rows:[{name:'2026-10-25',value:28}], series:{type:'line' as const}, dates:true }
it('resizes when its container changes and releases the instance on unmount', async () => {
  const { unmount } = renderChart(<DashboardChart {...props} />)
  expect(engine.init).toHaveBeenCalledWith(expect.any(HTMLElement),undefined,{renderer:'svg'})
  chart.resize.mockClear()
  await act(async () => { resize([],{} as ResizeObserver); await new Promise(resolve => requestAnimationFrame(resolve)) })
  expect(chart.resize).toHaveBeenCalledWith({width:600,height:280})
  unmount()
  expect(disconnect).toHaveBeenCalled()
  expect(chart.dispose).toHaveBeenCalledTimes(1)
})
it('updates data without replacing the instance and supplies complete tooltip values', () => {
  const { rerender } = renderChart(<DashboardChart {...props} />)
  rerender(<DashboardChart {...props} total={40} rows={[{name:'2026-10-26',value:30}]} />)
  expect(engine.init).toHaveBeenCalledTimes(1)
  const option = chart.setOption.mock.lastCall![0]
  const detail = option.tooltip.formatter([{name:'2026-10-26',value:30}])
  expect(detail).toContain('2026-10-26')
  expect(detail).toContain('"count":30')
  expect(detail).toContain('"percent":"75.0"')
  expect(screen.getByRole('img', {name:'Reports'}).textContent).toContain('2026-10-26: 30')
  expect(screen.queryByRole('table')).toBeNull()
  fireEvent.click(screen.getByRole('button', {name:/manage.dashboard.viewChartData/}))
  expect(screen.getByRole('dialog')).toBeTruthy()
  expect(screen.getByRole('table').textContent).toContain('75.0%')
})
it('changes the legend layout for narrow containers', () => {
  vi.mocked(HTMLElement.prototype.getBoundingClientRect).mockReturnValue({width:320,height:220} as DOMRect)
  renderChart(<DashboardChart {...props} series={{type:'pie'}} total={28} />)
  expect(chart.setOption.mock.lastCall![0].legend.orient).toBe('horizontal')
  expect(chart.setOption.mock.lastCall![0].tooltip.confine).toBe(true)
})

it('places the data action in the header, paginates rows and exports all filtered pages', () => {
  const rows = Array.from({length:60},(_,i) => ({name:`Item ${i}`,value:i}))
  const {container} = renderChart(<DashboardChart {...props} rows={rows} />)
  const action = screen.getByRole('button', {name:/manage.dashboard.viewChartData/})
  expect(container.querySelector('.ant-card-head')!.contains(action)).toBe(true)
  expect(container.querySelector('.rp-dashboard-chart')!.contains(action)).toBe(false)
  fireEvent.click(action)
  expect(screen.getAllByRole('row')).toHaveLength(26)
  fireEvent.click(screen.getByTitle('2'))
  expect(screen.getByRole('table').textContent).toContain('Item 25')
  expect(screen.getByRole('table').textContent).not.toContain('Item 0')
  fireEvent.click(screen.getByRole('button', {name:/manage.dashboard.downloadChartCSV/}))
  expect(vi.mocked(downloadCSV).mock.lastCall![1]).toContain('Item 59,59')
  fireEvent.change(screen.getByRole('searchbox', {name:/manage.dashboard.searchChartData/}),{target:{value:'Item 59'}})
  expect(screen.getAllByRole('row')).toHaveLength(2)
  fireEvent.click(screen.getByRole('button', {name:/manage.dashboard.downloadChartCSV/}))
  expect(vi.mocked(downloadCSV).mock.lastCall![1]).not.toContain('Item 0,0')
})

it('keeps the opened data snapshot stable while dashboard refreshes', () => {
  const {rerender} = renderChart(<DashboardChart {...props} />)
  fireEvent.click(screen.getByRole('button', {name:/manage.dashboard.viewChartData/}))
  rerender(<DashboardChart {...props} rows={[{name:'New data',value:99}]} />)
  expect(screen.getByRole('table').textContent).toContain('2026-10-25')
  expect(screen.getByRole('table').textContent).not.toContain('New data')
})
