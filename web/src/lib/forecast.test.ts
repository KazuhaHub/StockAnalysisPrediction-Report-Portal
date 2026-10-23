import { describe, expect, it } from 'vitest'
import type { ForecastCell, ForecastGroup, ForecastGroupsResponse } from '../api/types'
import {
  benchRuns,
  capExceeded,
  cellDisplay,
  civilDate,
  defaultRange,
  forecastCsv,
  groupsQuery,
  notBeforeInfo,
  pendingReasonParts,
  rangePresets,
  rangeProblem,
  readView,
  visibleGroups,
} from './forecast'

// These pin the two things a wrong implementation would hide: a number on a cell that is not
// mature, and an export that cannot be told apart from the official record.

const t = (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k)

const mature: ForecastCell = {
  state: 'mature',
  n: 42,
  spread_bp: 310,
  spread_p: 0.012,
  spread_q: 0.041,
  ic: 0.21,
  ic_n: 40,
  ic_p: 0.02,
  ic_q: 0.047,
  hit: 0.61,
  hit_n: 38,
  hit_null: 0.55,
  hit_p: 0.18,
  hit_q: 0.3,
  fill: 0.33,
  fill_ret_bp: -120,
}

describe('cellDisplay', () => {
  it('reads no metric from a cell that is not mature, even if the payload carries one', () => {
    // A pending cell with numbers is the exact shape a regression would produce: the server changed
    // to send partial metrics, or the UI started reading them regardless. It must still render as
    // pending.
    const pending: ForecastCell = { ...mature, state: 'pending' }
    expect(cellDisplay(pending, 'spread', 'month', t)).toBeNull()
    expect(cellDisplay({ ...mature, state: 'small' }, 'ic', 'month', t)).toBeNull()
    expect(cellDisplay({ ...mature, state: 'open' }, 'hit', 'month', t)).toBeNull()
  })

  it('formats spread in percentage points and colours it only when q passes 0.05', () => {
    const d = cellDisplay(mature, 'spread', 'month', t)
    expect(d?.value).toBe('+3.1 pt')
    expect(d?.signal).toBe(1)
    expect(d?.sub).toContain('q=0.041')

    const uncoloured = cellDisplay({ ...mature, spread_q: 0.2, spread_p: 0.1 }, 'spread', 'month', t)
    expect(uncoloured?.signal).toBe(0)
  })

  it('signs a spread and an IC so direction reads without colour, and leaves rates unsigned', () => {
    expect(cellDisplay({ ...mature, spread_bp: -210 }, 'spread', 'month', t)?.value).toBe('-2.1 pt')
    expect(cellDisplay({ ...mature, spread_bp: 0 }, 'spread', 'month', t)?.value).toBe('0.0 pt')
    expect(cellDisplay(mature, 'ic', 'month', t)?.value).toBe('+0.21')
    expect(cellDisplay({ ...mature, ic: -0.04 }, 'ic', 'month', t)?.value).toBe('-0.04')
    expect(cellDisplay(mature, 'hit', 'month', t)?.value).toBe('61%')
    expect(cellDisplay(mature, 'fill', 'month', t)?.value).toBe('33%')
  })

  it('marks p as p* in the exploratory view', () => {
    expect(cellDisplay(mature, 'ic', 'custom', t)?.sub).toContain('p*=')
  })

  it('reports a one-armed spread as a dash rather than a zero', () => {
    const d = cellDisplay({ ...mature, spread_bp: undefined, spread_one_arm: true }, 'spread', 'month', t)
    expect(d?.value).toBe('—')
    expect(d?.sub).toBe('forecast.cell.one_arm')
  })
})

