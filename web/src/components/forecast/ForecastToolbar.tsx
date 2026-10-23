import { Button, DatePicker, Segmented, Select, Tag, Typography, theme } from 'antd'
import { useTranslation } from 'react-i18next'
import dayjs, { type Dayjs } from 'dayjs'
import type { ForecastGrouping, ForecastMetric, ForecastMode } from '../../api/types'
import { MONTH_WINDOW, rangePresets, type BenchRun, type RangeLimits } from '../../lib/forecast'
import { forecastLinkColor } from '../../lib/forecastPresentation'
import Quiet from './Quiet'

// The controls under the tabs: which report type, which benchmark the cohorts were scored against,
// which months or dates, which metric. Each one says what it is without being opened, and they wrap
// rather than overflow a phone.
//
// Nothing here fetches. The page owns the view (it is in the URL) and the toolbar reports changes.

const GROUPS = ['week', 'month', 'quarter', 'all'] as const
const METRICS = ['spread', 'ic', 'hit', 'fill'] as const

interface Props {
  mode: ForecastMode
  metric: ForecastMetric
  /** The covered report types the server named, or undefined when it named none (withheld). */
  rtypes?: string[]
  /** The report type the view asks for; '' is the server's default. */
  rtype: string
  runs: BenchRun[]
  /** Months in the record, and how many of them the monthly view folds away. */
  total: number
  hidden: number
  showOlder: boolean
  // Custom range.
  from: string
  to: string
  group: ForecastGrouping
  limits?: RangeLimits
  today: string
  ledgerStart?: string
  onChange: (next: Record<string, string>) => void
}

const label = { fontSize: 13 } as const
const item = { display: 'inline-flex', alignItems: 'center', flexWrap: 'wrap', gap: 6, minWidth: 0 } as const

export default function ForecastToolbar(p: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const link = { padding: 0, height: 'auto', color: forecastLinkColor(token) } as const
  const types = p.rtypes ?? []
  // A type named by the link is shown even when the server did not list it (it is the one asked for).
  const shownType = p.rtype || (types.length === 1 ? types[0] : '')

  const rangeValue: [Dayjs | null, Dayjs | null] | null = p.from && p.to ? [dayjs(p.from), dayjs(p.to)] : null
  const presets = rangePresets(p.today, p.ledgerStart, p.limits).map((preset) => ({
    label: t(`forecast.range.preset.${preset.key}`),
    value: [dayjs(preset.from), dayjs(preset.to)] as [Dayjs, Dayjs],
  }))

  return (
    <div data-part="toolbar" style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 20px', margin: '12px 0' }}>
      {(types.length > 1 || shownType) && (
        <span data-part="rtype" style={item}>
          <Quiet style={label}>{t('forecast.toolbar.rtype')}</Quiet>
          {types.length > 1 ? (
            <Select
              size="small"
              aria-label={t('forecast.toolbar.rtype')}
              value={p.rtype || undefined}
              options={types.map((value) => ({ value, label: value }))}
              onChange={(value) => p.onChange({ rtype: value })}
              style={{ minWidth: 140, maxWidth: '100%' }}
            />
          ) : (
            <Tag style={{ margin: 0 }}>{shownType}</Tag>
          )}
        </span>
      )}

      {p.runs.length > 0 && (
        <span data-part="bench" style={item}>
          <Quiet style={label}>{t('forecast.toolbar.bench')}</Quiet>
          <Tag style={{ margin: 0 }}>{t('forecast.toolbar.bench_frozen')}</Tag>
          <Quiet style={{ fontSize: 12 }}>
            {p.runs
              .map((run) =>
                t('forecast.toolbar.bench_run', {
                  span: run.from === run.to ? run.from : `${run.from} – ${run.to}`,
                  bench: t(`forecast.bench.${run.bench}`),
                }),
              )
              .join(' · ')}
          </Quiet>
        </span>
      )}

      {p.mode === 'month' && p.total > 0 && (
        <span data-part="range" style={item}>
          <Quiet style={label}>{t('forecast.range.label')}</Quiet>
          <Tag style={{ margin: 0 }}>
            {p.hidden > 0 ? t('forecast.range.last_n', { n: MONTH_WINDOW }) : t('forecast.range.all')}
          </Tag>
          {p.hidden > 0 && (
            <Button type="link" size="small" onClick={() => p.onChange({ older: '1' })} style={link}>
              {t('forecast.range.show_older', { n: p.hidden })}
            </Button>
          )}
          {p.showOlder && p.total > MONTH_WINDOW && (
            <Button type="link" size="small" onClick={() => p.onChange({ older: '' })} style={link}>
              {t('forecast.range.show_recent')}
            </Button>
          )}
        </span>
      )}

      {p.mode === 'custom' && (
        <>
          <span data-part="range" style={item}>
            <Quiet style={label}>{t('forecast.range.custom')}</Quiet>
            <DatePicker.RangePicker
              size="small"
              value={rangeValue}
              presets={presets}
              allowEmpty={[true, true]}
              onChange={(v) => p.onChange({ from: v?.[0]?.format('YYYY-MM-DD') ?? '', to: v?.[1]?.format('YYYY-MM-DD') ?? '' })}
              style={{ maxWidth: '100%' }}
            />
          </span>
          <span style={item}>
            <Quiet style={label}>{t('forecast.range.group')}</Quiet>
            <Segmented
              size="small"
              value={p.group}
              onChange={(v) => p.onChange({ group: v as ForecastGrouping })}
              options={GROUPS.map((g) => ({ value: g, label: t(`forecast.group.${g}`) }))}
            />
          </span>
          {p.limits && (
            <Quiet style={{ fontSize: 12, flexBasis: '100%' }}>
              {t('forecast.range.limits', { months: p.limits.maxMonths, weeks: p.limits.maxWeekGroups })}
            </Quiet>
          )}
        </>
      )}

      {/* Four long labels do not fit a phone: scroll inside the card instead of hanging out of it. */}
      <span style={{ ...item, maxWidth: '100%' }}>
        <Quiet style={label}>{t('forecast.toolbar.metric')}</Quiet>
        <span style={{ maxWidth: '100%', overflowX: 'auto' }}>
          <Segmented
            size="small"
            value={p.metric}
            onChange={(v) => p.onChange({ metric: v as ForecastMetric })}
            options={METRICS.map((m) => ({ value: m, label: t(`forecast.metric.${m}`) }))}
          />
        </span>
      </span>
    </div>
  )
}
