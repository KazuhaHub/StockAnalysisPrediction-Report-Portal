import { useState } from 'react'
import { Alert, Button, Card, Select, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ForecastCheckpoint, ForecastGroup, ForecastGroupsResponse } from '../../api/types'
import { formatSigned } from '../../lib/forecastPresentation'
import { gateText } from '../../lib/forecast'

interface Props {
  response: ForecastGroupsResponse
  onOpen: (group: ForecastGroup, checkpoint: ForecastCheckpoint) => void
}

export default function ForecastReadableOverview({ response, onOpen }: Props) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState('')
  const options = [...response.groups].reverse().flatMap((group) => response.checkpoints
    .filter((cp) => group.cells[cp])
    .map((cp) => ({ key: JSON.stringify([group.start, group.end, group.bench, group.label, cp]), group, cp, cell: group.cells[cp] })))
  const current = options.find((o) => o.key === selected) ?? options.find((o) => o.cell.state === 'mature') ?? options[0]
  if (!current) return null
  const { group, cell, cp } = current
  const mature = cell.state === 'mature'
  const known = (v: number | undefined) => v !== undefined && Number.isFinite(v)
  const metric = (value: number | undefined, format: (v: number) => string) => mature && known(value) ? format(value!) : '—'
  const cards = [
    { key: 'sample', value: mature || cell.state === 'small' ? cell.n !== undefined ? String(cell.n) : gateText(cell, t) || '—' : '—' },
    { key: 'hit', value: metric(cell.hit, (v) => `${(v * 100).toFixed(0)}%`) },
    { key: 'spread', value: !cell.spread_one_arm ? metric(cell.spread_bp, (v) => formatSigned(v / 100, 1)) : '—' },
    { key: 'ic', value: metric(cell.ic, (v) => v.toFixed(2)) },
    { key: 'fill', value: metric(cell.fill, (v) => `${(v * 100).toFixed(0)}%`) },
  ]
  return <section aria-label={t('forecast.overview.title')} style={{ display: 'grid', gap: 16 }}>
    <Space wrap style={{ justifyContent: 'space-between' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>{t('forecast.overview.title')}</Typography.Title>
      <Select aria-label={t('forecast.overview.selection')} value={current.key} onChange={setSelected}
        style={{ width: 'min(100%, 420px)', minWidth: 240 }}
        options={options.map((o) => ({ value: o.key, label: `${o.group.label} · ${t(`forecast.bench.${o.group.bench}`)} · ${t(`forecast.cp.${o.cp}`)}` }))} />
    </Space>
    <Typography.Text type="secondary">{t('forecast.overview.scope')}</Typography.Text>
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))', gap: 12 }}>
      {cards.map((card) => <Card key={card.key} size="small">
        <Typography.Text type="secondary">{t(`forecast.overview.${card.key}`)}</Typography.Text>
        <div style={{ fontSize: 30, fontWeight: 650, margin: '8px 0', fontVariantNumeric: 'tabular-nums' }}>{card.value}</div>
        <Typography.Text type="secondary">{t(`forecast.overview.${card.key}_help`)}</Typography.Text>
      </Card>)}
    </div>
    {group.coverage && <Typography.Text type="secondary">{t('forecast.overview.coverage', {
      reports: group.coverage.reports, members: group.coverage.member, pending: group.coverage.pending_baseline,
    })}</Typography.Text>}
    {!mature && <Alert type="info" showIcon role="note" title={t('forecast.overview.waiting')}
      description={t(`forecast.overview.state_${cell.state}`)} />}
    {mature && <Typography.Text type="secondary">{t('forecast.overview.interpretation')}</Typography.Text>}
    <div><Button type="primary" onClick={() => onOpen(group, cp)}>{t('forecast.overview.details')}</Button></div>
  </section>
}