describe('cellDisplay for a reader who may not see counts', () => {
  // An external reader with the all scope gets the gate's assurance instead of any count. The
  // assurance belongs to the CELL: its member count cleared n >= 30. A metric's own denominator is a
  // smaller subset with no such minimum (a hit rate over twelve tested reports, an IC over two
  // pairs), so the flag must never sit in a metric's sample slot, where it would read as that
  // metric's own n.
  const gated: ForecastCell = {
    state: 'mature',
    min_n_met: true,
    spread_bp: 310,
    spread_p: 0.012,
    spread_q: 0.041,
    ic: 0.21,
    ic_p: 0.02,
    ic_q: 0.047,
    hit: 0.61,
    hit_null: 0.55,
    hit_p: 0.18,
    hit_q: 0.3,
    fill: 0.33,
    fill_ret_bp: -120,
  }

  it.each(['spread', 'ic', 'hit', 'fill'] as const)('states the gate once on the cell and never inside the %s sample slot', (metric) => {
    const d = cellDisplay(gated, metric, 'month', t)
    expect(d?.gate).toBe('forecast.cell.min_n_met')
    expect(d?.sub).not.toContain('forecast.cell.min_n_met')
    expect(d?.sub).not.toContain('forecast.cell.n')
    expect(d?.sub).not.toContain('"n":0')
  })

  it('leaves a metric\'s sample slot empty rather than borrowing the cell\'s gate for it', () => {
    // The shape of a 40-member cell whose hit rate rests on 12 tested reports: the reader is told
    // nothing about the 12, and in particular is not told "n >= 30" beside the hit rate.
    expect(cellDisplay(gated, 'hit', 'month', t)?.sub).toBe(['q=0.300', 'forecast.cell.base_rate:{"pct":"55%"}'].join(' · '))
    expect(cellDisplay(gated, 'ic', 'month', t)?.sub).toBe('q=0.047')
    expect(cellDisplay(gated, 'fill', 'month', t)?.sub).toBe('forecast.cell.after_fill:{"pct":"-1.2"}')
  })

  it('still carries the cell gate when the metric itself is absent', () => {
    const d = cellDisplay({ ...gated, hit: undefined }, 'hit', 'month', t)
    expect(d?.value).toBe('—')
    expect(d?.sub).toBe('')
    expect(d?.gate).toBe('forecast.cell.min_n_met')
  })

  it('states nothing about the sample rather than inventing a zero when neither a count nor the flag is present', () => {
    const d = cellDisplay({ ...gated, min_n_met: undefined }, 'spread', 'month', t)
    expect(d?.sub).toBe('q=0.041')
    expect(d?.gate).toBe('')
  })

  it('prefers a real count, and then has no gate to add', () => {
    const d = cellDisplay({ ...gated, n: 42, min_n_met: undefined }, 'spread', 'month', t)
    expect(d?.sub).toContain('forecast.cell.n:{"n":42}')
    expect(d?.gate).toBe('')
    // Even with the flag present beside a real count, the count is what is said.
    expect(cellDisplay({ ...gated, n: 42 }, 'spread', 'month', t)?.gate).toBe('')
  })
})

