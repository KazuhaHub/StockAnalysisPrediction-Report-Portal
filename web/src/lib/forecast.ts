import { toCSV } from './csv'
import { formatProbability } from './forecastPresentation'
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
  /**
   * The cell's publication-gate assurance ("n >= 30"), or '' when there is nothing to assure. It is
   * a property of the CELL, drawn once beside the metric and never inside a metric's own sample
   * slot: the metric's denominator is a smaller subset with no minimum of its own.
   */
  gate: string
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
  const fmt = formatProbability
  if (mode === 'custom') return p === undefined ? '—' : `p*=${fmt(p)}`
  const v = q ?? p
  return v === undefined ? '—' : `q=${fmt(v)}`
}

export type Translate = (key: string, opts?: Record<string, unknown>) => string

/**
 * The sample size a metric states, when the server sent one. A count the reader may not know is
 * never replaced by anything here: nothing is said, so a missing count is neither a measured zero
 * nor a borrowed claim about some other denominator.
 */
export function sampleText(n: number | undefined, t: Translate): string {
  return n === undefined ? '' : t('forecast.cell.n', { n })
}

/**
 * The assurance a reader who may not know the ledger's volume gets in place of the cell's count:
 * the cell cleared the publication gate (n >= 30). It is said only where the count is absent, and
 * once per cell, because it is a fact about the cell's member count. The IC, hit, band and
 * calibration denominators are smaller subsets that carry no such minimum.
 */
export function gateText(cell: ForecastCell, t: Translate): string {
  return cell.n === undefined && cell.min_n_met ? t('forecast.cell.min_n_met') : ''
}

const join = (...parts: string[]) => parts.filter((p) => p !== '').join(' · ')

