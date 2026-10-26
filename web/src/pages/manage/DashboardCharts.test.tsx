import { render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { MetricChart, ReportBars, ReportDonut, ReportKinds, ReportTrend } from './DashboardCharts'
import { defaultDashboardChart } from './dashboardLayout'

const captured = vi.hoisted(() => ({ value: {} as Record<string, any> }))
vi.mock('./DashboardChart', () => ({ default: (props: any) => {
  captured.value = props
  return <div role="img" aria-label={props.label}>{props.rows.map((row: any) => `${row.name}: ${row.value}`).join(', ')}</div>
} }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string, values?: Record<string, unknown>) => `${key}${values ? ` ${JSON.stringify(values)}` : ''}` }) }))

it.each([[ReportTrend, 'line'], [ReportBars, 'bar']] as const)('preserves full dates and values independently of visible labels', (Chart, type) => {
  render(<Chart days={[{key:'2026-10-25',count:28}]} config={{...defaultDashboardChart(),showValues:false}} />)
  expect(captured.value.series.type).toBe(type)
  expect(captured.value.series.label.show).toBe(false)
  expect(captured.value.rows).toEqual([{name:'2026-10-25',value:28}])
})
it.each([ReportKinds, ReportDonut])('retains full category names and the denominator for hover details', Chart => {
  render(<Chart kinds={[{key:'A full category name',count:3},{key:'B',count:1}]} />)
  expect(captured.value.total).toBe(4)
  expect(captured.value.rows[0]).toEqual({name:'A full category name',value:3})
})
it('groups the remainder in a donut without losing its counts', () => {
  render(<ReportDonut kinds={Array.from({length:6},(_,i) => ({key:String(i),count:i+1}))} config={{...defaultDashboardChart(),topN:5}} />)
  expect(captured.value.rows.map((row: any) => row.value)).toEqual([1,2,3,4,5,6])
  expect(captured.value.series.type).toBe('pie')
})
it.each(['bar','column'] as const)('shows selected metrics as %s without treating them as category shares', display => {
  render(<MetricChart display={display} metrics={[{key:'running',label:'Active runs',count:3},{key:'waiting',label:'Waiting jobs',count:7}]} />)
  expect(screen.getByRole('img').textContent).toContain('Waiting jobs: 7')
  expect(captured.value.total).toBeUndefined()
  expect(captured.value.horizontal).toBe(display === 'bar')
})
