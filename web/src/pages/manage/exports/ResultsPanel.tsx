import { Alert, Button, Empty, Space, Table, Typography } from 'antd'
import { DownloadOutlined, SettingOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import { useTranslation } from 'react-i18next'
import type { ExportSource } from '../../../api/types'
import { formatCell } from '../../../lib/exportTime'
import type { ExportLabels } from './labels'

type Row = Record<string, string | number | null>
export const PAGE_SIZES = [20, 50, 100, 200]

// Live results: "Showing a-b of N", Export all, the column gear, the table and its pager.
export default function ResultsPanel({
  source,
  columns,
  rows,
  total,
  loading,
  error,
  page,
  pageSize,
  tz,
  labels,
  onPage,
  onManageColumns,
  onExportAll,
}: {
  source: ExportSource
  columns: string[]
  rows: Row[]
  total: number | null
  loading: boolean
  error: string
  page: number
  pageSize: number
  tz: string
  labels: ExportLabels
  onPage: (page: number, pageSize: number) => void
  /** Absent for a reader, who does not choose columns. */
  onManageColumns?: () => void
  onExportAll: () => void
}) {
  const { t } = useTranslation()
  const from = total ? (page - 1) * pageSize + 1 : 0
  const to = total ? Math.min(total, from + rows.length - 1) : 0

  const cols: ColumnsType<Row> = columns.map((key) => ({
    key,
    dataIndex: key,
    title: labels.column(key),
    ellipsis: true,
    width: key === 'detail' ? 280 : key === 'id' ? 80 : undefined,
    render: (v: string | number | null) => {
      const text = formatCell(key, v, tz, source.time_field)
      const shown = key === 'action' || key === 'target_type' ? labels.value(key, text) : text
      return shown === '' ? <Typography.Text type="secondary">—</Typography.Text> : <span title={shown}>{shown}</span>
    },
  }))

  return (
    <div>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, marginBottom: 12 }}>
        <Typography.Text role="status" type="secondary">
          {total === null ? t('common.loading') : total === 0 ? t('export.results.none') : t('export.results.showing', { from, to, total })}
        </Typography.Text>
        <Space style={{ marginInlineStart: 'auto' }}>
          <Button type="primary" icon={<DownloadOutlined />} disabled={!total} onClick={onExportAll}>
            {t('export.results.exportAll', { n: total ?? 0 })}
          </Button>
          {onManageColumns && <Button icon={<SettingOutlined />} aria-label={t('export.results.columns')} title={t('export.results.columns')} onClick={onManageColumns} />}
        </Space>
      </div>
      {error && <Alert type="error" showIcon title={error} style={{ marginBottom: 12 }} />}
      <Table<Row>
        size="small"
        rowKey={(r) => String(r.id ?? JSON.stringify(r))}
        loading={loading}
        columns={cols}
        dataSource={rows}
        tableLayout="fixed"
        scroll={{ x: 'max-content' }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('export.results.none')} /> }}
        pagination={{
          current: page,
          pageSize,
          total: total ?? 0,
          pageSizeOptions: PAGE_SIZES,
          showSizeChanger: true,
          size: 'small',
          showTotal: undefined,
          onChange: onPage,
        }}
      />
    </div>
  )
}
