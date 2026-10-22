import type { CSSProperties } from 'react'
import { Tag, Typography, theme } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type {
  ForecastCell,
  ForecastCheckpoint,
  ForecastGroup,
  ForecastGroupsResponse,
  ForecastMetric,
  ForecastMode,
} from '../../api/types'
import { cellDisplay } from '../../lib/forecast'
import { forecastPalette } from '../../lib/forecastPresentation'
import ForecastLegend from './ForecastLegend'

// The verification matrix (plan §8.1).
//
// Rows are groups, newest first; columns are the checkpoints plus coverage. A mature cell shows its
// value, its p (or p* in the exploratory tab), n and the excluded count. The colour follows the
// A-share convention — red up, green down — and only when the corrected q passes 0.05: a number
// whose correction does not separate it from its null must not be coloured as if it had.

interface Props {
  resp: ForecastGroupsResponse
  metric: ForecastMetric
  mode: ForecastMode
  onOpen: (g: ForecastGroup, cp: ForecastCheckpoint) => void
}

export default function ForecastMatrix({ resp, metric, mode, onOpen }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const palette = forecastPalette(token)

  const box: CSSProperties = {
    borderRadius: 6,
    padding: '8px 10px',
    minHeight: 64,
    display: 'flex',
    flexDirection: 'column',
    justifyContent: 'center',
    gap: 2,
  }

  const cellBox = (c: ForecastCell, cp: ForecastCheckpoint, g: ForecastGroup) => {
    if (c.state === 'open') {
      return (
        <div style={{ ...box, background: token.colorFillQuaternary, color: token.colorTextTertiary }}>
          {t(mode === 'month' ? 'forecast.cell.open_month' : 'forecast.cell.open')}
        </div>
      )
    }
    if (c.state === 'small') {
      return (
        <div style={{ ...box, background: token.colorFillQuaternary, color: token.colorTextTertiary }}>
          <span>{t('forecast.cell.small', { n: c.n ?? 0 })}</span>
          <span style={{ fontSize: 12 }}>{t('forecast.cell.small_hint')}</span>
        </div>
      )
    }
    if (c.state === 'pending') {
      const done = c.resolved ?? 0
      const total = c.total ?? 0
      return (
        <div style={{ ...box, background: token.colorFillQuaternary, color: token.colorTextTertiary }}>
          <span>{t('forecast.cell.pending', { done, total })}</span>
          <div style={{ height: 4, borderRadius: 2, background: token.colorFillSecondary, overflow: 'hidden' }}>
            <div
              style={{
                height: 4,
                width: `${total > 0 ? Math.min(100, (done / total) * 100) : 0}%`,
                background: token.colorTextTertiary,
              }}
            />
          </div>
        </div>
      )
    }

    const d = cellDisplay(c, metric, mode, t)
    if (!d) return null
    const excluded = (c.refused ?? 0) + (c.corp_action ?? 0)
    const tone = d.signal > 0 ? palette.positive : d.signal < 0 ? palette.negative : palette.neutral
    return (
      <button
        type="button"
        onClick={() => onOpen(g, cp)}
        style={{
          ...box,
          background: tone.background,
          color: tone.foreground,
          border: `1px solid ${token.colorBorderSecondary}`,
          textAlign: 'left',
          cursor: 'pointer',
          font: 'inherit',
          width: '100%',
        }}
      >
        <span style={{ fontWeight: 600, fontSize: 15 }}>{d.value}</span>
        {d.signal !== 0 && (
          <span style={{ fontSize: 12 }}>
            <span aria-hidden="true">{d.signal > 0 ? '▲' : '▼'} </span>
            <span className="rp-sr-only">{t(d.signal > 0 ? 'forecast.signal.positive' : 'forecast.signal.negative')}: </span>
            q&lt;0.05
          </span>
        )}
        <span style={{ fontSize: 12, color: token.colorTextSecondary }}>{d.sub}</span>
        {excluded > 0 && (
          <span style={{ fontSize: 12, color: token.colorTextTertiary }}>
            {t('forecast.cell.excluded', { n: excluded })}
          </span>
        )}
      </button>
    )
  }

  const coverageBar = (g: ForecastGroup) => {
    const c = g.coverage
    const total = Math.max(1, c.reports)
    const seg = (n: number, colour: string) => (
      <i style={{ display: 'block', height: 6, width: `${(n / total) * 100}%`, background: colour }} />
    )
    const rejected = c.rejected
    return (
      <div style={{ minWidth: 132 }}>
        <div style={{ display: 'flex', borderRadius: 3, overflow: 'hidden', background: token.colorFillQuaternary }}>
          {seg(c.member, token.colorPrimary)}
          {seg(c.duplicate, token.colorInfo)}
          {seg(c.absent + c.rejected, token.colorWarning)}
          {seg(c.backtest + c.degraded + c.mismatch + c.pre_live + c.test + c.off_benchmark, token.colorFillSecondary)}
        </div>
        <div style={{ fontSize: 12, color: token.colorTextSecondary, marginTop: 3 }}>
          {t('forecast.cov.summary', { member: c.member, reports: c.reports })}
          {rejected > 0 && (
            <>
              {' · '}
              <Link to="/manage/audit?action=predictions.reject">
                <Typography.Text type="warning" style={{ fontSize: 12 }}>
                  {t('forecast.cov.rejected', { n: rejected })}
                </Typography.Text>
              </Link>
            </>
          )}
        </div>
        {c.pending_baseline > 0 && (
          <div style={{ fontSize: 12, color: token.colorWarning }}>
            {t('forecast.cov.pending_baseline', { n: c.pending_baseline })}
          </div>
        )}
        {c.test > 0 && (
          <div style={{ fontSize: 12, color: token.colorTextSecondary }}>
            {t('forecast.cov.test', { n: c.test })}
          </div>
        )}
        {c.off_benchmark > 0 && (
          <div style={{ fontSize: 12, color: token.colorTextSecondary }}>
            {t('forecast.cov.off_benchmark', { n: c.off_benchmark })}
          </div>
        )}
      </div>
    )
  }

  const rows = [...resp.groups].reverse()
  const th: CSSProperties = {
    textAlign: 'left',
    padding: '6px 8px',
    fontSize: 12,
    color: token.colorTextSecondary,
    fontWeight: 500,
    whiteSpace: 'nowrap',
  }
  const td: CSSProperties = { padding: 6, verticalAlign: 'top' }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {t(`forecast.metric.${metric}.help`)}
        {' · '}
        {t(mode === 'custom' ? 'forecast.legend.custom' : 'forecast.legend.monthly')}
      </Typography.Text>
      <ForecastLegend />
      <div style={{ overflowX: 'auto', overscrollBehaviorX: 'contain' }}>
        <table style={{ borderCollapse: 'separate', borderSpacing: 4, width: '100%' }}>
          <thead>
            <tr>
              <th style={th}>{t(mode === 'month' ? 'forecast.col.month' : 'forecast.col.group')}</th>
              {resp.checkpoints.map((cp) => (
                <th key={cp} style={th}>
                  {t(`forecast.cp.${cp}`)}
                </th>
              ))}
              <th style={th}>{t('forecast.col.coverage')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((g) => (
              <tr key={`${g.start}:${g.end}:${g.label}`}>
                <td style={{ ...td, whiteSpace: 'nowrap', fontWeight: 600 }}>
                  {g.label}
                  {!g.official && (
                    <Tag style={{ marginInlineStart: 6 }} color="warning">
                      {t('forecast.tag.exploratory')}
                    </Tag>
                  )}
                  {g.bench && (
                    <Typography.Text type="secondary" style={{ fontSize: 12, marginInlineStart: 6 }}>
                      {t(`forecast.bench.${g.bench}`)}
                    </Typography.Text>
                  )}
                </td>
                {resp.checkpoints.map((cp) => (
                  <td key={cp} style={td}>
                    {cellBox(g.cells[cp], cp, g)}
                  </td>
                ))}
                <td style={td}>{coverageBar(g)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
