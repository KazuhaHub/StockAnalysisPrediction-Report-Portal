import { useId } from 'react'
import { Button, DatePicker, Segmented, Select, Tag, theme } from 'antd'
import { CheckOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import type { ForecastGrouping, ForecastMetric, ForecastMode } from '../../api/types'
import { MONTH_WINDOW, rangePresets, type BenchRun, type RangeLimits } from '../../lib/forecast'
import { forecastLinkColor, forecastPressedPalette } from '../../lib/forecastPresentation'
import Quiet from './Quiet'

// The controls under the tabs: which report type, which benchmark the cohorts were scored against,
// which months or dates, which metric. Each one says what it is without being opened, and they wrap
// rather than overflow a phone.
//
// The custom range is two single-date pickers and a row of quick-range buttons, not one range picker.
// A range picker's popup holds two month panels, and a presets column beside them made it 748 px: a
// phone showed one panel and hid the rest off-screen to the left, and a tablet scrolled the page
// sideways while it was open. A single-date popup is 288 px at any width, and buttons wrap like every
// other control in this row. (The demo also uses two date inputs.)
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

const DATE = 'YYYY-MM-DD'
const label = { fontSize: 13 } as const
const dateInput = { width: 124 } as const
// The popup is 288 px. By default it flips to the input's other edge when it would overflow, which on
// a phone puts it off the left of the screen (a 124 px input cannot hold a 288 px popup on its right
// edge either); shifting keeps it inside the screen whichever side the input is on.
const popupAlign = { overflow: { adjustX: false, adjustY: true, shiftX: true } } as const
const item = { display: 'inline-flex', alignItems: 'center', flexWrap: 'wrap', gap: 6, minWidth: 0 } as const

export default function ForecastToolbar(p: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const link = { padding: 0, height: 'auto', color: forecastLinkColor(token) } as const
  const types = p.rtypes ?? []
  // A type named by the link is shown even when the server did not list it (it is the one asked for).
  const shownType = p.rtype || (types.length === 1 ? types[0] : '')

  const pressedLook = forecastPressedPalette(token)
  const rangeLabel = useId()
  const presets = rangePresets(p.today, p.ledgerStart, p.limits)
  const date = (value: string) => (value ? dayjs(value) : null)

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
          {/* One button whose label flips, not two that replace each other: the reader has just
              pressed it, and an element that unmounts under the focus hands the focus to the page. */}
          {(p.hidden > 0 || (p.showOlder && p.total > MONTH_WINDOW)) && (
            <Button type="link" size="small" onClick={() => p.onChange({ older: p.showOlder ? '' : '1' })} style={link}>
              {p.showOlder ? t('forecast.range.show_recent') : t('forecast.range.show_older', { n: p.hidden })}
            </Button>
          )}
        </span>
      )}

      {p.mode === 'custom' && (
        <>
          <span data-part="range" role="group" aria-labelledby={rangeLabel} style={item}>
            <Quiet id={rangeLabel} style={label}>{t('forecast.range.custom')}</Quiet>
            <DatePicker
              size="small"
              aria-label={t('forecast.range.from')}
              placeholder={t('forecast.range.from')}
              format={DATE}
              popupAlign={popupAlign}
              value={date(p.from)}
              onChange={(v) => p.onChange({ from: v?.format(DATE) ?? '' })}
              style={dateInput}
            />
            <span aria-hidden="true">–</span>
            <DatePicker
              size="small"
              aria-label={t('forecast.range.to')}
              placeholder={t('forecast.range.to')}
              format={DATE}
              popupAlign={popupAlign}
              value={date(p.to)}
              onChange={(v) => p.onChange({ to: v?.format(DATE) ?? '' })}
              style={dateInput}
            />
          </span>
          {presets.length > 0 && (
            <span data-part="presets" role="group" aria-label={t('forecast.range.presets')} style={item}>
              {presets.map((preset) => {
                // The range in force is drawn as selected, not only exposed as pressed: a border and a
                // check in the page's AA link colour and a tint, so the state is not carried by colour
                // alone. The label stays in the body text colour, which is the one that clears AA on
                // the tint in both themes.
                const pressed = preset.from === p.from && preset.to === p.to
                return (
                  <Button
                    key={preset.key}
                    size="small"
                    aria-pressed={pressed}
                    icon={pressed ? <CheckOutlined aria-hidden="true" style={{ color: pressedLook.icon }} /> : undefined}
                    style={pressed ? { borderColor: pressedLook.border, color: pressedLook.text, background: pressedLook.background } : undefined}
                    onClick={() => p.onChange({ from: preset.from, to: preset.to })}
                  >
                    {t(`forecast.range.preset.${preset.key}`)}
                  </Button>
                )
              })}
            </span>
          )}
          <span style={item}>
            <Quiet style={label}>{t('forecast.range.group')}</Quiet>
            <Segmented
              size="small"
              aria-label={t('forecast.range.group')}
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
            aria-label={t('forecast.toolbar.metric')}
            value={p.metric}
            onChange={(v) => p.onChange({ metric: v as ForecastMetric })}
            options={METRICS.map((m) => ({ value: m, label: t(`forecast.metric.${m}`) }))}
          />
        </span>
      </span>
    </div>
  )
}
