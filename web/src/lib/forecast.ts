import { toCSV } from './csv'
import { formatProbability, formatSigned } from './forecastPresentation'
import type {
  ForecastCell,
  ForecastCheckpoint,
  ForecastGroup,
  ForecastGroupsResponse,
  ForecastMetric,
  ForecastMode,
  ForecastGrouping,
  ForecastPurgedMonth,
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
  /** -1 / 0 / +1, and nonzero only when the corrected q passes 0.05 (and, exploratory, p* does too). */
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

const ALPHA = 0.05

/**
 * Whether a metric earns the cell's tint. The record follows the corrected q alone. The exploratory
 * view prints p*, and its badge words the threshold from p*, so a cell is tinted there only when its
 * p* is under the threshold as well: q is never smaller than p, so this never colours a cell the
 * record would not, and it keeps the badge from standing behind a number the cell does not show.
 */
function significant(q: number | undefined, p: number | undefined, mode: ForecastMode): boolean {
  if (q === undefined || !(q < ALPHA)) return false
  return mode !== 'custom' || (p !== undefined && p < ALPHA)
}

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
        significant(cell.spread_q, cell.spread_p, mode) ? (cell.spread_bp > 0 ? 1 : cell.spread_bp < 0 ? -1 : 0) : 0
      return {
        value: `${formatSigned(cell.spread_bp / 100, 1)} pt`,
        sub: join(correctedText(cell.spread_p, cell.spread_q, mode), sampleText(cell.n, t)),
        gate,
        signal: sig,
      }
    }
    case 'ic': {
      if (cell.ic === undefined) return { value: '—', sub: sampleText(cell.n, t), gate, signal: 0 }
      const sig: -1 | 0 | 1 =
        significant(cell.ic_q, cell.ic_p, mode) ? (cell.ic > 0 ? 1 : cell.ic < 0 ? -1 : 0) : 0
      return {
        value: formatSigned(cell.ic, 2),
        sub: join(correctedText(cell.ic_p, cell.ic_q, mode), sampleText(cell.ic_n ?? cell.n, t)),
        gate,
        signal: sig,
      }
    }
    case 'hit': {
      if (cell.hit === undefined) return { value: '—', sub: sampleText(cell.n, t), gate, signal: 0 }
      const sig: -1 | 0 | 1 =
        significant(cell.hit_q, cell.hit_p, mode)
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
        sub: join(t('forecast.cell.after_fill', { pct: formatSigned((cell.fill_ret_bp ?? 0) / 100, 1) }), sampleText(cell.n, t)),
        gate,
        signal: 0,
      }
    }
  }
}

// ── Months removed by retention ──────────────────────────────────────────────────────────────────

const monthOf = (day: string): string => (/^\d{4}-\d{2}/.test(day) ? day.slice(0, 7) : '')

/** The calendar months (YYYY-MM) a date range touches, or none for a range that cannot be read. */
function monthsTouched(start: string, end: string): string[] {
  const first = monthOf(start)
  const last = monthOf(end)
  if (!first || !last || first > last) return []
  const out: string[] = []
  let [y, m] = first.split('-').map(Number)
  // The range is bounded by the server's limit; the cap only keeps a malformed one from looping.
  while (out.length < 1200) {
    const key = `${y}-${String(m).padStart(2, '0')}`
    out.push(key)
    if (key >= last) break
    m += 1
    if (m > 12) {
      m = 1
      y += 1
    }
  }
  return out
}

/**
 * The exploratory view's groups with retention made visible. A purged month's sets are removed from a
 * custom range but its period stays, so a group whose every month was removed comes back as a cohort
 * of no members, which the publication gate calls "small": drawn as such it would say the cohort is
 * too young to publish, when it is gone. The response's excluded_months is what says so. A group that
 * still has a month left keeps its cells, because it still has a population (the notice names what
 * was left out of it), and a cell that is a result, pending or open is never rewritten.
 *
 * Safe to apply more than once, and it returns the group list itself when there is nothing to say.
 */
