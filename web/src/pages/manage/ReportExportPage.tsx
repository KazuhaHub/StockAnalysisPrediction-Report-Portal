import { useCallback, useEffect, useState } from 'react'
import { Alert, App, Button, Card, DatePicker, Input, Select, Space, Table, Tag, Typography } from 'antd'
import { DownloadOutlined } from '@ant-design/icons'
import type { Dayjs } from 'dayjs'
import { useTranslation } from 'react-i18next'
import { api, errText, qs } from '../../api/client'
import type { AdminExportReport, AdminExportReportPage } from '../../api/types'
import { downloadReportMarkdown, type ReportExportFilters } from '../../lib/reportExport'

// Bulk Markdown export: hands report bodies to an offline tool or an archive. Kept apart from the
// retention console on purpose — exporting is read-only and never changes or removes a report.

const PAGE_SIZE = 20
const EMPTY: ReportExportFilters = { q: '', rtype: '', generator_version: '', date_from: '', date_to: '' }

export default function ReportExportPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [filters, setFilters] = useState<ReportExportFilters>(EMPTY)
  const [q, setQ] = useState('')
  const [page, setPage] = useState(1)
  const [data, setData] = useState<AdminExportReportPage | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>()
  const [selected, setSelected] = useState<number[]>([])
  const [busy, setBusy] = useState<'selected' | 'all' | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setData(await api.get<AdminExportReportPage>('/api/admin/report-exports' + qs({ ...filters, page, page_size: PAGE_SIZE })))
      setError(undefined)
    } catch (e) {
      setError(errText(e, t))
    } finally {
      setLoading(false)
    }
  }, [filters, page, t])

  useEffect(() => {
    void load()
  }, [load])

  // A filter change redefines the result set, so a selection made under the old one is dropped
  // rather than silently carried into rows the admin can no longer see.
  const patch = (p: Partial<ReportExportFilters>) => {
    setFilters((f) => ({ ...f, ...p }))
    setPage(1)
    setSelected([])
  }

  const download = async (kind: 'selected' | 'all') => {
    setBusy(kind)
    try {
      await downloadReportMarkdown(kind === 'selected' ? { ids: selected } : { filters })
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(null)
    }
  }

  const total = data?.total ?? 0
  const onRange = (v: [Dayjs | null, Dayjs | null] | null) =>
    patch({ date_from: v?.[0]?.format('YYYY-MM-DD') ?? '', date_to: v?.[1]?.format('YYYY-MM-DD') ?? '' })

  const columns = [
    { title: t('reportExport.colDate'), dataIndex: 'date', width: 110 },
    { title: t('reportExport.colTitle'), dataIndex: 'title' },
    { title: t('reportExport.colType'), dataIndex: 'rtype', width: 160 },
    {
      title: t('reportExport.colVersion'),
      dataIndex: 'generator_version',
      width: 110,
      render: (v: string) => (v ? <Tag>{v}</Tag> : null),
    },
  ]

  return (
    <Card title={t('reportExport.title')}>
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Text type="secondary">{t('reportExport.hint')}</Typography.Text>
        <Space wrap>
          <Select
            allowClear
            showSearch
            style={{ width: 200 }}
            placeholder={t('reportExport.type')}
            value={filters.rtype || undefined}
            onChange={(v) => patch({ rtype: v ?? '' })}
            options={(data?.types ?? []).map((v) => ({ value: v, label: v }))}
          />
          <Select
            allowClear
            showSearch
            style={{ width: 160 }}
            placeholder={t('reportExport.version')}
            value={filters.generator_version || undefined}
            onChange={(v) => patch({ generator_version: v ?? '' })}
            options={(data?.generator_versions ?? []).map((v) => ({ value: v, label: v }))}
          />
          <DatePicker.RangePicker allowEmpty={[true, true]} onChange={onRange} aria-label={t('reportExport.dateRange')} />
          <Input.Search
            allowClear
            style={{ width: 220 }}
            placeholder={t('reportExport.search')}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onSearch={(v) => patch({ q: v.trim() })}
          />
        </Space>
        {error && <Alert type="error" showIcon title={error} />}
        <Space wrap>
          <Button
            type="primary"
            icon={<DownloadOutlined />}
            disabled={total === 0}
            loading={busy === 'all'}
            onClick={() => download('all')}
          >
            {t('reportExport.downloadAll', { n: total })}
          </Button>
          <Button
            icon={<DownloadOutlined />}
            disabled={selected.length === 0}
            loading={busy === 'selected'}
            onClick={() => download('selected')}
          >
            {t('reportExport.downloadSelected', { n: selected.length })}
          </Button>
          {selected.length > 0 && <Button type="link" onClick={() => setSelected([])}>{t('reportExport.clearSelection')}</Button>}
        </Space>
        <div style={{ overflowX: 'auto' }}>
          <Table<AdminExportReport>
            rowKey="id"
            size="small"
            loading={loading}
            dataSource={data?.reports ?? []}
            columns={columns}
            rowSelection={{
              selectedRowKeys: selected,
              preserveSelectedRowKeys: true,
              onChange: (keys) => setSelected(keys as number[]),
            }}
            pagination={{
              current: page,
              pageSize: PAGE_SIZE,
              total,
              size: 'small',
              showSizeChanger: false,
              onChange: setPage,
            }}
          />
        </div>
      </Space>
    </Card>
  )
}
