import type { CSSProperties } from 'react'
import { theme } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ForecastCell, ForecastCheckpoint, ForecastMetric, ForecastMode } from '../../api/types'
import { capExceeded, cellDisplay, notBeforeInfo, pendingReasonParts } from '../../lib/forecast'
import { forecastPalette, forecastWarningText } from '../../lib/forecastPresentation'

// One cell of the verification matrix: what a group has to say about one checkpoint.
//
// Five states, drawn so that a reader can tell them apart without colour and can tell why a cell has
// no number. A number appears ONLY on a mature cell (cellDisplay refuses to read a metric from any
// other state, so a payload that wrongly carries one still renders as what the cell is).
//
//   open     the group has not ended; dashed, because it is not a result and not yet a population
//   pending  the group ended and its checkpoint has not: progress, the phases holding it, the date
//   small    ended and fully settled but under 30 names: the size, the gate, "not published"
//   purged   removed by the retention policy
//   mature   a published result: the value, its sign, its significance, its corrected p
//
// A pending or small cell opens the drawer in membership mode (who is in this cohort and what each
// is waiting on), because its membership is already settled even though its results are not. An
// open cell's population is still growing and a purged one is gone, so neither opens anything.

interface Props {
  cell: ForecastCell
  cp: ForecastCheckpoint
  metric: ForecastMetric
  mode: ForecastMode
  /** The id of the line that explains the n >= 30 flag, which a flagged cell points at. */
  gateHintId: string
  /** The response's own instant: a date earlier than its day is not printed (see notBeforeInfo). */
  generatedAt?: string
  onOpen: () => void
}

export default function ForecastCellView({ cell: c, cp, metric, mode, gateHintId, generatedAt, onOpen }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const palette = forecastPalette(token)

  const box: CSSProperties = {
    borderRadius: token.borderRadiusSM,
    padding: '8px 10px',
    minHeight: 64,
    flex: '1 1 0%',
    minWidth: 0,
    boxSizing: 'border-box',
    display: 'flex',
    flexDirection: 'column',
    justifyContent: 'center',
    gap: 2,
    borderWidth: 1,
    borderStyle: 'solid',
  }
  const quiet: CSSProperties = { background: token.colorFillQuaternary, color: token.colorTextSecondary }
  const press: CSSProperties = { textAlign: 'left', font: 'inherit', width: '100%' }
  const small: CSSProperties = { fontSize: 12, lineHeight: 1.4 }

  if (c.state === 'open') {
    return (
      <div data-state="open" style={{ ...box, ...quiet, borderStyle: 'dashed', borderColor: token.colorBorder }}>
        {t(mode === 'month' ? 'forecast.cell.open_month' : 'forecast.cell.open')}
      </div>
    )
  }

  if (c.state === 'purged') {
    return (
      <div data-state="purged" style={{ ...box, ...quiet, borderColor: 'transparent' }}>
        {t('forecast.cell.purged')}
      </div>
    )
  }

  if (c.state === 'small') {
    return (
      <button type="button" data-state="small" className="rp-fc-cell" onClick={onOpen} style={{ ...box, ...quiet, ...press }}>
        <span>{c.n === undefined ? t('forecast.cell.small_nocount') : t('forecast.cell.small', { n: c.n })}</span>
      </button>
    )
  }

  if (c.state === 'pending') {
    // The server sent no denominator (an external reader with the all scope), so there is no
    // progress, no reason and no date to draw: a bar at 0 of 0 would say nothing has matured, which
    // nobody knows, and the reasons and dates are volume that reader may not have.
    if (c.total === undefined) {
      return (
        <button type="button" data-state="pending" className="rp-fc-cell" onClick={onOpen} style={{ ...box, ...quiet, ...press }}>
          {t('forecast.cell.pending_plain')}
        </button>
      )
    }
    const cap = capExceeded(c)
    if (cap) {
      return (
        <button type="button" data-state="pending" className="rp-fc-cell" onClick={onOpen} style={{ ...box, ...quiet, ...press }}>
          {cap.refused !== undefined && cap.total !== undefined && cap.pct !== undefined && (
            <span>{t('forecast.cell.cap', { refused: cap.refused, total: cap.total, pct: cap.pct })}</span>
          )}
          <span style={{ ...small, color: forecastWarningText(token) }}>{t('forecast.cell.cap_hint')}</span>
        </button>
      )
    }
    const done = c.resolved ?? 0
    const total = c.total
    const reasons = pendingReasonParts(c)
    const until = notBeforeInfo(c, cp, generatedAt)
    return (
      <button type="button" data-state="pending" className="rp-fc-cell" onClick={onOpen} style={{ ...box, ...quiet, ...press }}>
        <span>{t('forecast.cell.pending', { done, total })}</span>
        <div data-part="progress" aria-hidden="true" style={{ height: 4, borderRadius: token.borderRadiusXS, background: token.colorFillSecondary, overflow: 'hidden' }}>
          <div
            style={{
              height: 4,
              width: `${total > 0 ? Math.min(100, (done / total) * 100) : 0}%`,
              background: token.colorTextTertiary,
            }}
          />
        </div>
        {reasons.length > 0 && (
          <span style={small}>
            {reasons
              .map((r) => t('forecast.cell.reason', { phase: t(`forecast.phase.${r.phase}`), n: r.n }))
              .join(' · ')}
          </span>
        )}
        {until && (
          <span style={small}>
            {cp === 'h12'
              ? t('forecast.cell.from_month', { month: until.date })
              : t(until.expected ? 'forecast.cell.expected' : 'forecast.cell.not_before', { date: until.date })}
          </span>
        )}
      </button>
    )
  }

  const d = cellDisplay(c, metric, mode, t)
  if (!d) return null
  const excluded = (c.refused ?? 0) + (c.corp_action ?? 0)
  const tone = d.signal > 0 ? palette.positive : d.signal < 0 ? palette.negative : palette.neutral
  return (
    <button
      type="button"
      data-state="mature"
      className="rp-fc-cell"
      aria-describedby={d.gate ? gateHintId : undefined}
      onClick={onOpen}
      style={{ ...box, ...press, background: tone.background, color: tone.foreground }}
    >
      <span style={{ fontWeight: 600, fontSize: 18, lineHeight: 1.3 }}>{d.value}</span>
      {d.signal !== 0 && (
        <span style={small}>
          <span aria-hidden="true">{d.signal > 0 ? '▲' : '▼'} </span>
          <span className="rp-sr-only">{t(d.signal > 0 ? 'forecast.signal.positive' : 'forecast.signal.negative')}: </span>
          {/* The exploratory view prints p*, so its mark is worded from p* too: a q there would claim
              a correction for the choice of range, which the view does not make. */}
          {t(mode === 'custom' ? 'forecast.signal.badge_custom' : 'forecast.signal.badge')}
        </span>
      )}
      {d.sub && <span style={{ ...small, color: token.colorTextSecondary }}>{d.sub}</span>}
      {d.gate && (
        // The cell's own assurance, drawn once. It is not the metric's sample size: that
        // denominator is a smaller subset which has no n >= 30 minimum of its own.
        <span
          style={{
            alignSelf: 'flex-start',
            fontSize: 11,
            lineHeight: '16px',
            padding: '0 5px',
            borderRadius: token.borderRadiusSM,
            border: `1px solid ${token.colorBorderSecondary}`,
            color: token.colorTextSecondary,
          }}
        >
          {d.gate}
        </span>
      )}
      {excluded > 0 && <span style={{ ...small, color: token.colorTextSecondary }}>{t('forecast.cell.excluded', { n: excluded })}</span>}
    </button>
  )
}
