import { Table, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ForecastCell, ForecastMode } from '../../api/types'
import { cellDisplay, type Translate } from '../../lib/forecast'
import { formatProbability, formatSigned } from '../../lib/forecastPresentation'

// The numbers behind a mature cell, as the drawer states them: every metric with its null, its p and
// (on the record) its corrected q, then the same metrics per policy version.
//
// Two rules shape it. A count the reader may not know is a dash, never a zero, and the cell's
// publication gate stands in for a count only where the denominator IS the cell's own n (the spread);
// the IC, hit, fill, band and calibration denominators are smaller subsets with no minimum of their
// own. And an exploratory range reports p*, uncorrected for the choice of range, with no q beside it:
// printing a q there would claim a correction the view does not make.

interface Row {
  key: string
  metric: string
  value: string
  nullText: string
  p: string
  q: string
  n: number | string
}

export function metricRows(cell: ForecastCell, t: Translate): Row[] {
  const fmtP = formatProbability
  const sample = (n: number | undefined): number | string => (n !== undefined ? n : '—')
  const cellSample = (n: number | undefined): number | string =>
    n !== undefined ? n : cell.min_n_met ? t('forecast.cell.min_n_met') : '—'
  const rows: Row[] = []
  if (cell.spread_one_arm) {
    // One arm only (everyone long, or no one): there is no spread to state, and saying so beats
    // leaving the row out, which would read as a metric the page forgot.
    rows.push({ key: 'spread', metric: t('forecast.metric.spread'), value: '—', nullText: t('forecast.cell.one_arm'), p: '—', q: '—', n: cellSample(cell.n) })
  } else if (cell.spread_bp !== undefined) {
    rows.push({
      key: 'spread',
      metric: t('forecast.metric.spread'),
      value: `${formatSigned(cell.spread_bp / 100, 1)} pt`,
      nullText: '0',
      p: fmtP(cell.spread_p),
      q: fmtP(cell.spread_q),
      n: cellSample(cell.n),
    })
  }
  if (cell.ic !== undefined) {
    rows.push({
      key: 'ic',
      metric: t('forecast.metric.ic'),
      value: formatSigned(cell.ic, 2),
      nullText: '0',
      p: fmtP(cell.ic_p),
      q: fmtP(cell.ic_q),
      n: sample(cell.ic_n ?? cell.n),
    })
  }
  if (cell.hit !== undefined) {
    rows.push({
      key: 'hit',
      metric: t('forecast.metric.hit'),
      value: `${((cell.hit ?? 0) * 100).toFixed(0)}%`,
      nullText: `${((cell.hit_null ?? 0) * 100).toFixed(0)}%`,
      p: fmtP(cell.hit_p),
      q: fmtP(cell.hit_q),
      n: sample(cell.hit_n ?? cell.n),
    })
  }
  if (cell.fill !== undefined) {
    rows.push({
      key: 'fill',
      metric: t('forecast.metric.fill'),
      value: `${((cell.fill ?? 0) * 100).toFixed(0)}%`,
      nullText: '—',
      p: '—',
      q: '—',
      n: sample(cell.n),
    })
    if (cell.fill_ret_bp !== undefined) {
      rows.push({
        key: 'after_fill',
        metric: t('forecast.drawer.after_fill'),
        value: `${formatSigned(cell.fill_ret_bp / 100, 1)}%`,
        nullText: '—',
        p: '—',
        q: '—',
        n: '—',
      })
    }
  }
  if (cell.band !== undefined) {
    rows.push({ key: 'band', metric: t('forecast.metric.band'), value: `${((cell.band ?? 0) * 100).toFixed(0)}%`, nullText: '—', p: '—', q: '—', n: sample(cell.band_n) })
  }
  if (cell.calib !== undefined) {
    rows.push({ key: 'calib', metric: t('forecast.metric.calib'), value: ((cell.calib ?? 0) * 100).toFixed(0) + '%', nullText: '—', p: '—', q: '—', n: sample(cell.calib_n) })
  }
  return rows
}

export function MetricsTable({ cell, mode }: { cell: ForecastCell; mode: ForecastMode }) {
  const { t } = useTranslation()
  const custom = mode === 'custom'
  return (
    <Table
      size="small"
      rowKey="key"
      pagination={false}
      dataSource={metricRows(cell, t)}
      scroll={{ x: 'max-content' }}
      columns={[
        { title: t('forecast.drawer.metric'), dataIndex: 'metric', key: 'metric' },
        { title: t('forecast.drawer.value'), dataIndex: 'value', key: 'value' },
        { title: t('forecast.drawer.null'), dataIndex: 'nullText', key: 'nullText' },
        { title: custom ? t('forecast.drawer.p_star') : t('forecast.drawer.p'), dataIndex: 'p', key: 'p' },
        ...(custom ? [] : [{ title: t('forecast.drawer.q'), dataIndex: 'q', key: 'q' }]),
        { title: t('forecast.drawer.n'), dataIndex: 'n', key: 'n' },
      ]}
    />
  )
}

// A cell that mixes policy versions also carries each version's own result, with its own gate, so a
// reader can see whether a headline number is one policy's or a blend.
export function SlicesTable({ cell, mode }: { cell: ForecastCell; mode: ForecastMode }) {
  const { t } = useTranslation()
  const slices = cell.slices ?? []
  if (slices.length === 0) return null
  const metric = (slice: ForecastCell, name: 'spread' | 'ic' | 'hit') => {
    const d = slice.state === 'mature' ? cellDisplay(slice, name, mode, t) : null
    return d ? d.value : '—'
  }
  const sampleOf = (slice: ForecastCell): string => {
    if (slice.state !== 'mature') return slice.n !== undefined ? t('forecast.cell.small', { n: slice.n }) : t('forecast.cell.small_nocount')
    if (slice.n !== undefined) return t('forecast.cell.n', { n: slice.n })
    return slice.min_n_met ? t('forecast.cell.min_n_met') : '—'
  }
  return (
    <div>
      <Typography.Title level={5} style={{ marginTop: 0 }}>
        {t('forecast.drawer.slices')}
      </Typography.Title>
      <Table
        size="small"
        rowKey="policy_version"
        pagination={false}
        dataSource={slices}
        scroll={{ x: 'max-content' }}
        columns={[
          { title: t('forecast.drawer.policy'), dataIndex: 'policy_version', key: 'policy' },
          { title: t('forecast.metric.spread'), key: 'spread', render: (_: unknown, s: ForecastCell) => metric(s, 'spread') },
          { title: t('forecast.metric.ic'), key: 'ic', render: (_: unknown, s: ForecastCell) => metric(s, 'ic') },
          { title: t('forecast.metric.hit'), key: 'hit', render: (_: unknown, s: ForecastCell) => metric(s, 'hit') },
          { title: t('forecast.drawer.n'), key: 'n', render: (_: unknown, s: ForecastCell) => sampleOf(s) },
        ]}
      />
    </div>
  )
}
