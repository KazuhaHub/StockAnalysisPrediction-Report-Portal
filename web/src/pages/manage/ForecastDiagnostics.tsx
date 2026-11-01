import { Alert, Card, Collapse, Space, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ForecastDiagnostics as Diagnostics } from '../../api/types'
import CardTable from '../../components/CardTable'
import StatusTag from '../../components/StatusTag'

// Missing, unreadable and measured-zero telemetry are different states. These retained samples
// describe comparison only; agreement does not grant a candidate permission to settle anything.
export default function ForecastDiagnostics({ value, fallback }: { value?: Diagnostics; fallback?: 'off' | 'shadow' }) {
  const { t } = useTranslation()
  const title = t('forecast.diagnostics.title')
  const mode = fallback && <Tag>{t(fallback === 'shadow' ? 'forecast.settings.fallbackShadow' : 'forecast.settings.fallbackOff')}</Tag>
  if (!value || value.status === 'unavailable' || (value.status === 'ready' && (!value.shadow || !value.calls))) {
    return <Card title={title} extra={mode}><Alert type="warning" showIcon title={t('forecast.diagnostics.unavailable')} /></Card>
  }
  if (value.status === 'empty') {
    return <Card title={title} extra={mode}><Alert type="info" showIcon title={t('forecast.diagnostics.empty')} /></Card>
  }
  const shadow = value.shadow!
  const calls = value.calls!
  const callRows = calls.days.flatMap((day) => Object.entries(day.sources).sort(([a], [b]) => a.localeCompare(b)).map(([source, counters]) => ({ date: day.date, source, ...counters })))
  const column = (key: string) => ({ title: t(`forecast.diagnostics.${key === 'unavailable' ? 'unavailableCount' : key}`), dataIndex: key, key })
  return (
    <Card title={title} extra={mode}>
      <Space orientation="vertical" size={16} style={{ width: '100%', minWidth: 0 }}>
        <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
          {t('forecast.diagnostics.hint', { reads: value.max_reads, http: value.max_http_attempts })}
        </Typography.Paragraph>
        <Typography.Text>{t('forecast.diagnostics.updated', { at: shadow.updated_at })}</Typography.Text>
        <Typography.Title level={5} style={{ margin: 0 }}>{t('forecast.diagnostics.calls')}</Typography.Title>
        <CardTable
          size="small" pagination={false} scroll={{ x: 700 }} dataSource={callRows}
          rowKey={(row) => `${row.date}:${row.source}`}
          columns={[
            column('date'), column('source'), column('calls'), column('failures'), column('shadow_http_attempts'),
            { ...column('refusals'), render: (refusals: Record<string, number>) => Object.entries(refusals).sort(([a], [b]) => a.localeCompare(b)).map(([code, n]) => `${code}: ${n}`).join(', ') || '—' },
          ]}
        />
        <Typography.Title level={5} style={{ margin: 0 }}>{t('forecast.diagnostics.comparisons')}</Typography.Title>
        <CardTable
          size="small" pagination={false} scroll={{ x: 700 }} dataSource={shadow.days} rowKey="date"
          columns={['date', 'selected', 'reads', 'http_attempts', 'matches', 'mismatches', 'unavailable'].map(column)}
        />
        <Typography.Title level={5} style={{ margin: 0 }}>{t('forecast.diagnostics.recent')}</Typography.Title>
        {/* antd Collapse rather than a native disclosure: it animates and honours reduced motion. */}
        <Collapse
          size="small"
          style={{ width: '100%' }}
          defaultActiveKey={shadow.recent.length ? ['0'] : []}
          items={shadow.recent.map((result, index) => ({
            key: String(index),
            label: (
              <Space wrap>
                <Typography.Text>{result.at} · {result.target.market}{result.target.code} · {result.primary} → {result.candidate || '—'}</Typography.Text>
                <StatusTag tone={result.status === 'mismatch' ? 'warning' : result.status === 'match' ? 'success' : 'neutral'}>{t(`forecast.diagnostics.${result.status === 'unavailable' ? 'comparisonUnavailable' : result.status}`)}</StatusTag>
              </Space>
            ),
            children: (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                <Typography.Text>{t('forecast.diagnostics.anchors', { from: result.anchors[0], to: result.anchors[1] })}</Typography.Text>
                {result.reason && <Typography.Text code>{result.reason}</Typography.Text>}
                {result.comparison && <Typography.Text>{t('forecast.diagnostics.compared', { dates: result.comparison.compared_dates, differences: result.difference_count })}</Typography.Text>}
                {result.truncated && <Alert type="warning" showIcon title={t('forecast.diagnostics.truncated', { total: result.difference_count, shown: result.comparison?.differences.length ?? 0 })} />}
                {!!result.comparison?.differences.length && (
                  <CardTable
                    size="small"
                    pagination={false}
                    aria-label={t('forecast.diagnostics.differences')}
                    rowKey={(d) => `${d.date}:${d.field}`}
                    dataSource={result.comparison.differences}
                    columns={[
                      { key: 'barDate', title: t('forecast.diagnostics.barDate'), render: (_, d) => d.date },
                      { key: 'field', title: t('forecast.diagnostics.field'), render: (_, d) => d.field },
                      { key: 'anchor', title: t('forecast.diagnostics.anchor'), render: (_, d) => (d.anchor ? t('forecast.diagnostics.anchor') : '—') },
                      { key: 'primary', title: t('forecast.diagnostics.primary'), render: (_, d) => d.primary ?? '—' },
                      { key: 'candidate', title: t('forecast.diagnostics.candidate'), render: (_, d) => d.candidate ?? '—' },
                    ]}
                  />
                )}
              </div>
            ),
          }))}
        />
      </Space>
    </Card>
  )
}
