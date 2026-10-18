import { toCSV } from './csv'
import type {
  ForecastCell,
  ForecastCheckpoint,
  ForecastGroupsResponse,
  ForecastMetric,
  ForecastMode,
  ForecastGrouping,
} from '../api/types'

// The matrix's presentation rules, kept out of the component so the CSV export and the table cannot
// disagree about what a cell means.
//
// THE ONE RULE THAT MATTERS: metrics are read only when the cell is mature. A payload that wrongly
// carried a number on a pending cell must still render as pending, because a number beside "已到期
// 31/52" reads as a measured result over half a cohort.

export interface CellDisplay {
  value: string
  sub: string
  /** -1 / 0 / +1, and nonzero only when the corrected q passes 0.05. */
  signal: -1 | 0 | 1
}

function pct(v: number, digits = 0): string {
  return `${(v * 100).toFixed(digits)}%`
}

function num(v: number, digits: number): string {
  return v.toFixed(digits)
}

/**
 * The corrected value a cell shows. Monthly is the record, so it shows q (the BH-adjusted p).
 * Exploratory shows p*, because its q would still not account for the range having been chosen,
 * and printing a q there would claim a correction the view does not make.
 */
export function correctedText(p: number | undefined, q: number | undefined, mode: ForecastMode): string {
  const fmt = (v: number) => (v < 0.001 ? '<0.001' : v.toFixed(3))
  if (mode === 'custom') return p === undefined ? '—' : `p*=${fmt(p)}`
  const v = q ?? p
  return v === undefined ? '—' : `q=${fmt(v)}`
}

export type Translate = (key: string, opts?: Record<string, unknown>) => string

export function cellDisplay(
  cell: ForecastCell,
  metric: ForecastMetric,
  mode: ForecastMode,
  t: Translate,
): CellDisplay | null {
  if (cell.state !== 'mature') return null
  switch (metric) {
    case 'spread': {
      if (cell.spread_bp === undefined) {
        return { value: '—', sub: t('forecast.cell.one_arm'), signal: 0 }
      }
      const sig: -1 | 0 | 1 =
        cell.spread_q !== undefined && cell.spread_q < 0.05 ? (cell.spread_bp > 0 ? 1 : cell.spread_bp < 0 ? -1 : 0) : 0
      return {
        value: `${num(cell.spread_bp / 100, 1)} pt`,
        sub: `${correctedText(cell.spread_p, cell.spread_q, mode)} · ${t('forecast.cell.n', { n: cell.n ?? 0 })}`,
        signal: sig,
      }
    }
    case 'ic': {
      if (cell.ic === undefined) return { value: '—', sub: t('forecast.cell.n', { n: cell.n ?? 0 }), signal: 0 }
      const sig: -1 | 0 | 1 =
        cell.ic_q !== undefined && cell.ic_q < 0.05 ? (cell.ic > 0 ? 1 : cell.ic < 0 ? -1 : 0) : 0
      return {
        value: num(cell.ic, 2),
        sub: `${correctedText(cell.ic_p, cell.ic_q, mode)} · ${t('forecast.cell.n', { n: cell.ic_n ?? cell.n ?? 0 })}`,
        signal: sig,
      }
    }
    case 'hit': {
      if (cell.hit === undefined) return { value: '—', sub: t('forecast.cell.n', { n: cell.n ?? 0 }), signal: 0 }
      const sig: -1 | 0 | 1 =
        cell.hit_q !== undefined && cell.hit_q < 0.05
          ? cell.hit > (cell.hit_null ?? 0.5)
            ? 1
            : cell.hit < (cell.hit_null ?? 0.5)
              ? -1
              : 0
          : 0
      return {
        value: pct(cell.hit),
        sub: `${correctedText(cell.hit_p, cell.hit_q, mode)} · ${t('forecast.cell.base_rate', { pct: pct(cell.hit_null ?? 0) })} · ${t('forecast.cell.n', { n: cell.hit_n ?? cell.n ?? 0 })}`,
        signal: sig,
      }
    }
    case 'fill': {
      if (cell.fill === undefined) return { value: '—', sub: t('forecast.cell.n', { n: cell.n ?? 0 }), signal: 0 }
      return {
        value: pct(cell.fill),
        sub: `${t('forecast.cell.after_fill', { pct: num((cell.fill_ret_bp ?? 0) / 100, 1) })} · ${t('forecast.cell.n', { n: cell.n ?? 0 })}`,
        signal: 0,
      }
    }
  }
}

