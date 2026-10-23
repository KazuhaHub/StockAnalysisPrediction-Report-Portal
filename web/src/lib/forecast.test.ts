import { describe, expect, it } from 'vitest'
import type { ForecastCell, ForecastGroupsResponse } from '../api/types'
import { cellDisplay, forecastCsv, groupsQuery, readView } from './forecast'

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
    expect(d?.value).toBe('3.1 pt')
    expect(d?.signal).toBe(1)
    expect(d?.sub).toContain('q=0.041')

    const uncoloured = cellDisplay({ ...mature, spread_q: 0.2, spread_p: 0.1 }, 'spread', 'month', t)
    expect(uncoloured?.signal).toBe(0)
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
    expect(readView(new URLSearchParams())).toEqual({ mode: 'month', from: '', to: '', group: 'month', metric: 'spread' })
  })

  it('round-trips a custom view', () => {
    const q = groupsQuery('custom', '2026-01-01', '2026-02-01', 'week')
    const view = readView(new URLSearchParams(q))
    expect(view).toEqual({ mode: 'custom', from: '2026-01-01', to: '2026-02-01', group: 'week', metric: 'spread' })
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