describe('forecastCsv', () => {
  const resp: ForecastGroupsResponse = {
    ok: true,
    mode: 'custom',
    official: false,
    range: { from: '2026-01-01', to: '2026-02-01', group: 'week' },
    checkpoints: ['d5', 'd20'],
    unrecorded: 3,
    groups: [
      {
        label: '2026-01-05 – 2026-01-11',
        start: '2026-01-05',
        end: '2026-01-11',
        official: false,
        bench: 'sh000852',
        coverage: {
          reports: 10,
          member: 8,
          duplicate: 1,
          absent: 0,
          rejected: 1,
          backtest: 0,
          degraded: 0,
          mismatch: 0,
          pending_baseline: 0,
          pre_live: 0,
          test: 0,
          off_benchmark: 0,
        },
        cells: { d5: mature, d20: { state: 'open' }, d60: { state: 'open' }, d120: { state: 'open' }, h12: { state: 'open' } },
      },
    ],
  }

  it('carries mode, official and the range in its comment header', () => {
    const csv = forecastCsv(resp, '2026-10-18T00:00:00Z')
    const lines = csv.split('\n')
    expect(lines[0]).toBe('# mode=custom')
    expect(lines[1]).toBe('# official=false')
    expect(lines[2]).toBe('# range=2026-01-01..2026-02-01')
    expect(lines[3]).toBe('# group=week')
    expect(lines).toContain('# generated_at=2026-10-18T00:00:00Z')
    expect(lines).toContain('group,start,end,checkpoint,state,metric,value,null,p,q,n')
  })

  it('writes one row per metric and a state row for a cell with no numbers', () => {
    const csv = forecastCsv(resp, '2026-10-18T00:00:00Z')
    expect(csv).toContain('2026-01-05 – 2026-01-11,2026-01-05,2026-01-11,d5,mature,spread_bp,310,0,0.012,0.041,42')
    expect(csv).toContain('2026-01-05 – 2026-01-11,2026-01-05,2026-01-11,d20,open,,,,,,')
    expect(csv).not.toContain('d20,mature')
  })

  it('labels every reader export row and omits site-wide counts from its header', () => {
    const csv = forecastCsv(resp, '2026-10-18T00:00:00Z', { population: 'Own unit\nforged header', disclaimer: 'Research only', publicationGate: 'The flag belongs to the cell', hideCounts: false })
    expect(csv).toContain('# population=Own unit forged header')
    expect(csv).toContain('# disclaimer=Research only')
    expect(csv).not.toContain('# unrecorded=')
    expect(csv).toContain('n,coverage_reports,coverage_member,mode,official,bench')
    const rows = csv.split('\n').filter((line) => line && !line.startsWith('#')).slice(1)
    expect(rows.length).toBeGreaterThan(0)
    for (const row of rows) expect(row.endsWith(',custom,false,sh000852')).toBe(true)
    // An internal or own-scope reader keeps the sample size and the coverage of the population.
    expect(csv).toContain('0.041,42,10,8,custom,false,sh000852')
  })

  it('carries the benchmark of every group in the header and on every row', () => {
    const two = structuredClone(resp)
    two.groups.push({ ...structuredClone(resp.groups[0]), label: '2026-01-12 – 2026-01-18', bench: 'sh000906' })
    const csv = forecastCsv(two, '2026-10-18T00:00:00Z', { population: 'All reports', disclaimer: 'Research only', publicationGate: 'The flag belongs to the cell', hideCounts: false })
    expect(csv).toContain('# benchmark=2026-01-05 – 2026-01-11:sh000852')
    expect(csv).toContain('# benchmark=2026-01-12 – 2026-01-18:sh000906')
    const rows = csv.split('\n').filter((line) => line && !line.startsWith('#')).slice(1)
    expect(rows.some((row) => row.endsWith(',custom,false,sh000906'))).toBe(true)
    for (const row of rows) expect(/,custom,false,sh000(852|906)$/.test(row)).toBe(true)
  })

  it('removes count columns even if a malformed external-all response contains them', () => {
    const csv = forecastCsv(resp, '2026-10-18T00:00:00Z', { population: 'All reports', disclaimer: 'Research only', publicationGate: 'The flag belongs to the cell', hideCounts: true })
    const header = csv.split('\n').find((line) => line.startsWith('group,'))!
    expect(header.split(',')).not.toContain('n')
    expect(header).not.toMatch(/coverage/)
    expect(header).toContain('publication_gate,mode,official,bench')
    expect(csv).not.toContain('0.041,42')
    expect(csv).toContain('0.041,n >= 30,custom,false,sh000852')
    expect(csv).not.toContain('# unrecorded=')
    // The flag is the cell's, repeated on each metric row; the file says so, because a hit rate or an
    // IC rests on a smaller subset that has no minimum of its own.
    expect(csv).toContain('# publication_gate=The flag belongs to the cell')
    expect(csv).not.toContain('a metric may rest on a smaller subset')
  })

  it('keeps a multi-line gate note on its own comment line', () => {
    const csv = forecastCsv(resp, '2026-10-18T00:00:00Z', { population: 'All reports', disclaimer: 'Research only', publicationGate: 'First\nsecond\r\nthird', hideCounts: true })
    expect(csv).toContain('# publication_gate=First second  third\n')
    expect(csv.split('\n').filter((line) => line.startsWith('second') || line.startsWith('third'))).toEqual([])
  })

  it('does not explain a gate flag in an export that has none', () => {
    const csv = forecastCsv(resp, '2026-10-18T00:00:00Z', { population: 'Own unit', disclaimer: 'Research only', publicationGate: 'The flag belongs to the cell', hideCounts: false })
    expect(csv).not.toContain('# publication_gate=')
  })

  it('exports no volume at all for an external all reader even when the response lacks the coverage field', () => {
    const hidden = structuredClone(resp)
    for (const g of hidden.groups) delete g.coverage
    delete hidden.unrecorded
    // hideCounts false would be a caller bug; the missing coverage is the server's own statement
    // that volume is withheld, and the export has to follow it.
    const csv = forecastCsv(hidden, '2026-10-18T00:00:00Z', { population: 'All reports', disclaimer: 'Research only', publicationGate: 'The flag belongs to the cell', hideCounts: false })
    const header = csv.split('\n').find((line) => line.startsWith('group,'))!
    expect(header.split(',')).not.toContain('n')
    expect(header).not.toMatch(/coverage/)
    expect(csv).toContain('0.041,n >= 30,custom,false,sh000852')
  })

  it('never exports non-mature sample sizes for any reader population', () => {
    const malformed = structuredClone(resp)
    malformed.groups[0].cells.d20 = { state: 'pending', n: 987654, total: 987654 }
    const csv = forecastCsv(malformed, '2026-10-18T00:00:00Z', { population: 'Own unit', disclaimer: 'Research only', publicationGate: 'The flag belongs to the cell', hideCounts: false })
    expect(csv).not.toContain('987654')
    expect(malformed.groups[0].cells.d20.n).toBe(987654)
  })
})

