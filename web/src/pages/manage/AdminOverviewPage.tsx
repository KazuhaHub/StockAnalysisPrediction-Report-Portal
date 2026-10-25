import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Card, Collapse, Space, Statistic, Tag, Typography } from 'antd'
import { ArrowRightOutlined, ControlOutlined, FileTextOutlined, FundProjectionScreenOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api, errText } from '../../api/client'
import type { BatchQueueSummary, ForecastSettings } from '../../api/types'
import { evaluatorStatus, EVALUATOR_TAG_COLOR } from '../../lib/forecastEvaluator'
import { ReportKinds, ReportTrend, ReportBars, ReportDonut, type ReportOverview } from './DashboardCharts'
import { DASHBOARD_METRICS, DEFAULT_METRICS } from './dashboardMetrics'
import DashboardLayoutEditor from './DashboardLayoutEditor'
import { dashboardChartType, defaultDashboardChart, isDashboardChart, type DashboardBlock, type DashboardLayout } from './dashboardLayout'
import { ADMIN_GROUPS, adminPage } from './adminNavigation'

function useSnapshot<T>(url: string, revision: number, enabled = true) {
  const { t } = useTranslation()
  const [snapshot, setSnapshot] = useState<{ data?: T; error?: string; loading: boolean; at?: string }>({ loading: true })
  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    setSnapshot((previous) => ({ ...previous, loading: true }))
    api.get<T>(url).then((data) => {
      if (!cancelled) setSnapshot({ data, loading: false, at: new Date().toISOString() })
    }).catch((error) => {
      if (!cancelled) setSnapshot((previous) => ({ ...previous, loading: false, error: errText(error, t) }))
    })
    return () => { cancelled = true }
  }, [url, revision, enabled]) // eslint-disable-line react-hooks/exhaustive-deps
  return snapshot
}