export function checkpointKey(cp: ForecastCheckpoint): string {
  return `forecast.cp.${cp}`
}

/**
 * forecastCsv writes the matrix as CSV, one row per group x checkpoint x metric.
 *
 * The comment lines are not decoration: an export of an exploratory range must never be mistakable
 * for the official record, so mode, official and the chosen range travel with the file itself. A
 * spreadsheet loses everything else about where the numbers came from.
 */
export function forecastCsv(resp: ForecastGroupsResponse, generatedAt: string): string {
  const lines = [
    `# mode=${resp.mode}`,
    `# official=${resp.official}`,
    `# range=${resp.range.from}..${resp.range.to}`,
    `# group=${resp.range.group ?? ''}`,
    `# checkpoints=${resp.checkpoints.join('|')}`,
    `# unrecorded=${resp.unrecorded}`,
    `# generated_at=${generatedAt}`,
  ]
  const header = ['group', 'start', 'end', 'checkpoint', 'state', 'metric', 'value', 'null', 'p', 'q', 'n']
  const rows: (string | number)[][] = []
  for (const g of resp.groups) {
    for (const cp of resp.checkpoints) {
      const c = g.cells[cp]
      if (!c) continue
      if (c.state !== 'mature') {
        // One row for the state, so a reader sees why the cell is blank rather than a missing row.
        rows.push([g.label, g.start, g.end, cp, c.state, '', '', '', '', '', c.n ?? c.total ?? ''])
        continue
      }
      const base = [g.label, g.start, g.end, cp, c.state]
      const n = c.n ?? ''
      if (c.spread_bp !== undefined) {
        rows.push([...base, 'spread_bp', c.spread_bp, 0, c.spread_p ?? '', c.spread_q ?? '', n])
      }
      if (c.ic !== undefined) {
        rows.push([...base, 'ic', c.ic, 0, c.ic_p ?? '', c.ic_q ?? '', c.ic_n ?? n])
      }
      if (c.hit !== undefined) {
        rows.push([...base, 'hit', c.hit, c.hit_null ?? '', c.hit_p ?? '', c.hit_q ?? '', c.hit_n ?? n])
      }
      if (c.fill !== undefined) {
        rows.push([...base, 'fill', c.fill, '', '', '', n])
        if (c.fill_ret_bp !== undefined) {
          rows.push([...base, 'fill_ret_bp', c.fill_ret_bp, '', '', '', n])
        }
      }
      if (c.band !== undefined) rows.push([...base, 'band', c.band, '', '', '', c.band_n ?? n])
      if (c.calib !== undefined) rows.push([...base, 'calib', c.calib, '', '', '', c.calib_n ?? n])
    }
  }
  return `${lines.join('\n')}\n${toCSV(header, rows)}\n`
}

/** The query string a matrix request uses. Kept in one place so a link and a fetch agree. */
export function groupsQuery(mode: ForecastMode, from: string, to: string, group: ForecastGrouping): string {
  const p = new URLSearchParams({ mode })
  if (mode === 'custom') {
    p.set('from', from)
    p.set('to', to)
    p.set('group', group)
  }
  return p.toString()
}

/** Read the view out of the URL, with the monthly record as the default. */
export function readView(sp: URLSearchParams): {
  mode: ForecastMode
  from: string
  to: string
  group: ForecastGrouping
  metric: ForecastMetric
} {
  const mode: ForecastMode = sp.get('mode') === 'custom' ? 'custom' : 'month'
  const group = sp.get('group')
  const metric = sp.get('metric')
  return {
    mode,
    from: sp.get('from') ?? '',
    to: sp.get('to') ?? '',
    group: group === 'week' || group === 'quarter' || group === 'all' ? group : 'month',
    metric: metric === 'ic' || metric === 'hit' || metric === 'fill' ? metric : 'spread',
  }
}
