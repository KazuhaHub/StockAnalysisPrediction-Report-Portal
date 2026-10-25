import { ADMIN_GROUPS, adminPage } from './adminNavigation'
import { useTranslation } from 'react-i18next'
import { ReportBars, ReportDonut, ReportTrend } from './DashboardCharts'

/** Static illustrative values belong only to the component catalog, never to live cards. */
export default function DashboardWidgetPreview({ id }: { id: string }) {
  const { t } = useTranslation()
  const days = [4, 7, 5, 11, 8, 14, 9].map((count, index) => ({ key: `2026-01-${String(index+1).padStart(2, '0')}`, count }))
  const metricLabels = id === 'queue' ? ['running', 'waiting', 'scheduled'] : id === 'forecast' ? ['due', 'pending'] : ['totalReports', 'todayReports', 'recentReports']
  const kinds = [{ key: t('manage.dashboard.exampleCategoryA'), count: 28 }, { key: t('manage.dashboard.exampleCategoryB'), count: 17 }, { key: t('manage.dashboard.exampleCategoryC'), count: 9 }]
  return <div className="rp-dashboard-example" aria-label={t('manage.dashboard.example')}>
    <span className="rp-dashboard-example-label">{t('manage.dashboard.example')}</span>
    <div aria-hidden="true">
      {id === 'trend' ? <ReportTrend days={days} /> : id === 'kinds' ? <ReportDonut kinds={kinds} /> : id === 'report-bars' ? <ReportBars days={days} /> : id === 'report-donut' ? <ReportDonut kinds={kinds} /> :
        ['reports','queue','forecast'].includes(id) ? <div className="rp-dashboard-example-metrics">{metricLabels.map((label,index) => <div key={label}><span>{t(`manage.dashboard.${label}`)}</span><strong>{[128,24,56][index]}</strong></div>)}</div> :
          id === 'shortcuts' ? <div className="rp-dashboard-example-shortcuts">{['types','versions','forecast'].map((key) => {
            const page = adminPage(key)!
            return <span key={key}><i>{page.icon}</i><span>{t(page.label)}</span></span>
          })}</div> : <div className="rp-dashboard-example-directory">{ADMIN_GROUPS.slice(0,2).map((group) => <div key={group.key}><strong>{t(group.label)}</strong>{group.pages.slice(0,3).map((page) => <span key={page.key}>{page.icon}{t(page.label)}<b>›</b></span>)}</div>)}</div>}
    </div>
  </div>
}