export default function AdminOverviewPage() {
  const { t } = useTranslation()
  const [revision, refresh] = useState(0)
  const [layout, setLayout] = useState<DashboardLayout | null>(null)
  const [layoutError, setLayoutError] = useState('')
  const [previewBlocks, setPreviewBlocks] = useState<DashboardBlock[] | null>(null)
  useEffect(() => {
    let cancelled = false
    api.get<DashboardLayout>('/api/admin/dashboard/layout').then((next) => {
      if (!cancelled) { setLayout(next); setLayoutError('') }
    }).catch((error) => { if (!cancelled) setLayoutError(errText(error, t)) })
    return () => { cancelled = true }
  }, [revision]) // eslint-disable-line react-hooks/exhaustive-deps
  const blocks = previewBlocks ?? layout?.blocks ?? []
  const selected = (ids: string[]) => !!layout && blocks.some((block) => block.visible && ids.includes(block.id))
  const queue = useSnapshot<BatchQueueSummary>('/api/admin/batch/queue', revision, selected(['queue']))
  const reports = useSnapshot<ReportOverview>('/api/admin/reports/overview', revision, selected(['reports', 'trend', 'kinds', 'report-bars', 'report-donut']))
  const forecast = useSnapshot<ForecastSettings>('/api/admin/forecast', revision, selected(['forecast']))
  const [now, setNow] = useState(Date.now)
  // Refresh foreground snapshots without starting an evaluator or vendor request.
  useEffect(() => {
    const update = () => {
      if (document.visibilityState === 'hidden') return
      setNow(Date.now())
      refresh((value) => value + 1)
    }
    const timer = setInterval(update, 60_000)
    document.addEventListener('visibilitychange', update)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', update)
    }
  }, [])
  const status = forecast.data ? evaluatorStatus(forecast.data, now) : undefined
  const snapshotCard = (label: string, snapshot: { data?: unknown; error?: string; loading: boolean; at?: string }, children: ReactNode, path: string, icon: ReactNode, badge?: ReactNode) => (
    <section aria-label={t(label)}>
      <Card size="small" title={<Space><span className="rp-dashboard-card-icon">{icon}</span>{t(label)}</Space>} loading={snapshot.loading && !snapshot.data}
        extra={<Space>{badge}<Link aria-label={t('manage.dashboard.open', { name: t(label) })} to={path}><ArrowRightOutlined /></Link></Space>}>
        {snapshot.error && <Alert type="error" showIcon title={snapshot.error} />}
        {snapshot.data !== undefined && children}
        {snapshot.at && <Typography.Text type="secondary" className="rp-dashboard-updated">
          {t('manage.dashboard.updated', { time: dayjs(snapshot.at).format('HH:mm:ss') })}
        </Typography.Text>}
      </Card>
    </section>
  )
  const renderMetrics = (block: DashboardBlock) => {
    const snapshot = block.id === 'reports' ? reports : block.id === 'queue' ? queue : forecast
    const data = snapshot.data as unknown as Record<string, number> | undefined
    const keys = block.metrics ?? DEFAULT_METRICS[block.id]
    const metrics = <div className="rp-dashboard-metrics">{keys.map((key) => {
      const definition = DASHBOARD_METRICS[block.id].find((item) => item.key === key)
      return definition && <Statistic key={key} title={t(`manage.dashboard.${definition.label}`)} value={data?.[key] ?? '—'} />
    })}</div>
    return block.id === 'reports' ? snapshotCard('manage.dashboard.reports', reports, metrics, '/', <FileTextOutlined />) :
      block.id === 'queue' ? snapshotCard('queue.title', queue, metrics, '/queue', <ControlOutlined />) :
        snapshotCard('nav.forecast', forecast, metrics, '/manage/forecast', <FundProjectionScreenOutlined />, status && <Tag color={EVALUATOR_TAG_COLOR[status]}>{t(`forecast.eval.status.${status}`)}</Tag>)
  }
  const content: Record<string, ReactNode> = {
    shortcuts: <section aria-label={t('manage.dashboard.shortcuts')}>
      <Card size="small" title={t('manage.dashboard.shortcuts')}>
        <div className="rp-dashboard-shortcuts">{['types', 'versions', 'forecast', 'batch', 'runqueue', 'assistant', 'quotes', 'storage'].map((key) => {
          const page = ADMIN_GROUPS.flatMap((group) => group.pages).find((item) => item.key === key)!
          return <Link key={key} to={`/manage/${key}`} className="rp-dashboard-shortcut">
            <span className="rp-dashboard-shortcut-icon" aria-hidden="true">{page.icon}</span>
            <span>{t(page.label)}</span>
          </Link>
        })}</div>
      </Card>
    </section>,
    directory: <Collapse items={[{ key: 'settings', label: t('manage.dashboard.directory'), children: <div className="rp-admin-overview">
      {ADMIN_GROUPS.map((group) => <Card key={group.key} size="small" title={t(group.label)}>
        <div className="rp-admin-overview__pages">{group.pages.map((page) => <Link key={page.key} to={`/manage/${page.key}`} className="rp-admin-overview__link">
          <span className="rp-admin-overview__icon">{page.icon}</span><span>{t(page.label)}</span><ArrowRightOutlined />
        </Link>)}</div>
      </Card>)}
    </div> }]} />,
  }
  return <div className="rp-dashboard">
    <DashboardLayoutEditor layout={layout} error={layoutError} onSaved={setLayout} onPreview={setPreviewBlocks}
      renderBlock={(block) => {
        if (DASHBOARD_METRICS[block.id]) return renderMetrics(block)
        if (isDashboardChart(block.id)) {
          const config = block.chart ?? defaultDashboardChart()
          const kinds = reports.data?.kind_periods?.[String(config.range)] ?? reports.data?.kinds ?? []
          const type = dashboardChartType(block.id, config)
          const title = block.id === 'trend' || block.id === 'report-bars' ? 'reportTrend' : 'reportKinds'
          return <Card className="rp-dashboard-chart-card" size="small" title={t(`manage.dashboard.${title}`)} extra={<Tag>{t(`manage.dashboard.range.${config.range}`)}</Tag>} loading={reports.loading && !reports.data}>
            {reports.data ? type === 'line' ? <ReportTrend days={reports.data.days} config={config} /> : type === 'column' ? <ReportBars days={reports.data.days} config={config} /> : type === 'bar' ? <ReportKinds kinds={kinds} config={config} /> : <ReportDonut kinds={kinds} config={config} /> : reports.error && <Alert type="error" title={reports.error} />}
          </Card>
        }
        if (block.id.startsWith('shortcut:')) {
          const page = adminPage(block.id.slice(9))
          return page ? <Card className="rp-dashboard-shortcut-card"><Link to={`/manage/${page.key}`} className="rp-dashboard-shortcut rp-dashboard-shortcut-tile">
            <span className="rp-dashboard-shortcut-icon" aria-hidden="true">{page.icon}</span><span>{t(page.label)}</span>
          </Link></Card> : null
        }
        return content[block.id]
      }} />
  </div>
}
