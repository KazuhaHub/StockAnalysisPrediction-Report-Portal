import { useEffect, useState } from 'react'
import { Alert, Button, DatePicker, Input, Segmented, Select, Space, Spin, Statistic, Table, Typography } from 'antd'
import { DownloadOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import DashboardChart from './DashboardChart'
import DashboardChartCard from './DashboardChartCard'
import { api } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import { settleOnAnswer } from '../../lib/loadFailure'

type Dataset = 'reports' | 'runs' | 'accounts'
interface Result {
  items: { label: string; count: number }[]
  total: number
  records: number
  other?: number
  too_many_points?: boolean
  today?: string
}
const dimensions: Record<Dataset, string[]> = {
  reports: ['kind', 'subtype', 'symbol', 'date'],
  runs: ['status', 'target', 'submitter'],
  accounts: ['role', 'organization', 'source', 'active'],
}

export default function StatisticsPage() {
  const { t } = useTranslation()
  const { failure, retryKey } = useLoadFailure()
  const [dataset, setDataset] = useState<Dataset>('reports')
  const [group, setGroup] = useState('kind')
  const [interval, setInterval] = useState('month')
  const [view, setView] = useState('chart')
  const [chartType, setChartType] = useState('auto')
  const [range, setRange] = useState<[string, string] | null>(null)
  const [search, setSearch] = useState('')
  const [applied, setApplied] = useState('dataset=reports&group=kind')
  const [page, setPage] = useState(1)
  const [size, setSize] = useState(25)
  const [result, setResult] = useState<Result>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    let live = true
    setBusy(true)
    setError('')
    setResult(undefined)
    api.get<Result>(`/api/admin/statistics?${applied}&page=${page}&page_size=${size}${view === 'chart' ? '&view=chart' : ''}`)
      .then(value => { if (live) setResult(value) })
      .catch(err => { const text = failure(err); if (live && text !== null) setError(text) })
      .then(...settleOnAnswer(() => { if (live) setBusy(false) }))
    return () => { live = false }
  }, [applied, page, size, revision, view, t, retryKey])

  // Draft filters do not alter the result or export until the user applies them.
  const apply = () => {
    const params = new URLSearchParams({ dataset, group })
    if (range) { params.set('from', range[0]); params.set('to', range[1]) }
    if (search.trim()) params.set('q', search.trim())
    if (group === 'date') params.set('interval', interval)
    setPage(1)
    setApplied(params.toString())
    setRevision(n => n + 1)
  }

  const appliedFilters = new URLSearchParams(applied)
  const dateGroups = appliedFilters.get('group') === 'date'
  const chartTitle = `${t(`statistics.${appliedFilters.get('dataset')}`)} · ${t(`statistics.dimension.${appliedFilters.get('group')}`)}`
  const type = chartType === 'auto' ? dateGroups ? 'line' : 'bar' : chartType
  const pie = type === 'donut'
  const rows = (result?.items ?? []).map(row => ({name: row.label || t('statistics.unassigned'), value: row.count}))
  if ((result?.other ?? 0) > 0) rows.push({name:t('manage.dashboard.otherKinds'),value:result!.other!})

  return <div className="rp-statistics-page">
    <div className="rp-statistics-filters">
      <label>{t('statistics.dataset')}
        <Select value={dataset} aria-label={t('statistics.dataset')}
          onChange={(value: Dataset) => { setDataset(value); setGroup(dimensions[value][0]) }}
          options={Object.keys(dimensions).map(value => ({ value, label: t(`statistics.${value}`) }))} />
      </label>
      <label>{t('statistics.dimension')}
        <Select value={group} aria-label={t('statistics.dimension')} onChange={setGroup}
          options={dimensions[dataset].map(value => ({ value, label: t(`statistics.dimension.${value}`) }))} />
      </label>
      {group === 'date' && <label>{t('statistics.interval')}
        <Select aria-label={t('statistics.interval')} value={interval} onChange={setInterval}
          options={['day','month','year'].map(value=>({value,label:t(`statistics.interval.${value}`)}))}/>
      </label>}
      <label>{t(dataset === 'reports' ? 'statistics.reportDate' : 'statistics.createdDate')}
        <DatePicker.RangePicker allowClear value={range ? [dayjs(range[0]), dayjs(range[1])] : null}
          onChange={value => setRange(value?.[0] && value[1] ? [value[0].format('YYYY-MM-DD'), value[1].format('YYYY-MM-DD')] : null)}
          presets={[7, 30, 90, 365].map(days => ({
            label: t('statistics.lastDays', { count: days }),
            value: [dayjs(result?.today).subtract(days - 1, 'day'), dayjs(result?.today)],
          }))} />
      </label>
      <label>{t('statistics.search')}
        <Input.Search value={search} maxLength={200} aria-label={t('statistics.search')}
          onChange={e => setSearch(e.target.value)} onSearch={apply} allowClear />
      </label>
      <Button type="primary" onClick={apply}>{t('statistics.apply')}</Button>
    </div>
    <Typography.Paragraph type="secondary">{t('statistics.scope')}</Typography.Paragraph>
    {error && <Alert type="error" showIcon title={error} />}
    <div className="rp-admin-list-toolbar">
      <Space size="large">
        <Statistic title={t('statistics.records')} value={result?.records ?? '—'} />
        <Statistic title={t('statistics.groups')} value={result?.total ?? '—'} />
      </Space>
      <Button download="statistics.csv" href={`/api/admin/statistics?${applied}&format=csv`}
        disabled={busy || !result?.total} icon={<DownloadOutlined />}>{t('statistics.download')}</Button>
    </div>
    <div className="rp-admin-list-toolbar">
      <Segmented aria-label={t('statistics.view')} value={view} onChange={setView} options={[
        {value:'chart',label:t('statistics.chart')},{value:'table',label:t('statistics.table')},
      ]}/>
      {view === 'chart' && <Select aria-label={t('statistics.chartType')} value={chartType} onChange={setChartType}
        options={['auto','bar','column','line','donut'].map(value=>({value,label:t(`statistics.chartType.${value}`)}))}/>}
    </div>
    {error ? null : view === 'chart' ? <Spin spinning={busy}>
      {result?.too_many_points ? <Alert type="info" showIcon title={t('statistics.tooManyPoints')}/> :
        <DashboardChartCard title={chartTitle}>
          {!rows.length ? <Typography.Paragraph type="secondary">{t(busy ? 'common.loading' : 'statistics.noData')}</Typography.Paragraph> : <DashboardChart label={chartTitle} rows={rows} total={result?.records} horizontal={type === 'bar'}
            legendValues={pie} hint={dateGroups ? t('statistics.dateChartHint') : t('statistics.categoryChartHint')}
            series={pie ? {type:'pie',radius:['45%','68%'],label:{show:false}} :
              {type:type === 'line' ? 'line' : 'bar',barMaxWidth:36,label:{show:rows.length<=20,position:type==='bar'?'right':'top'}}}/>}
        </DashboardChartCard>}
    </Spin> : <Table size="middle" rowKey="label" loading={busy} dataSource={result?.items ?? []} tableLayout="fixed"
      columns={[
        { title: t('statistics.value'), dataIndex: 'label', render: (label: string) => label || t('statistics.unassigned') },
        { title: t('statistics.count'), dataIndex: 'count', width: '25%', align: 'right', render: (count: number) => count.toLocaleString() },
      ]}
      pagination={{
        current: page, pageSize: size, total: result?.total ?? 0, showSizeChanger: true, pageSizeOptions: [25, 50, 100],
        onChange: (next, n) => { setPage(n === size ? next : 1); setSize(n) },
      }} />}
    <Typography.Paragraph type="secondary">{t('statistics.exportHint')}</Typography.Paragraph>
  </div>
}
