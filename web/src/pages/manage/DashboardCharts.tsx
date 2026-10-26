import { useTranslation } from 'react-i18next'
import { Tooltip, Typography } from 'antd'
import type { ReactNode } from 'react'
import { defaultDashboardChart, type DashboardChart } from './dashboardLayout'
import { chartSeries } from './dashboardChartData'

export interface ReportOverview {
  total: number; today: number; recent: number; yesterday: number; month: number; year: number
  days: { key: string; count: number }[]
  kinds: { key: string; count: number }[]
  kind_periods?: Record<string, { key: string; count: number }[]>
}

function ChartDatum({ label, count, total, children }: { label: string; count: number; total?: number; children: ReactNode }) {
  const { t } = useTranslation()
  const detail = <div><strong>{label}</strong><div>{t('manage.dashboard.tooltipCount', { count })}</div>{total !== undefined && <div>{t('manage.dashboard.tooltipShare', { percent: total ? (count / total * 100).toFixed(1) : '0.0' })}</div>}</div>
  return <Tooltip title={detail} trigger={['hover', 'focus']}><g tabIndex={0} aria-label={`${label}: ${count}`} className="rp-chart-datum">{children}</g></Tooltip>
}

export function ReportTrend({ days: source, config = defaultDashboardChart() }: { days: ReportOverview['days']; config?: DashboardChart }) {
  const { t } = useTranslation()
  const days = chartSeries(source, config)
  const maximum = Math.max(2, Math.ceil(Math.max(0, ...days.map((item) => item.count)) / 2) * 2)
  const points = days.map((item, index) => [40 + index * (420 / Math.max(1, days.length - 1)), 160 - item.count / maximum * 128])
  return <figure className="rp-dashboard-chart">
    <svg viewBox="0 0 480 210" role="img" aria-label={t('manage.dashboard.reportTrend')}>
      <title>{days.map((item) => `${item.key}: ${item.count}`).join(', ')}</title>
      {[0, 0.5, 1].map((ratio) => <g key={ratio} className="rp-chart-grid">
        <line x1="40" x2="460" y1={160 - ratio * 128} y2={160 - ratio * 128} />
        <text x="28" y={164 - ratio * 128} textAnchor="end">{maximum * ratio}</text>
      </g>)}
      <polyline points={points.map((point) => point.join(',')).join(' ')} fill="none" stroke="var(--ant-color-primary)" strokeWidth="3" />
      {days.map((item, index) => <ChartDatum key={item.key} label={item.key} count={item.count}>
        <circle cx={points[index][0]} cy={points[index][1]} r="12" fill="transparent" />
        <circle className="rp-chart-mark" cx={points[index][0]} cy={points[index][1]} r={days.length > 40 ? 2 : 4} fill="var(--ant-color-primary)" />
        {config.showValues && days.length <= 16 && <text x={points[index][0]} y={points[index][1] - 12} textAnchor="middle" className="rp-chart-value">{item.count}</text>}
        {(index % Math.max(1, Math.ceil(days.length / 7)) === 0 || index === days.length - 1) && <text x={points[index][0]} y="190" textAnchor="middle">{item.key.slice(5)}</text>}
      </ChartDatum>)}
    </svg>
    <figcaption><Typography.Text type="secondary">{t('manage.dashboard.reportDateHint')}</Typography.Text></figcaption>
  </figure>
}