export function withRetention(groups: ForecastGroup[], excluded: ForecastPurgedMonth[] | undefined): ForecastGroup[] {
  if (!excluded || excluded.length === 0) return groups
  const removedAt = new Map(excluded.map((m) => [m.cohort, m.purged_at]))
  let changed = false
  const out = groups.map((g) => {
    if (g.official) return g
    const months = monthsTouched(g.start, g.end)
    if (months.length === 0 || !months.every((month) => removedAt.has(month))) return g
    const cells = { ...(g.cells ?? {}) } as ForecastGroup['cells']
    const checkpoints = Object.keys(cells) as ForecastCheckpoint[]
    for (const cp of checkpoints) {
      if (cells[cp]?.state === 'small') cells[cp] = { state: 'purged' }
    }
    const complete = checkpoints.length > 0 && checkpoints.every((cp) => cells[cp]?.state === 'purged')
    const latest = months.map((month) => removedAt.get(month) ?? '').reduce((a, b) => (b > a ? b : a), '')
    const unchanged = checkpoints.every((cp) => cells[cp] === g.cells?.[cp]) && (!complete || g.purged_at === latest)
    if (unchanged) return g
    changed = true
    return { ...g, cells, ...(complete ? { purged_at: latest } : {}) }
  })
  return changed ? out : groups
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
  /**
   * The sentence that says the n >= 30 flag belongs to the cell, in the reader's language. It is
   * written only into an export that carries the flag. The `# key=` identifiers around it stay
   * English: they are the file's vocabulary, not prose.
   */
  publicationGate: string
  /** External all readers must never export sample-size/count columns. */
  hideCounts: boolean
  /** The report type the numbers are about, when the reader's view names one. */
  rtype?: string
}

