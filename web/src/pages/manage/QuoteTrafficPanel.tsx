import { Card, Descriptions, Table, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

export type QuoteRequestTraffic = {
  requests: number; completed: number; cacheHits: number; merged: number; loads: number
  failures: number; stale: number; p95Millis: number
}
export type QuoteDisplayTraffic = {
  live: QuoteRequestTraffic
  history: QuoteRequestTraffic
  batch: { requests: number; completed: number; symbols: number; cachedSymbols: number; missingSymbols: number; merged: number; loads: number; p95Millis: number }
}

const percent = (part: number, total: number) => total > 0 ? `${(100 * part / total).toFixed(1)}%` : '—'

export default function QuoteTrafficPanel({ traffic }: { traffic?: QuoteDisplayTraffic }) {
  const { t } = useTranslation()
  if (!traffic) return null
  const columns = [
    { title: t('quoteAdmin.display.path'), dataIndex: 'label', key: 'label' },
    ...(['requests', 'completed', 'cacheHits', 'merged', 'loads', 'failures', 'stale'] as const).map((field) => ({
      title: t(`quoteAdmin.display.${field}`), dataIndex: field, key: field,
    })),
    { title: t('quoteAdmin.display.hitRatio'), key: 'ratio', render: (_: unknown, row: QuoteRequestTraffic) => percent(row.cacheHits, row.completed) },
    { title: t('quoteAdmin.display.p95'), key: 'p95', render: (_: unknown, row: QuoteRequestTraffic) => row.completed > 0 ? `${row.p95Millis} ms` : '—' },
  ]
  const batch = traffic.batch
  return <Card title={t('quoteAdmin.display.title')}>
    <Table rowKey="key" size="small" pagination={false} scroll={{ x: 'max-content' }} columns={columns}
      onRow={(row) => ({ 'data-testid': `quote-traffic-${row.key}` } as React.HTMLAttributes<HTMLTableRowElement>)}
      dataSource={[{ ...traffic.live, key: 'live', label: t('quoteAdmin.display.live') }, { ...traffic.history, key: 'history', label: t('quoteAdmin.display.history') }]} />
    <Typography.Title level={5}>{t('quoteAdmin.display.batch')}</Typography.Title>
    <Descriptions size="small" items={[
      { key: 'requests', label: t('quoteAdmin.display.requests'), children: batch.requests },
      { key: 'completed', label: t('quoteAdmin.display.completed'), children: batch.completed },
      { key: 'symbols', label: t('quoteAdmin.display.symbols'), children: batch.symbols },
      { key: 'cached', label: t('quoteAdmin.display.cachedSymbols'), children: batch.cachedSymbols },
      { key: 'ratio', label: t('quoteAdmin.display.symbolRatio'), children: percent(batch.cachedSymbols, batch.symbols) },
      { key: 'missing', label: t('quoteAdmin.display.missingSymbols'), children: batch.missingSymbols },
      { key: 'merged', label: t('quoteAdmin.display.merged'), children: batch.merged },
      { key: 'loads', label: t('quoteAdmin.display.loads'), children: batch.loads },
      { key: 'p95', label: t('quoteAdmin.display.p95'), children: batch.completed > 0 ? `${batch.p95Millis} ms` : '—' },
    ]} />
    <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>{t('quoteAdmin.display.scope')}</Typography.Paragraph>
  </Card>
}
