import { useId, useState } from 'react'
import { Card, Progress, Segmented, Space, Tag, Typography, theme } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { ForecastMember } from '../../api/types'
import { readableMembers } from '../../lib/forecastReadable'
import { formatSigned } from '../../lib/forecastPresentation'

interface Props {
  members: ForecastMember[]
  mature: boolean
  complete: boolean
  hideCounts?: boolean
}

export default function ForecastVisibleResults({ members, mature, complete, hideCounts = false }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const chartId = useId()
  const [filter, setFilter] = useState('all')
  if (!mature) return null
  const data = readableMembers(members)
  const aggregate = complete && !hideCounts
  const pct = (v: number | undefined) => v === undefined || !Number.isFinite(v) ? '—' : `${formatSigned(v / 100, 1)}%`
  const pp = (v: number | undefined) => v === undefined || !Number.isFinite(v) ? '—' : t('forecast.readable.points', { value: formatSigned(v / 100, 1) })
  const price = (v: number | undefined) => v === undefined || !Number.isFinite(v) ? '—' : (v / 100).toFixed(2)
  const domain = (values: number[]) => {
    const low = Math.min(0, ...values)
    const high = Math.max(0, ...values)
    const pad = Math.max((high - low) * 0.08, 0.01)
    return [low - pad, high + pad]
  }
  const [xmin, xmax] = domain(data.points.map((m) => m.upside! * 100))
  const [ymin, ymax] = domain(data.points.map((m) => m.excess_bp! / 100))
  const x = (v: number) => 65 + (v - xmin) / (xmax - xmin) * 460
  const y = (v: number) => 250 - (v - ymin) / (ymax - ymin) * 215
  const maxReturn = Math.max(1, ...data.returns.flatMap((r) => [Math.abs(r.stock ?? 0), Math.abs(r.bench ?? 0)]))
  const visible = data.members.filter((m) => filter === 'all' || (m.direction ?? 'untested') === filter)
  return <section aria-label={t('forecast.readable.title')} style={{ display: 'grid', gap: 20 }}>
    <Typography.Text type="secondary">{t('forecast.readable.visible_scope')}</Typography.Text>
    {!complete && <Typography.Text type="secondary">{t('forecast.readable.partial')}</Typography.Text>}
    {aggregate && <>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12 }}>
        {(['up', 'down'] as const).map((direction) => {
          const result = data[direction]
          const value = result.tested ? result.agreed / result.tested * 100 : undefined
          return <Card key={direction} size="small" title={t(`forecast.readable.${direction}_verdict`)}>
            <div style={{ fontSize: 28, fontWeight: 650 }}>{result.tested ? `${result.agreed} / ${result.tested}` : '—'}</div>
            {value !== undefined && <Progress percent={Math.round(value)} strokeColor={token.colorPrimary} />}
            <Typography.Text type="secondary">{t(`forecast.readable.${direction}_help`)}</Typography.Text>
          </Card>
        })}
      </div>
      <Typography.Text type="secondary">{t('forecast.readable.verdict_rule')}</Typography.Text>
      <Card size="small" title={t('forecast.readable.group_returns')}>
        <Typography.Paragraph type="secondary">{t('forecast.readable.paired')}</Typography.Paragraph>
        <div style={{ display: 'grid', gap: 20 }}>
          {data.returns.map((row) => <div key={row.stance}>
            <Typography.Text strong>{t(`forecast.stance.${row.stance}`)}</Typography.Text>
            {([['stock', row.stock], ['benchmark', row.bench]] as const).map(([name, value]) => <div key={name}
              style={{ display: 'grid', gridTemplateColumns: '80px 1fr 76px', alignItems: 'center', gap: 8, marginTop: 8 }}>
              <Typography.Text type="secondary">{t(`forecast.readable.${name}`)}</Typography.Text>
              <div style={{ position: 'relative', height: 18, background: token.colorFillQuaternary, borderRadius: token.borderRadiusXS }}>
                <div style={{ position: 'absolute', left: '50%', top: 0, bottom: 0, borderLeft: `1px solid ${token.colorTextTertiary}` }} />
                {value !== undefined && <div style={{ position: 'absolute', top: 3, height: 12,
                  left: value < 0 ? `${50 - Math.abs(value) / maxReturn * 48}%` : '50%',
                  width: `${Math.abs(value) / maxReturn * 48}%`, background: name === 'stock' ? token.colorPrimary : token.colorTextTertiary, borderRadius: token.borderRadiusXS }} />}
              </div>
              <span style={{ textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>{pct(value)}</span>
            </div>)}
            <Typography.Text type="secondary">{t('forecast.readable.return_detail', {
              n: row.count, median: pct(row.median), excess: pp(row.excess),
            })}</Typography.Text>
          </div>)}
        </div>
      </Card>
    </>}
    {aggregate && data.points.length > 0 && <Card size="small" title={t('forecast.readable.scatter')}>
      <Typography.Paragraph type="secondary">{t('forecast.readable.scatter_help')}</Typography.Paragraph>
      <svg role="img" aria-labelledby={`${chartId}-title`} aria-describedby={`${chartId}-desc`} viewBox="0 0 560 305" style={{ width: '100%', maxHeight: 360 }}>
        <title id={`${chartId}-title`}>{t('forecast.readable.scatter')}</title>
        <desc id={`${chartId}-desc`}>{t('forecast.readable.scatter_description')}</desc>
        <rect x="65" y="35" width="460" height="215" fill={token.colorFillQuaternary} />
        <line x1={x(0)} x2={x(0)} y1="35" y2="250" stroke={token.colorTextTertiary} />
        <line x1="65" x2="525" y1={y(0)} y2={y(0)} stroke={token.colorTextTertiary} />
        {[xmin, (xmin + xmax) / 2, xmax].map((v) => <text key={v} x={x(v)} y="270" textAnchor="middle" fontSize="11" fill={token.colorTextSecondary}>{v.toFixed(1)}%</text>)}
        {[ymin, (ymin + ymax) / 2, ymax].map((v) => <text key={v} x="58" y={y(v) + 4} textAnchor="end" fontSize="11" fill={token.colorTextSecondary}>{v.toFixed(1)}</text>)}
        <text x="295" y="298" textAnchor="middle" fontSize="12" fill={token.colorText}>{t('forecast.readable.predicted')}</text>
        <text x="65" y="20" fontSize="12" fill={token.colorText}>{t('forecast.readable.excess_axis')}</text>
        {data.points.map((m) => <circle key={m.set_id} cx={x(m.upside! * 100)} cy={y(m.excess_bp! / 100)} r="4.5" fill={token.colorPrimary} fillOpacity="0.75">
          <title>{`${m.name || m.symbol} · ${m.made_on} · ${formatSigned(m.upside! * 100, 1)}% · ${pp(m.excess_bp)}`}</title>
        </circle>)}
      </svg>
    </Card>}
    <div>
      <Typography.Title level={5}>{t('forecast.readable.title')}</Typography.Title>
      <Segmented aria-label={t('forecast.readable.filter')} value={filter} onChange={setFilter}
        options={['all', 'agree', 'disagree', 'flat', 'untested'].map((value) => ({ value, label: t(`forecast.readable.filter_${value}`) }))}
        style={{ marginBottom: 12, maxWidth: '100%', overflowX: 'auto' }} />
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 270px), 1fr))', gap: 12 }}>
        {visible.map((m) => <Card key={m.set_id} size="small" title={<Space wrap><span>{m.name || m.symbol}</span><Typography.Text type="secondary">{m.name ? m.symbol : null}</Typography.Text></Space>}>
          <Space orientation="vertical" size={8} style={{ width: '100%' }}>
            <Space wrap><Tag>{t(`forecast.stance.${m.stance}`)}</Tag><Tag>{t(`forecast.direction.${m.direction ?? 'untested'}`)}</Tag>{m.halted && <Tag>{t('forecast.mark.halted')}</Tag>}</Space>
            <Typography.Text type="secondary">{t('forecast.readable.received', { date: m.made_on })}</Typography.Text>
            <div>{t('forecast.readable.forecast', { price: price(m.terminal_fen), upside: m.upside === undefined || !Number.isFinite(m.upside) ? '—' : `${formatSigned(m.upside * 100, 1)}%` })}</div>
            <div>{t('forecast.readable.closes', { from: price(m.s0_fen), to: price(m.close_fen) })}</div>
            <div>{t('forecast.readable.stock_return', { value: pct(m.ret_bp) })}</div>
            <div>{t('forecast.readable.bench_return', { value: pct(m.bench_ret_bp) })}</div>
            <Typography.Text strong>{t('forecast.readable.excess_return', { value: pp(m.excess_bp) })}</Typography.Text>
            {m.report_id && <Link to={`/stock/${encodeURIComponent(m.symbol)}?${new URLSearchParams({ date: m.report_date, r: String(m.report_id), ...(m.kind ? { kind: m.kind } : {}) })}`}>{t('forecast.readable.open_report')}</Link>}
          </Space>
        </Card>)}
      </div>
      {visible.length === 0 && <Typography.Text type="secondary">{t('forecast.readable.no_match')}</Typography.Text>}
    </div>
  </section>
}