describe('view state', () => {
  it('defaults to the monthly record', () => {
    expect(readView(new URLSearchParams())).toEqual({ mode: 'month', from: '', to: '', group: 'month', metric: 'spread', rtype: '', older: false })
  })

  it('round-trips a custom view', () => {
    const q = groupsQuery('custom', '2026-01-01', '2026-02-01', 'week')
    const view = readView(new URLSearchParams(q))
    expect(view).toEqual({ mode: 'custom', from: '2026-01-01', to: '2026-02-01', group: 'week', metric: 'spread', rtype: '', older: false })
  })

  it('sends no range in monthly mode, so the server never has to refuse one', () => {
    expect(groupsQuery('month', '2026-01-01', '2026-02-01', 'week')).toBe('mode=month')
  })

  it('rejects values outside the closed vocabularies', () => {
    const view = readView(new URLSearchParams('mode=custom&group=decade&metric=bogus'))
    expect(view.group).toBe('month')
    expect(view.metric).toBe('spread')
  })
})

describe('report type and older months in the view', () => {
  it('sends the report type with a monthly or custom request only when one is chosen', () => {
    expect(groupsQuery('month', '', '', 'month', 'A')).toBe('mode=month&rtype=A')
    expect(groupsQuery('custom', '2026-01-01', '2026-02-01', 'week', 'A')).toBe('mode=custom&from=2026-01-01&to=2026-02-01&group=week&rtype=A')
    expect(groupsQuery('month', '', '', 'month', '')).toBe('mode=month')
  })

  it('round-trips the report type and the older-months switch through the URL', () => {
    const view = readView(new URLSearchParams('rtype=Decision+memo&older=1'))
    expect(view.rtype).toBe('Decision memo')
    expect(view.older).toBe(true)
    expect(readView(new URLSearchParams('older=yes')).older).toBe(false)
  })

  it('names the report type in a reader export header, once, on one line', () => {
    const resp: ForecastGroupsResponse = {
      ok: true, mode: 'month', official: true, range: { from: '2026-01-01', to: '2026-01-31' }, checkpoints: ['d5'], groups: [],
    }
    const csv = forecastCsv(resp, 'now', { population: 'All', disclaimer: 'D', publicationGate: 'G', hideCounts: false, rtype: 'Type\nA' })
    expect(csv).toContain('# rtype=Type A\n')
    expect(forecastCsv(resp, 'now', { population: 'All', disclaimer: 'D', publicationGate: 'G', hideCounts: false })).not.toContain('# rtype=')
  })
})

