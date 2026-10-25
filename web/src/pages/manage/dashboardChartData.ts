import type { DashboardChart } from './dashboardLayout'
export interface ChartCount { key: string; count: number }
export function chartInterval(chart: DashboardChart) {
  return chart.interval === 'auto' ? chart.range > 90 ? 'month' : chart.range > 30 ? 'week' : 'day' : chart.interval
}
export function chartSeries(days: ChartCount[], chart: DashboardChart): ChartCount[] {
  const interval = chartInterval(chart)
  const groups = new Map<string, number>()
  for (const item of days.slice(-chart.range)) {
    let key = item.key
    if (interval === 'month') key = key.slice(0, 7)
    if (interval === 'week') {
      const date = new Date(`${key}T00:00:00Z`)
      date.setUTCDate(date.getUTCDate() - (date.getUTCDay() + 6) % 7)
      key = date.toISOString().slice(0, 10)
    }
    groups.set(key, (groups.get(key) ?? 0) + item.count)
  }
  let total = 0
  return [...groups].map(([key, count]) => { total += count; return { key, count: chart.cumulative ? total : count } })
}
