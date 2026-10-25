import { fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { ReportBars, ReportDonut, ReportKinds, ReportTrend } from './DashboardCharts'
import { defaultDashboardChart } from './dashboardLayout'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string, values?: Record<string, unknown>) => `${key}${values ? ` ${JSON.stringify(values)}` : ''}` }) }))

it.each([ReportTrend, ReportBars])('shows complete dates and counts on hover even when values are hidden', async Chart => {
  const { container } = render(<Chart days={[{key:'2026-10-25',count:28}]} config={{...defaultDashboardChart(), showValues:false}} />)
  fireEvent.mouseOver(container.querySelector('.rp-chart-datum')!)
  const tooltip = await screen.findByRole('tooltip')
  expect(tooltip.textContent).toContain('2026-10-25')
  expect(tooltip.textContent).toContain('"count":28')
})

it.each([ReportKinds, ReportDonut])('shows full category names, counts and shares on keyboard focus', async Chart => {
  const { container } = render(<Chart kinds={[{key:'A long category name that is shortened in the chart',count:3},{key:'B',count:1}]} />)
  fireEvent.focus(container.querySelector('.rp-chart-datum')!)
  const tooltip = await screen.findByRole('tooltip')
  expect(tooltip.textContent).toContain('A long category name that is shortened in the chart')
  expect(tooltip.textContent).toContain('"count":3')
  expect(tooltip.textContent).toContain('"percent":"75.0"')
})

it('centers category bars and donut legends within their plot', () => {
  const { container, rerender } = render(<ReportKinds kinds={[{key:'A',count:3}]} />)
  const bar = container.querySelector('rect.rp-chart-mark')!
  expect(Number(bar.getAttribute('y'))).toBeGreaterThan(80)
  rerender(<ReportDonut kinds={[{key:'A',count:3}]} />)
  expect(Number(container.querySelector('.rp-chart-datum rect')!.getAttribute('y'))).toBeGreaterThan(80)
})
