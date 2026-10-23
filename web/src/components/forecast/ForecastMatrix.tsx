import { useId, type CSSProperties } from 'react'
import { Tag, Typography, theme } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type {
  ForecastCheckpoint,
  ForecastGroup,
  ForecastGroupsResponse,
  ForecastMetric,
  ForecastMode,
} from '../../api/types'
import { gateText } from '../../lib/forecast'
import ForecastCellView from './ForecastCellView'
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
  /**
   * Whether a rejected-sets count links to the audit log. The log is an administrator page, so a
   * reader's matrix states the count without a link that would only bounce them home.
   */
  auditLink?: boolean
}

// A reader response is not an admin response with fields missing by accident. An external reader
// with the all scope gets no coverage and no counts on purpose (a count would reveal the ledger's
// volume), so every count here is read as possibly absent and an absent one is never drawn as zero.
export default function ForecastMatrix({ resp, metric, mode, onOpen, auditLink = true }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  // The gate flag's explanation is drawn once, as text, and each flagged cell points at it.
  const gateHintId = useId()

  const coverageBar = (g: ForecastGroup) => {
    const c = g.coverage
    if (!c) return null
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
              {auditLink ? (
                <Link to="/manage/audit?action=predictions.reject">
                  <Typography.Text type="warning" style={{ fontSize: 12 }}>
                    {t('forecast.cov.rejected', { n: rejected })}
                  </Typography.Text>
                </Link>
              ) : (
                <Typography.Text type="warning" style={{ fontSize: 12 }}>
                  {t('forecast.cov.rejected', { n: rejected })}
                </Typography.Text>
              )}
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

  // A flag stands where a count would have been, so it needs its meaning on the page: the badge
  // alone says "n >= 30" and nothing about which sample that is, and a hover title is not reachable
  // from a keyboard or a phone.
  const showsGate = resp.groups.some((g) =>
    Object.values(g.cells ?? {}).some((c) => c?.state === 'mature' && gateText(c, t) !== ''),
  )
  const rows = [...resp.groups].reverse()
  // The column exists only when the server sent coverage for at least one group.
  const showCoverage = resp.groups.some((g) => g.coverage)
  const th: CSSProperties = {
    textAlign: 'left',
    padding: '6px 8px',
    fontSize: 12,
    color: token.colorTextSecondary,
    fontWeight: 500,
    whiteSpace: 'nowrap',
  }
  // Intrinsic grid rows stretch every checkpoint, including wrapped status text and exclusions.
  // Percentage heights inside auto-sized table cells do not reliably fill the row in browsers.
  const row: CSSProperties = {
    display: 'grid',
    gridTemplateColumns: `minmax(140px, 1.2fr) repeat(${resp.checkpoints.length}, minmax(112px, 1fr))${showCoverage ? ' minmax(200px, 1.8fr)' : ''}`,
    columnGap: 4,
    alignItems: 'stretch',
  }
  const td: CSSProperties = { padding: 6, minWidth: 0, display: 'flex', alignItems: 'stretch' }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {t(`forecast.metric.${metric}.help`)}
        {' · '}
        {t(mode === 'custom' ? 'forecast.legend.custom' : 'forecast.legend.monthly')}
      </Typography.Text>
      <ForecastLegend />
      {showsGate && (
        <Typography.Text id={gateHintId} type="secondary" style={{ fontSize: 12 }}>
          {t('forecast.cell.min_n_met_hint')}
        </Typography.Text>
      )}
      <div style={{ overflowX: 'auto', overscrollBehaviorX: 'contain' }}>
        <table role="table" style={{ display: 'block', width: '100%', minWidth: 140 + resp.checkpoints.length * 116 + (showCoverage ? 224 : 0) }}>
          <thead style={{ display: 'block' }}>
            <tr role="row" style={row}>
              <th role="columnheader" scope="col" style={th}>{t(mode === 'month' ? 'forecast.col.month' : 'forecast.col.group')}</th>
              {resp.checkpoints.map((cp) => (
                <th role="columnheader" scope="col" key={cp} style={th}>
                  {t(`forecast.cp.${cp}`)}
                </th>
              ))}
              {showCoverage && <th role="columnheader" scope="col" style={th}>{t('forecast.col.coverage')}</th>}
            </tr>
          </thead>
          <tbody style={{ display: 'grid', rowGap: 4 }}>
            {rows.map((g) => (
              <tr role="row" key={`${g.start}:${g.end}:${g.label}`} style={row}>
                <td style={{ ...td, fontWeight: 600, overflowWrap: 'anywhere' }}>
                  <div style={{ minWidth: 0, display: 'flex', flexDirection: 'column', gap: 4, alignItems: 'flex-start' }}>
                    <span>{g.label}</span>
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
                      {!g.official && (
                        <Tag style={{ margin: 0, whiteSpace: 'normal' }} color="warning">
                          {t('forecast.tag.exploratory')}
                        </Tag>
                      )}
                      {g.bench && (
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {t(`forecast.bench.${g.bench}`)}
                        </Typography.Text>
                      )}
                    </div>
                  </div>
                </td>
                {resp.checkpoints.map((cp) => (
                  <td key={cp} style={td}>
                    {g.cells[cp] ? (
                      <ForecastCellView
                        cell={g.cells[cp]}
                        cp={cp}
                        metric={metric}
                        mode={mode}
                        gateHintId={gateHintId}
                        onOpen={() => onOpen(g, cp)}
                      />
                    ) : null}
                  </td>
                ))}
                {showCoverage && <td style={td}>{coverageBar(g)}</td>}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