export function ReportKinds({ kinds, config = defaultDashboardChart() }: { kinds: ReportOverview['kinds']; config?: DashboardChart }) {
  const { t } = useTranslation()
  const selected = kinds.slice(0, config.topN)
  const maximum = Math.max(1, ...selected.map((item) => item.count))
  const total = kinds.reduce((sum, item) => sum + item.count, 0)
  const height = Math.max(210, selected.length * 30 + 20)
  const top = (height - selected.length * 30) / 2
  return <figure className="rp-dashboard-chart">
    <svg viewBox={`0 0 480 ${height}`} role="img" aria-label={t('manage.dashboard.reportKinds')}>
      <title>{selected.map((item) => `${item.key || t('manage.dashboard.uncategorized')}: ${item.count}`).join(', ')}</title>
      {selected.map((item, index) => {
        const label = item.key || t('manage.dashboard.uncategorized')
        const width = item.count / maximum * 260
        const y = top + index * 30
        return <ChartDatum key={item.key} label={label} count={item.count} total={total}>
          <text x="124" y={y + 15} textAnchor="end">{label.length > 10 ? `${label.slice(0, 9)}…` : label}</text>
          <rect className="rp-chart-mark" x="140" y={y} width={width} height="20" rx="4" fill="var(--ant-color-primary)" />
          {config.showValues && <text x={150 + width} y={y + 15} className="rp-chart-value">{item.count}</text>}
        </ChartDatum>
      })}
      {!selected.length && <text x="240" y="105" textAnchor="middle">{t('manage.dashboard.noReports')}</text>}
    </svg>
    <figcaption><Typography.Text type="secondary">{t('manage.dashboard.reportKindsHint')}</Typography.Text></figcaption>
  </figure>
}

export function ReportBars({ days: source, config = defaultDashboardChart() }: { days: ReportOverview['days']; config?: DashboardChart }) {
  const { t } = useTranslation()
  const days = chartSeries(source, config)
  const maximum = Math.max(1, ...days.map((item) => item.count))
  return <figure className="rp-dashboard-chart"><svg viewBox="0 0 480 210" role="img" aria-label={t('manage.dashboard.reportBars')}>
    <title>{days.map((item) => `${item.key}: ${item.count}`).join(', ')}</title>
    {days.map((item, index) => {
      const step = 420 / Math.max(1, days.length)
      const x = 40 + index * step
      const width = Math.max(1, step * 0.65)
      const height = item.count / maximum * 130
      return <ChartDatum key={item.key} label={item.key} count={item.count}>
        <rect x={x} y={160 - Math.max(12, height)} width={width} height={Math.max(12, height)} fill="transparent" />
        <rect className="rp-chart-mark" x={x} y={160-height} width={width} height={height} rx="4" fill="var(--ant-color-primary)" />
        {config.showValues && days.length <= 16 && <text x={x+width/2} y={150-height} textAnchor="middle">{item.count}</text>}
        {(index % Math.max(1, Math.ceil(days.length / 7)) === 0 || index === days.length - 1) && <text x={x+width/2} y="190" textAnchor="middle">{item.key.slice(5)}</text>}
      </ChartDatum>
    })}
  </svg><figcaption>{t('manage.dashboard.reportDateHint')}</figcaption></figure>
}

function donutSlice(start: number, size: number, center: number) {
  if (size <= 0) return ''
  if (size >= 100) return `M110 ${center-81} A81 81 0 1 1 110 ${center+81} A81 81 0 1 1 110 ${center-81} Z M110 ${center-59} A59 59 0 1 1 110 ${center+59} A59 59 0 1 1 110 ${center-59} Z`
  const point = (radius: number, percent: number) => {
    const angle = percent / 100 * Math.PI * 2 - Math.PI / 2
    return `${110 + radius * Math.cos(angle)} ${center + radius * Math.sin(angle)}`
  }
  const large = size > 50 ? 1 : 0
  return `M${point(81, start)} A81 81 0 ${large} 1 ${point(81, start+size)} L${point(59, start+size)} A59 59 0 ${large} 0 ${point(59, start)} Z`
}

