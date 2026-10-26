import { useTranslation } from 'react-i18next'
import DashboardChart, { type ChartRow } from './DashboardChart'
import { defaultDashboardChart, type DashboardChart as ChartConfig } from './dashboardLayout'
import { chartSeries } from './dashboardChartData'

export interface ReportOverview {
  total: number; today: number; recent: number; yesterday: number; month: number; year: number
  days: { key: string; count: number }[]
  kinds: { key: string; count: number }[]
  kind_periods?: Record<string, { key: string; count: number }[]>
}

function ReportTimeChart({ days, config, type }: { days: ReportOverview['days']; config: ChartConfig; type: 'line' | 'bar' }) {
  const { t } = useTranslation()
  const rows = chartSeries(days, config).map(item => ({ name: item.key, value: item.count }))
  return <DashboardChart label={t(type === 'line' ? 'manage.dashboard.reportTrend' : 'manage.dashboard.reportBars')}
    rows={rows} hint={t('manage.dashboard.reportDateHint')} dates
    series={{ type, label: { show: config.showValues && rows.length <= 16, position: 'top' }, ...(type === 'line' ? { symbolSize: 6 } : { barMaxWidth: 42 }) }} />
}
export function ReportTrend({ days, config = defaultDashboardChart() }: { days: ReportOverview['days']; config?: ChartConfig }) {
  return <ReportTimeChart days={days} config={config} type="line" />
}
export function ReportBars({ days, config = defaultDashboardChart() }: { days: ReportOverview['days']; config?: ChartConfig }) {
  return <ReportTimeChart days={days} config={config} type="bar" />
}
export function ReportKinds({ kinds, config = defaultDashboardChart() }: { kinds: ReportOverview['kinds']; config?: ChartConfig }) {
  const { t } = useTranslation()
  const rows = kinds.slice(0, config.topN).map(item => ({ name: item.key || t('manage.dashboard.uncategorized'), value: item.count }))
  return <DashboardChart label={t('manage.dashboard.reportKinds')} rows={rows} horizontal
    total={kinds.reduce((sum, item) => sum + item.count, 0)} hint={t('manage.dashboard.reportKindsHint')}
    series={{ type: 'bar', barMaxWidth: 28, label: { show: config.showValues, position: 'right' } }} />
}
export function ReportDonut({ kinds, config = defaultDashboardChart() }: { kinds: ReportOverview['kinds']; config?: ChartConfig }) {
  const { t } = useTranslation()
  const rows: ChartRow[] = kinds.slice(0, config.topN).map(item => ({ name: item.key || t('manage.dashboard.uncategorized'), value: item.count }))
  if (kinds.length > config.topN) rows.push({ name: t('manage.dashboard.otherKinds'), value: kinds.slice(config.topN).reduce((sum,item) => sum + item.count,0) })
  return <DashboardChart label={t('manage.dashboard.reportDonut')} rows={rows} legendValues={config.showValues}
    total={kinds.reduce((sum,item) => sum + item.count,0)} hint={t('manage.dashboard.reportKindsHint')}
    series={{ type: 'pie', radius: ['48%', '70%'], label: { show: false }, labelLine: { show: false } }} />
}
export function MetricChart({ metrics, display }: { metrics: { key: string; label: string; count: number }[]; display: 'bar' | 'column' }) {
  const { t } = useTranslation()
  return <DashboardChart label={t('manage.dashboard.metricComparison')} rows={metrics.map(item => ({ name: item.label, value: item.count }))}
    horizontal={display === 'bar'} series={{ type: 'bar', barMaxWidth: 42, label: { show: true, position: display === 'bar' ? 'right' : 'top' } }} />
}
