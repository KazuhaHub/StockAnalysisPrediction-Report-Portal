import { Alert, Card, InputNumber, Space, Table, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

export type VendorRatePolicy = { requestsPerMinute: number; backgroundPerMinute: number }
export type VendorScheduleStatus = {
  requests: number; failures: number; deferred: number; requestsLastMinute: number
  queued: number; inFlight: number; oldestWaitMillis: number; waitMillis: number; durationMillis: number
  coolingUntil: string; probing: boolean
}

export default function VendorSchedulePanel({ policies, schedule, sources, sourceLabel, onChange }: {
  policies: Record<string, VendorRatePolicy>
  schedule?: Record<string, VendorScheduleStatus>
  sources: string[]
  sourceLabel: (source: string) => string
  onChange: (policies: Record<string, VendorRatePolicy>) => void
}) {
  const { t } = useTranslation()
  const change = (source: string, field: keyof VendorRatePolicy, value: number | null) => {
    if (value === null || !Number.isInteger(value)) return
    onChange({ ...policies, [source]: { ...policies[source], [field]: value } })
  }
  const columns = [
    { title: t('quoteAdmin.source'), key: 'source', render: (_: unknown, source: string) => sourceLabel(source) },
    { title: t('quoteAdmin.traffic.total'), key: 'total', render: (_: unknown, source: string) => policies[source] ? <InputNumber
      min={2} max={60} precision={0} value={policies[source].requestsPerMinute}
      aria-label={`${sourceLabel(source)} ${t('quoteAdmin.traffic.total')}`}
      onChange={(value) => change(source, 'requestsPerMinute', value)} /> : '—' },
    { title: t('quoteAdmin.traffic.background'), key: 'background', render: (_: unknown, source: string) => policies[source] ? <InputNumber
      min={1} max={Math.floor(policies[source].requestsPerMinute / 2)} precision={0} value={policies[source].backgroundPerMinute}
      aria-label={`${sourceLabel(source)} ${t('quoteAdmin.traffic.background')}`}
      onChange={(value) => change(source, 'backgroundPerMinute', value)} /> : '—' },
    ...(['requestsLastMinute', 'requests', 'failures', 'deferred', 'queued', 'inFlight', 'oldestWaitMillis'] as const).map((field) => ({
      title: t(`quoteAdmin.traffic.${field}`), key: field, render: (_: unknown, source: string) => schedule?.[source]?.[field] ?? '—',
    })),
    { title: t('quoteAdmin.traffic.cooldown'), key: 'cooldown', render: (_: unknown, source: string) => {
      const status = schedule?.[source]
      if (!status) return '—'
      if (status.probing) return t('quoteAdmin.traffic.probing')
      return status.coolingUntil ? <Typography.Text code>{status.coolingUntil}</Typography.Text> : t('quoteAdmin.traffic.ready')
    } },
  ]
  return <Card title={t('quoteAdmin.traffic.title')}>
    <Space orientation="vertical" size={12} style={{ width: '100%' }}>
      <Alert type="info" showIcon title={t('quoteAdmin.traffic.hint')} />
      <Table<string> rowKey={(source) => source} dataSource={sources} columns={columns} pagination={false} size="small" scroll={{ x: 1500 }} />
      <Typography.Text type="secondary">{t('quoteAdmin.traffic.scope')}</Typography.Text>
    </Space>
  </Card>
}
