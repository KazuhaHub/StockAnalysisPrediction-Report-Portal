import { useTranslation } from 'react-i18next'
import { Typography } from 'antd'
import { defaultDashboardChart, type DashboardChart } from './dashboardLayout'
import { chartSeries } from './dashboardChartData'

export interface ReportOverview {
  total: number; today: number; recent: number; yesterday: number; month: number; year: number
  days: { key: string; count: number }[]
  kinds: { key: string; count: number }[]
  kind_periods?: Record<string, { key: string; count: number }[]>
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
      {days.map((item, index) => <g key={item.key}>
        <title>{item.key}: {item.count}</title>
        <circle cx={points[index][0]} cy={points[index][1]} r={days.length > 40 ? 2 : 4} fill="var(--ant-color-primary)" />
        {config.showValues && days.length <= 16 && <text x={points[index][0]} y={points[index][1] - 12} textAnchor="middle" className="rp-chart-value">{item.count}</text>}
        {(index % Math.max(1, Math.ceil(days.length / 7)) === 0 || index === days.length - 1) && <text x={points[index][0]} y="190" textAnchor="middle">{item.key.slice(5)}</text>}
      </g>)}
    </svg>
    <figcaption><Typography.Text type="secondary">{t('manage.dashboard.reportDateHint')}</Typography.Text></figcaption>
  </figure>
}

export function ReportKinds({ kinds, config = defaultDashboardChart() }: { kinds: ReportOverview['kinds']; config?: DashboardChart }) {
  const { t } = useTranslation()
  const selected = kinds.slice(0, config.topN)
  const maximum = Math.max(1, ...selected.map((item) => item.count))
  return <figure className="rp-dashboard-chart">
    <svg viewBox={`0 0 480 ${Math.max(210, selected.length * 30 + 20)}`} role="img" aria-label={t('manage.dashboard.reportKinds')}>
      <title>{selected.map((item) => `${item.key || t('manage.dashboard.uncategorized')}: ${item.count}`).join(', ')}</title>
      {selected.map((item, index) => {
        const label = item.key || t('manage.dashboard.uncategorized')
        const width = item.count / maximum * 260
        return <g key={item.key}>
          <title>{label}: {item.count}</title>
          <text x="124" y={30 + index * 30} textAnchor="end">{label.length > 10 ? `${label.slice(0, 9)}…` : label}</text>
          <rect x="140" y={16 + index * 30} width={width} height="20" rx="4" fill="var(--ant-color-primary)" />
          {config.showValues && <text x={150 + width} y={31 + index * 30} className="rp-chart-value">{item.count}</text>}
        </g>
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
    {days.map((item, index) => { const step = 420 / Math.max(1, days.length); const x = 40 + index * step; const width = Math.max(1, step * 0.65); const height = item.count / maximum * 130
      return <g key={item.key}><rect x={x} y={160-height} width={width} height={height} rx="4" fill="var(--ant-color-primary)" />
        <title>{item.key}: {item.count}</title>
        {config.showValues && days.length <= 16 && <text x={x+width/2} y={150-height} textAnchor="middle">{item.count}</text>}
        {(index % Math.max(1, Math.ceil(days.length / 7)) === 0 || index === days.length - 1) && <text x={x+width/2} y="190" textAnchor="middle">{item.key.slice(5)}</text>}</g>
    })}
  </svg><figcaption>{t('manage.dashboard.reportDateHint')}</figcaption></figure>
}

export function ReportDonut({ kinds, config = defaultDashboardChart() }: { kinds: ReportOverview['kinds']; config?: DashboardChart }) {
  const { t } = useTranslation()
  const total = kinds.reduce((sum, item) => sum + item.count, 0)
  const colors = ['var(--ant-color-primary)', 'var(--ant-color-success)', 'var(--ant-color-warning)', 'var(--ant-color-info)', 'var(--ant-color-error)', 'var(--ant-color-text-tertiary)']
  const slices = [...kinds.slice(0, config.topN), ...(kinds.length > config.topN ? [{ key: t('manage.dashboard.otherKinds'), count: kinds.slice(config.topN).reduce((sum, item) => sum + item.count, 0) }] : [])]
  let offset = 0
  return <figure className="rp-dashboard-chart"><svg viewBox={`0 0 480 ${Math.max(210, slices.length * 28 + 20)}`} role="img" aria-label={t('manage.dashboard.reportDonut')}>
    <title>{slices.map((item) => `${item.key || t('manage.dashboard.uncategorized')}: ${item.count}`).join(', ')}</title>
    <circle cx="110" cy="105" r="70" fill="none" stroke="var(--ant-color-fill-secondary)" strokeWidth="22" />
    {slices.map((item, index) => { const size = total ? item.count / total * 100 : 0; const start = offset; offset += size
      return <g key={item.key}><circle cx="110" cy="105" r="70" fill="none" stroke={colors[index % colors.length]} strokeWidth="22" pathLength="100" strokeDasharray={`${size} ${100-size}`} strokeDashoffset={-start} transform="rotate(-90 110 105)" />
        <rect x="220" y={25+index*28} width="9" height="9" rx="2" fill={colors[index % colors.length]} /><text x="239" y={34+index*28}>{(item.key || t('manage.dashboard.uncategorized')).slice(0,14)}</text>{config.showValues && <text x="465" y={34+index*28} textAnchor="end">{item.count}</text>}</g>
    })}
    <text x="110" y="110" textAnchor="middle" className="rp-chart-value">{total}</text>
    {!total && <text x="239" y="105">{t('manage.dashboard.noReports')}</text>}
  </svg></figure>
}