export function ReportDonut({ kinds, config = defaultDashboardChart() }: { kinds: ReportOverview['kinds']; config?: DashboardChart }) {
  const { t } = useTranslation()
  const total = kinds.reduce((sum, item) => sum + item.count, 0)
  const colors = ['var(--ant-color-primary)', 'var(--ant-color-success)', 'var(--ant-color-warning)', 'var(--ant-color-info)', 'var(--ant-color-error)', 'var(--ant-color-text-tertiary)']
  const slices = [...kinds.slice(0, config.topN), ...(kinds.length > config.topN ? [{ key: t('manage.dashboard.otherKinds'), count: kinds.slice(config.topN).reduce((sum, item) => sum + item.count, 0) }] : [])]
  const height = Math.max(210, slices.length * 28 + 20)
  const center = height / 2
  const legendTop = (height - slices.length * 28) / 2
  let offset = 0
  return <figure className="rp-dashboard-chart"><svg viewBox={`0 0 480 ${height}`} role="img" aria-label={t('manage.dashboard.reportDonut')}>
    <title>{slices.map((item) => `${item.key || t('manage.dashboard.uncategorized')}: ${item.count}`).join(', ')}</title>
    <circle cx="110" cy={center} r="70" fill="none" stroke="var(--ant-color-fill-secondary)" strokeWidth="22" />
    {slices.map((item, index) => {
      const size = total ? item.count / total * 100 : 0
      const start = offset
      offset += size
      const label = item.key || t('manage.dashboard.uncategorized')
      return <ChartDatum key={item.key} label={label} count={item.count} total={total}>
        <path className="rp-chart-mark" d={donutSlice(start, size, center)} fillRule="evenodd" fill={colors[index % colors.length]} />
        <rect x="220" y={legendTop+index*28} width="9" height="9" rx="2" fill={colors[index % colors.length]} />
        <text x="239" y={legendTop+9+index*28}>{label.length > 14 ? `${label.slice(0,13)}…` : label}</text>
        {config.showValues && <text x="465" y={legendTop+9+index*28} textAnchor="end">{item.count}</text>}
      </ChartDatum>
    })}
    <text x="110" y={center+5} textAnchor="middle" className="rp-chart-value">{total}</text>
    {!total && <text x="239" y={center}>{t('manage.dashboard.noReports')}</text>}
  </svg><figcaption><Typography.Text type="secondary">{t('manage.dashboard.reportKindsHint')}</Typography.Text></figcaption></figure>
}

export function MetricChart({ metrics, display }: { metrics: { key: string; label: string; count: number }[]; display: 'bar' | 'column' }) {
  const { t } = useTranslation()
  const maximum = Math.max(1, ...metrics.map(item => item.count))
  const horizontal = display === 'bar'
  const height = Math.max(210, horizontal ? metrics.length * 30 + 20 : 210)
  const top = (height - metrics.length * 30) / 2
  return <figure className="rp-dashboard-chart"><svg viewBox={`0 0 480 ${height}`} role="img" aria-label={t('manage.dashboard.metricComparison')}>
    <title>{metrics.map(item => `${item.label}: ${item.count}`).join(', ')}</title>
    {metrics.map((item, index) => {
      const step = 420 / Math.max(1, metrics.length)
      const x = horizontal ? 170 : 30 + step * index
      const y = horizontal ? top + index * 30 : 160 - item.count / maximum * 128
      const width = horizontal ? item.count / maximum * 245 : Math.min(60, step * 0.65)
      const markHeight = horizontal ? 20 : item.count / maximum * 128
      return <ChartDatum key={item.key} label={item.label} count={item.count}>
        <rect x={x} y={horizontal ? y : Math.min(y,148)} width={Math.max(12,width)} height={Math.max(12,markHeight)} fill="transparent" />
        <rect className="rp-chart-mark" x={x} y={y} width={width} height={markHeight} rx="4" fill="var(--ant-color-primary)" />
        <text x={horizontal ? 156 : x+width/2} y={horizontal ? y+15 : 188} textAnchor={horizontal ? 'end' : 'middle'}>{item.label.length > (horizontal ? 16 : 10) ? `${item.label.slice(0,horizontal ? 15 : 9)}…` : item.label}</text>
        <text className="rp-chart-value" x={horizontal ? x+width+8 : x+width/2} y={horizontal ? y+15 : y-10} textAnchor={horizontal ? 'start' : 'middle'}>{item.count}</text>
      </ChartDatum>
    })}
  </svg></figure>
}
