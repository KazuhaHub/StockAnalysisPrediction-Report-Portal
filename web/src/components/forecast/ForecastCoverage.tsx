import type { CSSProperties } from 'react'
import { theme } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { ForecastGroup } from '../../api/types'
import { coveragePalette, forecastWarningText } from '../../lib/forecastPresentation'
import Quiet from './Quiet'

// Where a group's reports went. The bar's segments add up to the group's reports and each has its
// own colour (counted, repeat run, missing or rejected, everything else, before the ledger began),
// but colour is never the only carrier: the counted share is text, the buckets a reader should not
// have to decode from a shade are chips with a name and a count, and the bar itself is an image whose
// text alternative names every non-empty segment with its count (the segments are empty spans).
//
// A reader who may not know the ledger's volume has no coverage at all (the server sends none), so
// there is nothing here for them: no bar, no chip, no number.

interface Props {
  group: ForecastGroup
  /** Whether the rejected count links to the audit log, which is an administrator page. */
  auditLink: boolean
}

export default function ForecastCoverage({ group: g, auditLink }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const palette = coveragePalette(token)
  const c = g.coverage
  if (!c) return null

  // A purged group has no reports left to account for: a bar at 0 of 0 would say the group was empty.
  const cells = Object.values(g.cells ?? {})
  if (g.purged_at || (cells.length > 0 && cells.every((cell) => cell?.state === 'purged'))) {
    return <Quiet style={{ fontSize: 12 }}>{t('forecast.cell.purged')}</Quiet>
  }

  const total = Math.max(1, c.reports)
  const share = (n: number) => `${(n / total) * 100}%`
  const other = c.backtest + c.degraded + c.mismatch + c.pending_baseline + c.test + c.off_benchmark + (c.unknown ?? 0)
  const families = [
    ['member', c.member, palette.member],
    ['duplicate', c.duplicate, palette.duplicate],
    ['missing', c.absent + c.rejected, palette.missing],
    ['other', other, palette.other],
    ['pre_live', c.pre_live, palette.preLive],
  ] as const
  const barLabel = t('forecast.cov.bar', {
    parts: families.filter(([, n]) => n > 0).map(([name, n]) => `${t(`forecast.legend.cov.${name}`)} ${n}`).join(' · '),
  })

  const chip = (name: string, n: number, warn: boolean) =>
    n > 0 ? (
      <span
        key={name}
        data-chip={name}
        style={{
          fontSize: 12,
          lineHeight: '18px',
          padding: '0 6px',
          borderRadius: token.borderRadiusSM,
          border: `1px solid ${warn ? token.colorWarningBorder : token.colorBorderSecondary}`,
          background: warn ? token.colorWarningBg : token.colorFillQuaternary,
          color: warn ? forecastWarningText(token) : token.colorTextSecondary,
        }}
      >
        {t(`forecast.cov.${name}`, { n })}
      </span>
    ) : null
  const rejected: CSSProperties = { fontSize: 12, color: forecastWarningText(token) }

  return (
    <div style={{ minWidth: 132, maxWidth: 260 }}>
      <div role="img" aria-label={barLabel} style={{ display: 'flex', gap: 1, borderRadius: token.borderRadiusSM, overflow: 'hidden', background: token.colorFillQuaternary }}>
        {families.map(([name, n, background]) => (
          <span key={name} data-seg={name} style={{ display: 'block', height: 8, width: share(n), background }} />
        ))}
      </div>
      <div style={{ fontSize: 12, color: token.colorTextSecondary, marginTop: 3 }}>
        {t('forecast.cov.summary', { member: c.member, reports: c.reports })}
        {c.rejected > 0 && (
          <>
            {' · '}
            {auditLink ? (
              <Link to="/manage/audit?action=predictions.reject">
                <span style={rejected}>{t('forecast.cov.rejected', { n: c.rejected })}</span>
              </Link>
            ) : (
              <span style={rejected}>{t('forecast.cov.rejected', { n: c.rejected })}</span>
            )}
          </>
        )}
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginTop: 3 }}>
        {chip('duplicate', c.duplicate, false)}
        {chip('absent', c.absent, true)}
        {chip('pre_live', c.pre_live, false)}
        {chip('test', c.test, false)}
        {chip('off_benchmark', c.off_benchmark, true)}
        {chip('pending_baseline', c.pending_baseline, true)}
      </div>
    </div>
  )
}