const group = (label: string, bench = 'sh000852'): ForecastGroup => ({
  label, start: `${label}-01`, end: `${label}-28`, official: true, bench, cells: {} as ForecastGroup['cells'],
})

describe('visibleGroups', () => {
  const months = Array.from({ length: 15 }, (_, i) => group(`2026-${String(i + 1).padStart(2, '0')}`))

  it('keeps the newest twelve months of the record and counts what is folded away', () => {
    const { shown, hidden } = visibleGroups(months, 'month', false)
    expect(shown.map((g) => g.label)).toEqual(months.slice(3).map((g) => g.label))
    expect(hidden).toBe(3)
  })

  it('shows everything when asked, and never folds a short record', () => {
    expect(visibleGroups(months, 'month', true)).toEqual({ shown: months, hidden: 0 })
    expect(visibleGroups(months.slice(0, 12), 'month', false)).toEqual({ shown: months.slice(0, 12), hidden: 0 })
  })

  it('never folds a custom range: its extent is the reader\'s own choice', () => {
    expect(visibleGroups(months, 'custom', false)).toEqual({ shown: months, hidden: 0 })
  })
})

describe('benchRuns', () => {
  it('collapses consecutive groups on one benchmark and keeps a change visible', () => {
    const runs = benchRuns([group('2026-10'), group('2026-11', 'sh000906'), group('2026-12', 'sh000906'), group('2027-01', 'sh000852')])
    expect(runs).toEqual([
      { bench: 'sh000852', from: '2026-10', to: '2026-10' },
      { bench: 'sh000906', from: '2026-11', to: '2026-12' },
      { bench: 'sh000852', from: '2027-01', to: '2027-01' },
    ])
  })

  it('skips a group that states no benchmark rather than inventing one', () => {
    expect(benchRuns([group('2026-10', ''), group('2026-11')])).toEqual([{ bench: 'sh000852', from: '2026-11', to: '2026-11' }])
    expect(benchRuns([])).toEqual([])
  })
})

describe('pending cell explanation', () => {
  it('lists the phases that still hold marks, in a fixed order, without the settled count', () => {
    const cell: ForecastCell = {
      state: 'pending', resolved: 30, total: 40,
      pending_reasons: { resolved: 30, waiting: 6, waiting_s0: 1, retrying: 2, overdue: 1, final: 0, settling: 0 },
    }
    expect(pendingReasonParts(cell)).toEqual([
      { phase: 'waiting_s0', n: 1 }, { phase: 'waiting', n: 6 }, { phase: 'retrying', n: 2 }, { phase: 'overdue', n: 1 },
    ])
  })

  it('says nothing when the server explained nothing, and keeps an unknown phase visible', () => {
    expect(pendingReasonParts({ state: 'pending' })).toEqual([])
    expect(pendingReasonParts({ state: 'pending', pending_reasons: { unknown: 3 } })).toEqual([{ phase: 'unknown', n: 3 }])
  })

  it('reads the final-refusal cap from the flag, the refusals and the member count', () => {
    expect(capExceeded({ state: 'pending', failed_cap_exceeded: true, total: 50, pending_reasons: { final: 3 } })).toEqual({ refused: 3, total: 50, pct: 6 })
    expect(capExceeded({ state: 'pending', failed_cap_exceeded: false, total: 50, pending_reasons: { final: 1 } })).toBeNull()
    expect(capExceeded({ state: 'pending', total: 50, pending_reasons: { final: 9 } })).toBeNull()
  })

  it('does not invent a ratio when the member count is missing', () => {
    expect(capExceeded({ state: 'pending', failed_cap_exceeded: true, pending_reasons: { final: 3 } })).toEqual({ refused: 3, total: undefined, pct: undefined })
  })

  it('reports the earliest date as a month for the twelve-month checkpoint and as a date elsewhere', () => {
    const cell: ForecastCell = { state: 'pending', not_before: '2027-05-14', expected: true }
    expect(notBeforeInfo(cell, 'd5')).toEqual({ date: '2027-05-14', expected: true })
    expect(notBeforeInfo(cell, 'h12')).toEqual({ date: '2027-05', expected: true })
    expect(notBeforeInfo({ state: 'pending' }, 'd5')).toBeNull()
    expect(notBeforeInfo({ state: 'pending', not_before: '' }, 'd5')).toBeNull()
  })
})