export function forecastCsv(resp: ForecastGroupsResponse, generatedAt: string, reader?: ForecastCsvReader): string {
  const line = (value: string) => value.replace(/[\r\n]/g, ' ')
  // A month the retention policy removed is in no figure of the file. Say so in the file, and write a
  // group that was removed whole as purged: the same rows would otherwise read as a cohort too small
  // to publish.
  const excluded = resp.excluded_months ?? []
  const groups = withRetention(resp.groups, excluded)
  const lines = [
    `# mode=${resp.mode}`,
    `# official=${resp.official}`,
    `# range=${resp.range.from}..${resp.range.to}`,
    `# group=${resp.range.group ?? ''}`,
    `# checkpoints=${resp.checkpoints.join('|')}`,
    ...(excluded.length > 0 ? [`# excluded_months=${excluded.map((m) => line(`${m.cohort}@${m.purged_at}`)).join('|')}`] : []),
    ...(!reader ? [`# unrecorded=${resp.unrecorded ?? ''}`] : []),
    `# generated_at=${generatedAt}`,
  ]
  // Do not trust absence alone, and do not trust the caller alone: either one withholds volume.
  const volumeHidden = !!reader && (reader.hideCounts || groups.some((g) => !g.coverage))
  if (reader) {
    lines.push(`# disclaimer=${line(reader.disclaimer)}`, `# population=${line(reader.population)}`)
    if (reader.rtype) lines.push(`# rtype=${line(reader.rtype)}`)
    for (const group of groups) lines.push(`# benchmark=${line(group.label)}:${line(group.bench)}`)
    // The flag is a fact about the cell, repeated on each of its metric rows. Say so in the file:
    // a hit rate or an IC rests on a smaller subset that has no minimum of its own.
    if (volumeHidden) lines.push(`# publication_gate=${line(reader.publicationGate)}`)
  }
  const header: string[] = ['group', 'start', 'end', 'checkpoint', 'state', 'metric', 'value', 'null', 'p', 'q',
    volumeHidden ? 'publication_gate' : 'n']
  if (reader && !volumeHidden) header.push('coverage_reports', 'coverage_member')
  if (reader) header.push('mode', 'official', 'bench')

  const rows: (string | number)[][] = []
  for (const g of groups) {
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
export function groupsQuery(mode: ForecastMode, from: string, to: string, group: ForecastGrouping, rtype = ''): string {
  const p = new URLSearchParams({ mode })
  if (mode === 'custom') {
    p.set('from', from)
    p.set('to', to)
    p.set('group', group)
  }
  // Absent means the server's default report type; the page names one only when the reader chose it.
  if (rtype) p.set('rtype', rtype)
  return p.toString()
}

/** Read the view out of the URL, with the monthly record as the default. */
export function readView(sp: URLSearchParams): {
  mode: ForecastMode
  from: string
  to: string
  group: ForecastGrouping
  metric: ForecastMetric
  rtype: string
  older: boolean
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
    rtype: sp.get('rtype') ?? '',
    older: sp.get('older') === '1',
  }
}

// ── The record page's view helpers ───────────────────────────────────────────────────────────────

/** How many calendar months of the record a monthly view opens with. */
export const MONTH_WINDOW = 12

/**
 * The groups a view draws. The monthly record folds everything older than the newest twelve months
 * behind a "show older" switch so a long-running portal does not open on dozens of rows. A custom
 * range is never folded: its extent is the range the reader chose.
 */
export function visibleGroups<T>(groups: T[], mode: ForecastMode, showOlder: boolean): { shown: T[]; hidden: number } {
  if (mode !== 'month' || showOlder || groups.length <= MONTH_WINDOW) return { shown: groups, hidden: 0 }
  return { shown: groups.slice(-MONTH_WINDOW), hidden: groups.length - MONTH_WINDOW }
}

export interface BenchRun {
  bench: string
  from: string
  to: string
}

/**
 * The benchmarks of the groups on screen as runs of consecutive groups. Every cohort is scored
 * against the index frozen when it was recorded, so a change of benchmark shows up here as a new run
 * and not as a quiet relabelling of old rows. A group that states none (nothing recorded in it yet)
 * is skipped.
 */
export function benchRuns(groups: ForecastGroup[]): BenchRun[] {
  const runs: BenchRun[] = []
  for (const g of groups) {
    if (!g.bench) continue
    const last = runs[runs.length - 1]
    if (last && last.bench === g.bench) last.to = g.label
    else runs.push({ bench: g.bench, from: g.label, to: g.label })
  }
  return runs
}

// The phases a pending cell can still be waiting in, in the order a reader thinks about them.
// `resolved` is the progress bar, not a reason.
const REASON_PHASES = ['waiting_s0', 'waiting', 'settling', 'retrying', 'overdue', 'final', 'unknown'] as const

/** The non-empty phase counts of a pending cell. An unknown phase stays visible rather than vanishing. */
export function pendingReasonParts(cell: ForecastCell): { phase: string; n: number }[] {
  const reasons = cell.pending_reasons ?? {}
  return REASON_PHASES.flatMap((phase) => {
    const n = reasons[phase] ?? 0
    return n > 0 ? [{ phase, n }] : []
  })
}

/**
 * The final-refusal cap (more than 5% of a cohort refused for good) is what holds a finished cell
 * back from publishing, and only the server's flag says so. The counts are a convenience for the
 * sentence and are never guessed: a refusal count the server did not send is not zero, and with no
 * member count there is no ratio to state.
 */
export function capExceeded(
  cell: ForecastCell,
): { refused: number | undefined; total: number | undefined; pct: number | undefined } | null {
  if (cell.failed_cap_exceeded !== true) return null
  const refused = cell.pending_reasons?.final
  const total = cell.total
  const known = refused !== undefined && total !== undefined && total > 0
  return { refused, total, pct: known ? Math.round((refused / total) * 100) : undefined }
}

/**
 * The earliest date a pending cell can finish. The twelve-month checkpoint is a coarse horizon, so
 * it is told as a month; every other checkpoint as the date. Either is a lower bound, never a promise.
 */
export function notBeforeInfo(cell: ForecastCell, cp: ForecastCheckpoint): { date: string; expected: boolean } | null {
  if (!cell.not_before) return null
  return { date: cp === 'h12' ? cell.not_before.slice(0, 7) : cell.not_before, expected: !!cell.expected }
}

// ── Custom range ─────────────────────────────────────────────────────────────────────────────────

export interface RangeLimits {
  maxMonths: number
  maxWeekGroups: number
}

const PANEL_ZONE = 'Asia/Shanghai'

/** The civil date (YYYY-MM-DD) of a server instant in the panel timezone, or '' for a bad instant. */
export function civilDate(instant: string | undefined): string {
  if (!instant) return ''
  const at = new Date(instant)
  if (Number.isNaN(at.getTime())) return ''
  const parts = new Intl.DateTimeFormat('en-CA', { timeZone: PANEL_ZONE, year: 'numeric', month: '2-digit', day: '2-digit' })
    .formatToParts(at)
  const get = (type: string) => parts.find((p) => p.type === type)?.value ?? ''
  return `${get('year')}-${get('month')}-${get('day')}`
}

/**
 * The date the page may print as "next result": the server's lower bound, unless that bound is before
 * the response's own civil date. An overdue, settling or retrying checkpoint carries a bound that has
 * already passed, and printing it reads as a promise already broken ("expected 2026-06-09" on a page
 * generated a year later); how late those checkpoints are is said by the phase counts. When the
 * response does not say what day it is there is nothing to compare against, and the date is printed
 * as given.
 */
export function upcomingDate(date: string | undefined, generatedAt: string | undefined): string | undefined {
  if (!date) return undefined
  const today = civilDate(generatedAt)
  return today && date < today ? undefined : date
}

const isoOf = (d: Date) => d.toISOString().slice(0, 10)
const utc = (iso: string) => {
  const [y, m, d] = iso.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d))
}

