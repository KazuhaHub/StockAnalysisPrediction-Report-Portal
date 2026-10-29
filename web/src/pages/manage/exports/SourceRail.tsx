import { Button, Select, Typography, theme } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ExportSource } from '../../../api/types'
import type { ExportLabels } from './labels'

// The data-source list on the left, with each source's unfiltered row count. On a phone the rail
// collapses to a select so it does not push the results below the fold.
export default function SourceRail({
  sources,
  value,
  onChange,
  labels,
  compact,
}: {
  sources: ExportSource[]
  value: string
  onChange: (key: string) => void
  labels: ExportLabels
  compact: boolean
}) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  if (compact) {
    return (
      <Select
        style={{ width: '100%' }}
        aria-label={t('export.sources')}
        value={value}
        onChange={onChange}
        options={sources.map((s) => ({ value: s.key, label: `${labels.source(s.key)} (${s.total.toLocaleString()})` }))}
      />
    )
  }
  return (
    <nav aria-label={t('export.sources')} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Typography.Text type="secondary" style={{ fontSize: 12, textTransform: 'uppercase', letterSpacing: 0.4, padding: '0 8px 4px' }}>
        {t('export.sources')}
      </Typography.Text>
      {sources.map((s) => {
        const on = s.key === value
        return (
          <Button
            key={s.key}
            type="text"
            aria-current={on ? 'true' : undefined}
            onClick={() => onChange(s.key)}
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              height: 36,
              background: on ? token.colorPrimaryBg : undefined,
              color: on ? token.colorPrimary : undefined,
              fontWeight: on ? 600 : 400,
            }}
          >
            <span>{labels.source(s.key)}</span>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {s.total.toLocaleString()}
            </Typography.Text>
          </Button>
        )
      })}
    </nav>
  )
}
