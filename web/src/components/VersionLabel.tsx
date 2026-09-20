import type { CSSProperties } from 'react'
import { Button, Tooltip, Typography, theme } from 'antd'
import { useTranslation } from 'react-i18next'
import { formatBuildTime, pageBuildIdentity, type BuildIdentity } from '../lib/buildIdentity'
import { productVersionLabel } from '../lib/productVersionLabel'
import { useUpdate } from './UpdateProvider'

// The product version, shown in the footer and the management rail, and the way into that build's
// release notes.
//
// It shows the build this PAGE is running, taken from the bundle's own compile-time identity — not
// from /api/version, whose first answer describes the server and may well describe a build the
// browser has never loaded. When the two disagree the disagreement is the point, and it is the
// update prompt's job to say so; this label keeps telling the truth about the document you are
// reading.
//
// It is a button, not a tooltip: "view the release notes" is an action, and an action reachable only
// by hovering a span is unreachable by keyboard and invisible on touch.

export default function VersionLabel({ size = 12, style }: { size?: number; style?: CSSProperties }) {
  const { t, i18n } = useTranslation()
  const { token } = theme.useToken()
  const ctx = useUpdate()
  // Outside a provider the label still names the real build — the identity comes from the bundle,
  // not from the coordinator; only the dialog it opens lives there.
  const build: BuildIdentity = ctx?.state.page ?? pageBuildIdentity()
  const label = t('version.label', { version: productVersionLabel(build.version) })

  const tooltip = (
    <div style={{ lineHeight: 1.6, fontWeight: 600 }}>
      {/* The label carries its own colon in the locale files, because a half-width ":" reads wrong in
          Chinese beside a full-width label. */}
      <div>
        {t('version.tip.version')} {productVersionLabel(build.version)}
      </div>
      <div>
        {t('version.tip.commit')} {build.commit}
      </div>
      <div>
        {t('version.tip.built')} {formatBuildTime(build.buildDate, i18n.language)}
      </div>
    </div>
  )

  // Outside a provider — a component test, or any surface mounted without the coordinator — the
  // label still reads correctly and simply has no dialog to open.
  if (!ctx) {
    return (
      <Typography.Text type="secondary" style={{ fontSize: size, fontVariantNumeric: 'tabular-nums', ...style }}>
        {label}
      </Typography.Text>
    )
  }

  return (
    <Tooltip title={tooltip} trigger={['hover', 'focus']}>
      <Button
        type="text"
        size="small"
        className="rp-version-label"
        onClick={() => ctx.openNotes(ctx.state.page)}
        style={{
          height: 'auto',
          padding: 0,
          fontSize: size,
          fontVariantNumeric: 'tabular-nums',
          color: token.colorTextTertiary,
          ...style,
        }}
      >
        {label}
      </Button>
    </Tooltip>
  )
}