/** Calendar-month arithmetic that clamps to the last day (31 January + 1 month = 28/29 February). */
function addMonthsClamped(iso: string, months: number): string {
  const [y, m, d] = iso.split('-').map(Number)
  const first = new Date(Date.UTC(y, m - 1 + months, 1))
  const last = new Date(Date.UTC(first.getUTCFullYear(), first.getUTCMonth() + 1, 0)).getUTCDate()
  return isoOf(new Date(Date.UTC(first.getUTCFullYear(), first.getUTCMonth(), Math.min(d, last))))
}

/**
 * The server's range checks, run before a request so a refusal costs nothing and can say what the
 * limit is. The server stays the authority: the "too long" comparison uses the same overflow-
 * normalising month addition, and a limit of 0 (unknown) is never enforced here.
 */
export function rangeProblem(
  from: string,
  to: string,
  group: ForecastGrouping,
  limits: RangeLimits | undefined,
): '' | 'inverted' | 'too_long' | 'week_too_long' {
  if (!from || !to) return ''
  if (from > to) return 'inverted'
  if (!limits) return ''
  if (limits.maxMonths > 0) {
    const [y, m, d] = from.split('-').map(Number)
    if (to > isoOf(new Date(Date.UTC(y, m - 1 + limits.maxMonths, d)))) return 'too_long'
  }
  if (group === 'week' && limits.maxWeekGroups > 0) {
    const days = Math.round((utc(to).getTime() - utc(from).getTime()) / 86_400_000) + 1
    if (days > limits.maxWeekGroups * 7) return 'week_too_long'
  }
  return ''
}

const RELATIVE_PRESETS = [['last3', 3], ['last6', 6], ['last12', 12]] as const

export interface RangePreset {
  key: 'last3' | 'last6' | 'last12' | 'since_ledger'
  from: string
  to: string
}

/**
 * Quick ranges ending today (the panel date). A relative preset longer than the server allows is not
 * offered; the ledger preset is clamped to the longest range the server accepts, and is offered only
 * when the ledger start is known and has passed.
 */
export function rangePresets(today: string, ledgerStart: string | undefined, limits: RangeLimits | undefined): RangePreset[] {
  const longest = limits && limits.maxMonths > 0 ? limits.maxMonths : Infinity
  const out: RangePreset[] = RELATIVE_PRESETS.filter(([, months]) => months <= longest).map(([key, months]) => ({
    key, from: addMonthsClamped(today, -months), to: today,
  }))
  if (ledgerStart && ledgerStart <= today) {
    const earliest = Number.isFinite(longest) ? addMonthsClamped(today, -longest) : ledgerStart
    out.push({ key: 'since_ledger', from: ledgerStart > earliest ? ledgerStart : earliest, to: today })
  }
  return out
}

/**
 * The range a custom tab opens on, so it is never a blank form: the record since the ledger began,
 * or the last six months when that start is not known.
 */
export function defaultRange(today: string, ledgerStart: string | undefined, limits: RangeLimits | undefined): { from: string; to: string } {
  const presets = rangePresets(today, ledgerStart, limits)
  const chosen = presets.find((p) => p.key === 'since_ledger') ?? presets.find((p) => p.key === 'last6') ?? presets[0]
  return chosen ? { from: chosen.from, to: chosen.to } : { from: addMonthsClamped(today, -1), to: today }
}
