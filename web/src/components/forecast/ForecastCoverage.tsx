import type { CSSProperties } from 'react'
import { Typography, theme } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { ForecastGroup } from '../../api/types'
import { coveragePalette } from '../../lib/forecastPresentation'

// Where a group's reports went. The bar's segments add up to the group's reports and each has its
// own colour (counted, repeat run, missing or rejected, everything else, before the ledger began),
// but colour is never the only carrier: the counted share is text, and the buckets a reader should
// not have to decode from a shade are chips with a name and a count.
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
  if (g.purged_at || Object.values(g.cells ?? {}).every((cell) => cell?.state === 'purged')) {
    return <Typography.Text type="secondary" style={{ fontSize: 12 }}>{t('forecast.cell.purged')}</Typography.Text>
  }

  const total = Math.max(1, c.reports)
  const share = (n: number) => `${(n / total) * 100}%`
  const seg = (name: string, n: number, background: string) => (
    <span key={name} data-seg={name} style={{ display: 'block', height: 8, width: share(n), background }} />
  )
  const other = c.backtest + c.degraded + c.mismatch + c.pending_baseline + c.test + c.off_benchmark + (c.unknown ?? 0)

  const chip = (name: string, n: number, warn: boolean) =>
    n > 0 ? (
      <span
        key={name}
        data-chip={name}
        style={{
          fontSize: 12,
          lineHeight: '18px',
          padding: '0 6px',
          borderRadius: 4,
          border: `1px solid ${warn ? token.colorWarningBorder : token.colorBorderSecondary}`,
          background: warn ? token.colorWarningBg : token.colorFillQuaternary,
          color: warn ? token.colorWarningText : token.colorTextSecondary,
        }}
      >
        {t(`forecast.cov.${name}`, { n })}
      </span>
    ) : null
  const rejected: CSSProperties = { fontSize: 12 }

  return (
    <div style={{ minWidth: 132, maxWidth: 260 }}>
      <div style={{ display: 'flex', gap: 1, borderRadius: 4, overflow: 'hidden', background: token.colorFillQuaternary }}>
        {seg('member', c.member, palette.member)}
        {seg('duplicate', c.duplicate, palette.duplicate)}
        {seg('missing', c.absent + c.rejected, palette.missing)}
        {seg('other', other, palette.other)}
        {seg('pre_live', c.pre_live, palette.preLive)}
      </div>
      <div style={{ fontSize: 12, color: token.colorTextSecondary, marginTop: 3 }}>
        {t('forecast.cov.summary', { member: c.member, reports: c.reports })}
        {c.rejected > 0 && (
          <>
            {' · '}
            {auditLink ? (
              <Link to="/manage/audit?action=predictions.reject">
                <Typography.Text type="warning" style={rejected}>
                  {t('forecast.cov.rejected', { n: c.rejected })}
                </Typography.Text>
              </Link>
            ) : (
              <Typography.Text type="warning" style={rejected}>
                {t('forecast.cov.rejected', { n: c.rejected })}
              </Typography.Text>
            )}
          </>
        )}
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginTop: 3 }}>
        {chip('pre_live', c.pre_live, false)}
        {chip('test', c.test, false)}
        {chip('off_benchmark', c.off_benchmark, true)}
        {chip('pending_baseline', c.pending_baseline, true)}
      </div>
    </div>
  )
}