describe('custom range helpers', () => {
  const limits = { maxMonths: 36, maxWeekGroups: 26 }

  it('turns the server instant into the panel civil date (Asia/Shanghai)', () => {
    expect(civilDate('2027-06-20T07:00:00Z')).toBe('2027-06-20')
    expect(civilDate('2027-06-20T17:00:00Z')).toBe('2027-06-21')
    expect(civilDate('not a date')).toBe('')
    expect(civilDate(undefined)).toBe('')
  })

  it('mirrors the server\'s range checks', () => {
    expect(rangeProblem('2026-01-01', '2026-12-31', 'month', limits)).toBe('')
    expect(rangeProblem('2026-12-31', '2026-01-01', 'month', limits)).toBe('inverted')
    expect(rangeProblem('2023-01-01', '2026-01-01', 'month', limits)).toBe('')
    expect(rangeProblem('2023-01-01', '2026-01-02', 'month', limits)).toBe('too_long')
    expect(rangeProblem('2026-01-01', '2026-06-30', 'week', limits)).toBe('')
    expect(rangeProblem('2026-01-01', '2026-07-02', 'week', limits)).toBe('week_too_long')
    expect(rangeProblem('2026-01-01', '2026-01-31', 'month', { maxMonths: 0, maxWeekGroups: 0 })).toBe('')
  })

  it('does not second-guess a range it has no limits for', () => {
    expect(rangeProblem('2000-01-01', '2026-01-01', 'week', undefined)).toBe('')
    expect(rangeProblem('2026-12-31', '2026-01-01', 'week', undefined)).toBe('inverted')
  })

  it('offers presets that end today, start inside the limits, and add the ledger start only when known', () => {
    const presets = rangePresets('2027-06-20', '2026-10-19', limits)
    expect(presets.map((p) => p.key)).toEqual(['last3', 'last6', 'last12', 'since_ledger'])
    expect(presets[0]).toEqual({ key: 'last3', from: '2027-03-20', to: '2027-06-20' })
    expect(presets[3]).toEqual({ key: 'since_ledger', from: '2026-10-19', to: '2027-06-20' })
    expect(rangePresets('2027-06-20', undefined, limits).map((p) => p.key)).toEqual(['last3', 'last6', 'last12'])
  })

  it('clamps the ledger preset to the longest range the server accepts and drops presets that cannot fit', () => {
    const presets = rangePresets('2027-06-20', '2020-01-01', limits)
    expect(presets.find((p) => p.key === 'since_ledger')).toEqual({ key: 'since_ledger', from: '2024-06-20', to: '2027-06-20' })
    expect(rangePresets('2027-06-20', undefined, { maxMonths: 5, maxWeekGroups: 26 }).map((p) => p.key)).toEqual(['last3'])
  })

  it('never offers a ledger start in the future', () => {
    expect(rangePresets('2027-06-20', '2027-07-01', limits).map((p) => p.key)).not.toContain('since_ledger')
  })

  it('opens on the record since the ledger began, and on six months when it does not know', () => {
    expect(defaultRange('2027-06-20', '2026-10-19', limits)).toEqual({ from: '2026-10-19', to: '2027-06-20' })
    expect(defaultRange('2027-06-20', undefined, limits)).toEqual({ from: '2026-12-20', to: '2027-06-20' })
  })
})
