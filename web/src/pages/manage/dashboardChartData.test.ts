import { expect, it } from 'vitest'
import { chartSeries } from './dashboardChartData'
import { defaultDashboardChart } from './dashboardLayout'
it('filters the chosen range before aggregating and supports cumulative series', () => {
  const days = Array.from({ length: 365 }, (_, i) => ({ key: new Date(Date.UTC(2026,0,i+1)).toISOString().slice(0,10), count: 1 }))
  const config = { ...defaultDashboardChart(), range: 30 as const }
  expect(chartSeries(days, config)).toHaveLength(30)
  expect(chartSeries(days, { ...config, cumulative: true })[29].count).toBe(30)
  const annual = chartSeries(days, { ...config, range: 365 })
  expect(annual).toHaveLength(12)
  expect(annual.reduce((sum,item)=>sum+item.count,0)).toBe(365)
})
it('groups weeks on Monday across year boundaries using civil dates', () => {
  expect(chartSeries([{key:'2025-12-31',count:2},{key:'2026-01-01',count:3}], {...defaultDashboardChart(), interval:'week'})).toEqual([{key:'2025-12-29',count:5}])
})
