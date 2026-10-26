import { useEffect, useState } from 'react'
import { Alert, Button, DatePicker, Input, Select, Space, Statistic, Table, Typography } from 'antd'
import { DownloadOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'

type Dataset = 'reports' | 'runs' | 'accounts'
interface Result {
  items: { label: string; count: number }[]
  total: number
  records: number
  today?: string
}
const dimensions: Record<Dataset, string[]> = {
  reports: ['kind', 'subtype', 'symbol', 'date'],
  runs: ['status', 'target', 'submitter'],
  accounts: ['role', 'organization', 'source', 'active'],
}

export default function StatisticsPage() {
  const { t } = useTranslation()
  const [dataset, setDataset] = useState<Dataset>('reports')
  const [group, setGroup] = useState('kind')
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
    api.get<Result>(`/api/admin/statistics?${applied}&page=${page}&page_size=${size}`)
      .then(value => { if (live) setResult(value) })
      .catch(err => { if (live) setError(errText(err, t)) })
      .finally(() => { if (live) setBusy(false) })
    return () => { live = false }
  }, [applied, page, size, revision, t])

  // Draft filters do not alter the result or export until the user applies them.
  const apply = () => {
    const params = new URLSearchParams({ dataset, group })
    if (range) { params.set('from', range[0]); params.set('to', range[1]) }
    if (search.trim()) params.set('q', search.trim())
    setPage(1)
    setApplied(params.toString())
    setRevision(n => n + 1)
  }

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
    <Table size="middle" rowKey="label" loading={busy} dataSource={result?.items ?? []} tableLayout="fixed"
      columns={[
        { title: t('statistics.value'), dataIndex: 'label', render: (label: string) => label || t('statistics.unassigned') },
        { title: t('statistics.count'), dataIndex: 'count', width: '25%', align: 'right', render: (count: number) => count.toLocaleString() },
      ]}
      pagination={{
        current: page, pageSize: size, total: result?.total ?? 0, showSizeChanger: true, pageSizeOptions: [25, 50, 100],
        onChange: (next, n) => { setPage(n === size ? next : 1); setSize(n) },
      }} />
    <Typography.Paragraph type="secondary">{t('statistics.exportHint')}</Typography.Paragraph>
  </div>
}