export function cellDisplay(
  cell: ForecastCell,
  metric: ForecastMetric,
  mode: ForecastMode,
  t: Translate,
): CellDisplay | null {
  if (cell.state !== 'mature') return null
  const gate = gateText(cell, t)
  switch (metric) {
    case 'spread': {
      if (cell.spread_bp === undefined) {
        return { value: '—', sub: t('forecast.cell.one_arm'), gate, signal: 0 }
      }
      const sig: -1 | 0 | 1 =
        cell.spread_q !== undefined && cell.spread_q < 0.05 ? (cell.spread_bp > 0 ? 1 : cell.spread_bp < 0 ? -1 : 0) : 0
      return {
        value: `${num(cell.spread_bp / 100, 1)} pt`,
        sub: join(correctedText(cell.spread_p, cell.spread_q, mode), sampleText(cell.n, t)),
        gate,
        signal: sig,
      }
    }
    case 'ic': {
      if (cell.ic === undefined) return { value: '—', sub: sampleText(cell.n, t), gate, signal: 0 }
      const sig: -1 | 0 | 1 =
        cell.ic_q !== undefined && cell.ic_q < 0.05 ? (cell.ic > 0 ? 1 : cell.ic < 0 ? -1 : 0) : 0
      return {
        value: num(cell.ic, 2),
        sub: join(correctedText(cell.ic_p, cell.ic_q, mode), sampleText(cell.ic_n ?? cell.n, t)),
        gate,
        signal: sig,
      }
    }
    case 'hit': {
      if (cell.hit === undefined) return { value: '—', sub: sampleText(cell.n, t), gate, signal: 0 }
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
        sub: join(
          correctedText(cell.hit_p, cell.hit_q, mode),
          t('forecast.cell.base_rate', { pct: pct(cell.hit_null ?? 0) }),
          sampleText(cell.hit_n ?? cell.n, t),
        ),
        gate,
        signal: sig,
      }
    }
    case 'fill': {
      if (cell.fill === undefined) return { value: '—', sub: sampleText(cell.n, t), gate, signal: 0 }
      return {
        value: pct(cell.fill),
        sub: join(t('forecast.cell.after_fill', { pct: num((cell.fill_ret_bp ?? 0) / 100, 1) }), sampleText(cell.n, t)),
        gate,
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
 * spreadsheet loses everything else about where the numbers came from. For the same reason every
 * reader row repeats mode, official and the benchmark, and the header carries the disclaimer, the
 * population the numbers cover and each group's benchmark.
 *
 * Volume follows the response. A reader who may not know the ledger's volume (an external reader
 * with the all scope, or any response the server sent without coverage) gets no sample-size or
 * coverage column at all, only a publication-gate flag on mature cells. Everyone else keeps the
 * sample size and the coverage of the population they are reading.
 */
export interface ForecastCsvReader {
  population: string
  disclaimer: string
  /** External all readers must never export sample-size/count columns. */
  hideCounts: boolean
}

export function forecastCsv(resp: ForecastGroupsResponse, generatedAt: string, reader?: ForecastCsvReader): string {
  const line = (value: string) => value.replace(/[\r\n]/g, ' ')
  const lines = [
    `# mode=${resp.mode}`,
    `# official=${resp.official}`,
    `# range=${resp.range.from}..${resp.range.to}`,
    `# group=${resp.range.group ?? ''}`,
    `# checkpoints=${resp.checkpoints.join('|')}`,
    ...(!reader ? [`# unrecorded=${resp.unrecorded ?? ''}`] : []),
    `# generated_at=${generatedAt}`,
  ]
  // Do not trust absence alone, and do not trust the caller alone: either one withholds volume.
  const volumeHidden = !!reader && (reader.hideCounts || resp.groups.some((g) => !g.coverage))
  if (reader) {
    lines.push(`# disclaimer=${line(reader.disclaimer)}`, `# population=${line(reader.population)}`)
    for (const group of resp.groups) lines.push(`# benchmark=${line(group.label)}:${line(group.bench)}`)
    // The flag is a fact about the cell, repeated on each of its metric rows. Say so in the file:
    // a hit rate or an IC rests on a smaller subset that has no minimum of its own.
    if (volumeHidden) lines.push('# publication_gate=cell n >= 30; a metric may rest on a smaller subset')
  }
  const header: string[] = ['group', 'start', 'end', 'checkpoint', 'state', 'metric', 'value', 'null', 'p', 'q',
    volumeHidden ? 'publication_gate' : 'n']
  if (reader && !volumeHidden) header.push('coverage_reports', 'coverage_member')
  if (reader) header.push('mode', 'official', 'bench')

  const rows: (string | number)[][] = []
  for (const g of resp.groups) {
    // What follows the ten fixed columns: the sample size (or the gate flag), the coverage of the
    // group's population, then the labels that keep a row from being mistaken for another view.
    const tail = (state: string, n: string | number): (string | number)[] => {
      if (!reader) return [n]
      const out: (string | number)[] = [volumeHidden ? (state === 'mature' ? 'n >= 30' : '') : n]
      if (!volumeHidden) out.push(g.coverage?.reports ?? '', g.coverage?.member ?? '')
      out.push(resp.mode, String(g.official), g.bench)
      return out
    }
    for (const cp of resp.checkpoints) {
      const c = g.cells[cp]
      if (!c) continue
      if (c.state !== 'mature') {
        // One row for the state, so a reader sees why the cell is blank rather than a missing row.
        // A reader never gets the size of a cell that has not cleared its gates.
        rows.push([g.label, g.start, g.end, cp, c.state, '', '', '', '', '', ...tail(c.state, reader ? '' : c.n ?? c.total ?? '')])
        continue
      }
      const base = [g.label, g.start, g.end, cp, c.state]
      const n = c.n ?? ''
      if (c.spread_bp !== undefined) {
        rows.push([...base, 'spread_bp', c.spread_bp, 0, c.spread_p ?? '', c.spread_q ?? '', ...tail(c.state, n)])
      }
      if (c.ic !== undefined) {
        rows.push([...base, 'ic', c.ic, 0, c.ic_p ?? '', c.ic_q ?? '', ...tail(c.state, c.ic_n ?? n)])
      }
      if (c.hit !== undefined) {
        rows.push([...base, 'hit', c.hit, c.hit_null ?? '', c.hit_p ?? '', c.hit_q ?? '', ...tail(c.state, c.hit_n ?? n)])
      }
      if (c.fill !== undefined) {
        rows.push([...base, 'fill', c.fill, '', '', '', ...tail(c.state, n)])
        if (c.fill_ret_bp !== undefined) {
          rows.push([...base, 'fill_ret_bp', c.fill_ret_bp, '', '', '', ...tail(c.state, n)])
        }
      }
      if (c.band !== undefined) rows.push([...base, 'band', c.band, '', '', '', ...tail(c.state, c.band_n ?? n)])
      if (c.calib !== undefined) rows.push([...base, 'calib', c.calib, '', '', '', ...tail(c.state, c.calib_n ?? n)])
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
