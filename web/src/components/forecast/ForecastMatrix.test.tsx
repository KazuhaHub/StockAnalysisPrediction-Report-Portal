import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import type { ForecastCell, ForecastGroupsResponse } from '../../api/types'
import ForecastMatrix from './ForecastMatrix'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const mature: ForecastCell = {
  state: 'mature',
  n: 42,
  spread_bp: 310,
  spread_p: 0.012,
  spread_q: 0.041,
}

function resp(cells: Record<string, ForecastCell>): ForecastGroupsResponse {
  return {
    ok: true,
    mode: 'month',
    official: true,
    range: { from: '2026-09-01', to: '2026-09-30' },
    checkpoints: ['d5', 'd20', 'd60', 'd120', 'h12'],
    unrecorded: 0,
    groups: [
      {
        label: '2026-09',
        start: '2026-09-01',
        end: '2026-09-30',
        official: true,
        bench: 'sh000852',
        coverage: {
          reports: 50,
          member: 42,
          duplicate: 5,
          absent: 1,
          rejected: 2,
          backtest: 0,
          degraded: 0,
          mismatch: 0,
          pending_baseline: 0,
          pre_live: 0,
          test: 0,
          off_benchmark: 0,
        },
        cells,
      },
    ],
  }
}

const empty = { d5: { state: 'open' }, d20: { state: 'open' }, d60: { state: 'open' }, d120: { state: 'open' }, h12: { state: 'open' } } as Record<string, ForecastCell>

function show(r: ForecastGroupsResponse, mode: 'month' | 'custom' = 'month', extra: { auditLink?: boolean } = {}) {
  return render(
    <MemoryRouter>
      <ForecastMatrix resp={r} metric="spread" mode={mode} onOpen={() => undefined} {...extra} />
    </MemoryRouter>,
  )
}

// What an external reader with the all scope receives: no coverage, no unrecorded, no metadata, and
// cells that carry a state, or the metrics plus the n >= 30 flag, but never a count.
function withheld(cells: Record<string, ForecastCell>): ForecastGroupsResponse {
  const r = resp(cells)
  delete r.unrecorded
  for (const g of r.groups) delete g.coverage
  return r
}

describe('ForecastMatrix', () => {
  it('stretches every checkpoint to its row height across mixed states', () => {
    show(resp({
      d5: { ...mature, refused: 3 },
      d20: { ...mature, spread_bp: -210, spread_q: 0.2 },
      d60: { state: 'pending', resolved: 31, total: 42 },
      d120: { state: 'small', n: 12 },
      h12: { state: 'open' },
    }))
    const rows = screen.getAllByRole('row')
    expect(rows[1].style.display).toBe('grid')
    expect(rows[1].style.gridTemplateColumns).toBe(rows[0].style.gridTemplateColumns)
    const cells = rows[1].querySelectorAll('td')
    for (const cell of Array.from(cells).slice(1, 6)) {
      expect(cell.style.display).toBe('flex')
      expect(cell.style.alignItems).toBe('stretch')
      const box = cell.firstElementChild as HTMLElement
      expect(box.style.flex).toBe('1 1 0%')
      expect(box.style.height).toBe('')
    }
  })

  it('renders a mature cell with its value, q and n', () => {
    show(resp({ ...empty, d5: mature }))
    expect(screen.getByText('+3.1 pt')).toBeTruthy()
    expect(screen.getByText(/q=0.041/)).toBeTruthy()
    expect(document.body.textContent).toContain('forecast.cov.summary:')
  })

  it.each([
    ['pending', { state: 'pending', resolved: 31, total: 42, spread_bp: 999 } as ForecastCell],
    ['small', { state: 'small', n: 12, spread_bp: 999 } as ForecastCell],
    ['open', { state: 'open', spread_bp: 999 } as ForecastCell],
  ])('shows no number on a %s cell even when the payload carries one', (_state, cell) => {
    show(resp({ ...empty, d5: cell }))
    expect(screen.queryByText('10.0 pt')).toBeNull()
    expect(screen.queryByText('9.99 pt')).toBeNull()
    expect(screen.queryByText(/pt$/)).toBeNull()
  })

  it('shows the progress of a pending checkpoint', () => {
    show(resp({ ...empty, d5: { state: 'pending', resolved: 31, total: 42 } }))
    expect(screen.getByText('forecast.cell.pending:{"done":31,"total":42}')).toBeTruthy()
  })

  it('marks an exploratory group and keeps the p* convention', () => {
    show(
      {
        ...resp({ ...empty, d5: mature }),
        mode: 'custom',
        official: false,
        range: { from: '2026-09-01', to: '2026-09-30', group: 'month' },
        groups: [{ ...resp({ ...empty, d5: mature }).groups[0], official: false }],
      },
      'custom',
    )
    expect(screen.getByText('forecast.tag.exploratory')).toBeTruthy()
    expect(screen.getByText(/p\*=0.012/)).toBeTruthy()
    const labelCell = screen.getByText('2026-09').closest('td')!
    expect(labelCell.style.whiteSpace).not.toBe('nowrap')
    expect(labelCell.style.overflowWrap).toBe('anywhere')
    expect(screen.getByText('forecast.tag.exploratory').parentElement!.style.flexWrap).toBe('wrap')
  })

  it('highlights a rejected count and links it to the audit log', () => {
    show(resp({ ...empty, d5: mature }))
    const link = screen.getByRole('link', { name: /forecast.cov.rejected/ })
    expect(link.getAttribute('href')).toBe('/manage/audit?action=predictions.reject')
  })

  it('expresses significance in text and glyphs, and removes it when q fails the gate', () => {
    const { unmount } = show(resp({ ...empty, d5: mature }))
    expect(screen.getByRole('button').textContent).toContain('▲')
    expect(screen.getByRole('button').textContent).toContain('q<0.05')
    expect(screen.getByRole('button').textContent).toContain('forecast.signal.positive')
    unmount()
    show(resp({ ...empty, d5: { ...mature, spread_q: 0.05 } }))
    expect(screen.getByRole('button').textContent).not.toContain('q<0.05')
    expect(screen.getByRole('button').textContent).not.toContain('▲')
  })

  describe('for a reader who may not see the ledger volume', () => {
    const gated: ForecastCell = { state: 'mature', min_n_met: true, spread_bp: 310, spread_p: 0.012, spread_q: 0.041 }

    it('renders without coverage, without crashing, and says n >= 30 instead of a count', () => {
      show(withheld({
        d5: gated,
        d20: { state: 'pending' },
        d60: { state: 'small' },
        d120: { state: 'open' },
        h12: { state: 'purged' },
      }))
      expect(screen.getByText('+3.1 pt')).toBeTruthy()
      expect(document.body.textContent).toContain('forecast.cell.min_n_met')
      // No coverage column, no coverage cell, no count of any kind.
      expect(screen.queryByText('forecast.col.coverage')).toBeNull()
      expect(document.body.textContent).not.toContain('forecast.cov.')
      expect(document.body.textContent).not.toMatch(/forecast\.cell\.n:/)
      expect(document.body.textContent).not.toContain('"n":0')
      expect(document.body.textContent).not.toContain('"done":0')
      expect(document.body.textContent).not.toContain('"total":0')
    })

    it('states the publication gate once per cell, as its own badge, and not inside the metric line', () => {
      show(withheld({ ...empty, d5: gated }))
      const badge = screen.getByText('forecast.cell.min_n_met')
      // The explanation is visible text and not a hover title, which a keyboard or a phone never sees.
      expect(badge.hasAttribute('title')).toBe(false)
      expect(screen.getAllByText('forecast.cell.min_n_met')).toHaveLength(1)
      // The line under the value is the corrected value alone: no sample is claimed for the metric.
      expect(screen.getByText('q=0.041')).toBeTruthy()
    })

    it('explains the gate in one visible line, tied to every flagged cell', () => {
      show(withheld({ ...empty, d5: gated, d20: gated, d60: { ...gated, spread_bp: -200 } }))
      const hint = screen.getByText('forecast.cell.min_n_met_hint')
      expect(hint.tagName).not.toBe('TITLE')
      expect(hint.id).not.toBe('')
      const buttons = screen.getAllByRole('button')
      expect(buttons).toHaveLength(3)
      for (const button of buttons) expect(button.getAttribute('aria-describedby')).toBe(hint.id)
      expect(screen.getAllByText('forecast.cell.min_n_met_hint')).toHaveLength(1)
    })

    it('says nothing about the gate where every mature cell carries its count', () => {
      show(resp({ ...empty, d5: mature }))
      expect(screen.queryByText('forecast.cell.min_n_met_hint')).toBeNull()
      expect(screen.getByRole('button').hasAttribute('aria-describedby')).toBe(false)
    })

    it('does not explain a gate that no cell shows', () => {
      show(withheld({ ...empty, d5: { state: 'pending' } }))
      expect(screen.queryByText('forecast.cell.min_n_met_hint')).toBeNull()
    })

    it.each(['ic', 'hit', 'fill'] as const)('never reads the cell gate as the %s sample size', (metric) => {
      const cell: ForecastCell = { ...gated, ic: 0.21, ic_q: 0.047, hit: 0.61, hit_null: 0.55, hit_q: 0.3, fill: 0.33, fill_ret_bp: -120 }
      render(
        <MemoryRouter>
          <ForecastMatrix resp={withheld({ ...empty, d5: cell })} metric={metric} mode="month" onOpen={() => undefined} />
        </MemoryRouter>,
      )
      const button = screen.getByRole('button')
      expect(screen.getAllByText('forecast.cell.min_n_met')).toHaveLength(1)
      // The badge is a sibling of the metric line, never part of it.
      const sub = Array.from(button.querySelectorAll('span')).find((n) => n.textContent?.includes('q=') || n.textContent?.includes('after_fill'))!
      expect(sub.textContent).not.toContain('forecast.cell.min_n_met')
    })

    it('adds no badge to a cell that carries its count', () => {
      show(resp({ ...empty, d5: mature }))
      expect(screen.queryByText('forecast.cell.min_n_met')).toBeNull()
    })

    it('shows a pending cell with no progress bar when the server sent no counts', () => {
      const { container } = show(withheld({ ...empty, d5: { state: 'pending' } }))
      expect(screen.getByText('forecast.cell.pending_plain')).toBeTruthy()
      expect(container.querySelector('[data-part="progress"]')).toBeNull()
    })

    it('says a small cell is below the gate without stating how many names it has', () => {
      show(withheld({ ...empty, d5: { state: 'small' } }))
      expect(screen.getByText('forecast.cell.small_nocount')).toBeTruthy()
      expect(document.body.textContent).not.toContain('forecast.cell.small:')
    })

    it('names a purged cell instead of leaving it blank', () => {
      show(resp({ ...empty, d5: { state: 'purged' } }))
      expect(screen.getByText('forecast.cell.purged')).toBeTruthy()
    })

    it('keeps the coverage column for a reader who is allowed volume', () => {
      show(resp({ ...empty, d5: mature }))
      expect(screen.getByText('forecast.col.coverage')).toBeTruthy()
    })

    it('leaves the audit link to administrators', () => {
      show(resp({ ...empty, d5: mature }), 'month', { auditLink: false })
      expect(screen.queryByRole('link', { name: /forecast.cov.rejected/ })).toBeNull()
      expect(document.body.textContent).toContain('forecast.cov.rejected')
    })
  })
})

// The cell states a young ledger spends most of its life in. Each one has to say what the cell is
// waiting for, and none of them may show a number the matrix refuses to publish.
describe('ForecastMatrix cell states', () => {
  const cellOf = (state: string) => document.querySelector(`[data-state="${state}"]`) as HTMLElement

  it('draws an open cell with a dashed border, and makes it neither a button nor a link', () => {
    show(resp({ ...empty, d5: mature }))
    const open = cellOf('open')
    expect(open.style.borderStyle).toBe('dashed')
    expect(open.tagName).not.toBe('BUTTON')
    expect(within(open).queryByRole('button')).toBeNull()
    expect(open.textContent).toBe('forecast.cell.open_month')
  })

  it('says whether an unfinished group is a month or a custom group', () => {
    show({ ...resp({ ...empty }), mode: 'custom', official: false }, 'custom')
    expect(cellOf('open').textContent).toBe('forecast.cell.open')
  })

  it('shows a small cell as its size, the gate and that nothing is published, with no progress bar', () => {
    show(resp({ ...empty, d5: { state: 'small', n: 12 } }))
    const small = cellOf('small')
    expect(small.textContent).toContain('forecast.cell.small:{"n":12}')
    expect(small.querySelector('[data-part="progress"]')).toBeNull()
    expect(small.textContent).not.toContain('forecast.cell.pending')
  })

  it('shows a payload with metrics on a small cell as nothing but the gate text', () => {
    show(resp({ ...empty, d5: { state: 'small', n: 12, spread_bp: 999, ic: 0.9, hit: 0.9, fill: 0.9 } as ForecastCell }))
    expect(cellOf('small').textContent).not.toMatch(/pt|0\.9|90%/)
  })

  it('gives a pending cell its progress, the phases that hold it and the earliest date', () => {
    show(resp({
      ...empty,
      d5: {
        state: 'pending', resolved: 31, total: 42, not_before: '2026-10-26',
        pending_reasons: { resolved: 31, waiting: 8, retrying: 2, overdue: 1, waiting_s0: 0, settling: 0, final: 0, unknown: 0 },
      },
    }))
    const pending = cellOf('pending')
    expect(pending.textContent).toContain('forecast.cell.pending:{"done":31,"total":42}')
    expect(pending.querySelector('[data-part="progress"]')).not.toBeNull()
    expect(pending.textContent).toContain('forecast.cell.reason:{"phase":"forecast.phase.waiting","n":8}')
    expect(pending.textContent).toContain('forecast.cell.reason:{"phase":"forecast.phase.retrying","n":2}')
    expect(pending.textContent).toContain('forecast.cell.reason:{"phase":"forecast.phase.overdue","n":1}')
    expect(pending.textContent).not.toContain('forecast.phase.resolved')
    expect(pending.textContent).toContain('forecast.cell.not_before:{"date":"2026-10-26"}')
  })

  it('words a calendar-backed date as expected and leaves a lower bound as "not before"', () => {
    show(resp({ ...empty, d5: { state: 'pending', resolved: 0, total: 5, not_before: '2026-10-26', expected: true } }))
    expect(cellOf('pending').textContent).toContain('forecast.cell.expected:{"date":"2026-10-26"}')
    expect(cellOf('pending').textContent).not.toContain('forecast.cell.not_before')
  })

  it('tells the twelve-month checkpoint as the month it can start from', () => {
    show(resp({ ...empty, h12: { state: 'pending', resolved: 0, total: 5, not_before: '2027-05-14' } }))
    expect(cellOf('pending').textContent).toContain('forecast.cell.from_month:{"month":"2027-05"}')
    expect(cellOf('pending').textContent).not.toContain('2027-05-14')
  })

  it('draws no reasons and no date when the server explained nothing', () => {
    show(resp({ ...empty, d5: { state: 'pending', resolved: 4, total: 9 } }))
    const text = cellOf('pending').textContent!
    expect(text).toContain('forecast.cell.pending:{"done":4,"total":9}')
    expect(text).not.toContain('forecast.cell.reason')
    expect(text).not.toContain('forecast.cell.not_before')
  })

  it('names the final-refusal cap as the reason a finished cell does not publish', () => {
    show(resp({ ...empty, d5: { state: 'pending', resolved: 47, total: 50, failed_cap_exceeded: true, pending_reasons: { final: 3, resolved: 47 } } }))
    const text = cellOf('pending').textContent!
    expect(text).toContain('forecast.cell.cap:{"refused":3,"total":50,"pct":6}')
    expect(text).toContain('forecast.cell.cap_hint')
  })

  it('shows an external reader\'s pending cell as its state alone, even if a malformed payload carries reasons', () => {
    show(withheld({ ...empty, d5: { state: 'pending', pending_reasons: { waiting: 5 }, not_before: '2026-10-26', failed_cap_exceeded: true } }))
    const text = cellOf('pending').textContent!
    expect(text).toBe('forecast.cell.pending_plain')
  })

  it('shows a purged cell as purged, not as an empty box', () => {
    show(resp({ ...empty, d5: { state: 'purged' } }))
    expect(cellOf('purged').textContent).toBe('forecast.cell.purged')
    expect(cellOf('purged').tagName).not.toBe('BUTTON')
  })

  it('draws a mature value at 18px and weight 600, signed, as a button with hover and focus styling', () => {
    show(resp({ ...empty, d5: mature }))
    const button = cellOf('mature')
    expect(button.tagName).toBe('BUTTON')
    expect(button.className).toContain('rp-fc-cell')
    const value = screen.getByText('+3.1 pt')
    expect(value.style.fontSize).toBe('18px')
    expect(value.style.fontWeight).toBe('600')
  })

  it.each([
    ['pending', { state: 'pending', resolved: 3, total: 9 } as ForecastCell],
    ['small', { state: 'small', n: 12 } as ForecastCell],
  ])('opens a %s cell\'s members when it is pressed, because membership is settled before results are', (state, cell) => {
    const onOpen = vi.fn()
    render(
      <MemoryRouter>
        <ForecastMatrix resp={resp({ ...empty, d5: cell })} metric="spread" mode="month" onOpen={onOpen} />
      </MemoryRouter>,
    )
    const button = cellOf(state)
    expect(button.tagName).toBe('BUTTON')
    fireEvent.click(button)
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen.mock.calls[0][1]).toBe('d5')
  })

  it('does not offer a drawer for an open or a purged cell, which have no settled membership to list', () => {
    show(resp({ ...empty, d5: { state: 'purged' } }))
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })

  it.each(['open', 'pending', 'small', 'purged'] as const)('never reads a metric from a %s cell, whatever the payload carries', (state) => {
    show(resp({ ...empty, d5: { state, resolved: 1, total: 2, n: 3, spread_bp: 999, spread_q: 0.001, ic: 0.9, hit: 0.9, fill: 0.9 } as ForecastCell }))
    expect(document.body.textContent).not.toMatch(/9\.99|10\.0 pt|\+9\.9|90%|q<0\.05/)
    expect(document.querySelector('[data-state="mature"]')).toBeNull()
  })
})

// The coverage cell answers "where did this group's reports go?": a bar whose segments add up to the
// group's reports, each with its own colour and a name, plus a chip for the buckets a reader should
// not have to decode from colour alone.
describe('ForecastMatrix coverage', () => {
  const segs = () => Array.from(document.querySelectorAll('[data-seg]')) as HTMLElement[]
  const seg = (name: string) => document.querySelector(`[data-seg="${name}"]`) as HTMLElement | null
  const widthOf = (el: HTMLElement | null) => (el ? parseFloat(el.style.width) : 0)
  const full = {
    reports: 100, member: 50, duplicate: 20, absent: 4, rejected: 6, backtest: 3, degraded: 2, mismatch: 1,
    pending_baseline: 4, pre_live: 5, test: 3, off_benchmark: 2, unknown: 0,
  }
  const withCoverage = (coverage: Partial<typeof full>) => {
    const r = resp({ ...empty, d5: mature })
    r.groups[0].coverage = { ...full, ...coverage }
    return r
  }

  it('draws a segment per bucket family, in distinct colours, that together fill the group\'s reports', () => {
    show(withCoverage({}))
    expect(segs().map((el) => el.dataset.seg)).toEqual(['member', 'duplicate', 'missing', 'other', 'pre_live'])
    expect(widthOf(seg('member'))).toBeCloseTo(50)
    expect(widthOf(seg('duplicate'))).toBeCloseTo(20)
    expect(widthOf(seg('missing'))).toBeCloseTo(10)
    // Backtest, degraded, mismatch, unverified baseline, test and off-benchmark are one family.
    expect(widthOf(seg('other'))).toBeCloseTo(15)
    expect(widthOf(seg('pre_live'))).toBeCloseTo(5)
    expect(segs().reduce((sum, el) => sum + widthOf(el), 0)).toBeCloseTo(100)
    const backgrounds = segs().map((el) => el.style.background)
    expect(new Set(backgrounds).size).toBe(backgrounds.length)
  })

  it('keeps an unknown state in the bar so coverage always adds up', () => {
    show(withCoverage({ unknown: 5, member: 45 }))
    expect(widthOf(seg('other'))).toBeCloseTo(20)
  })

  it('states the counted share as text, never by colour alone', () => {
    show(withCoverage({}))
    expect(document.body.textContent).toContain('forecast.cov.summary:{"member":50,"reports":100}')
  })

  it('names the ledger-start, test and off-benchmark buckets as chips, only when they are not empty', () => {
    show(withCoverage({}))
    const chips = Array.from(document.querySelectorAll('[data-chip]')).map((el) => `${(el as HTMLElement).dataset.chip}=${el.textContent}`)
    expect(chips).toEqual([
      'pre_live=forecast.cov.pre_live:{"n":5}',
      'test=forecast.cov.test:{"n":3}',
      'off_benchmark=forecast.cov.off_benchmark:{"n":2}',
      'pending_baseline=forecast.cov.pending_baseline:{"n":4}',
    ])
  })

  it('draws no chip for a bucket that is empty', () => {
    show(withCoverage({ pre_live: 0, test: 0, off_benchmark: 0, pending_baseline: 0 }))
    expect(document.querySelectorAll('[data-chip]')).toHaveLength(0)
  })

  it('does not divide by zero for a group with no reports', () => {
    show(withCoverage({ reports: 0, member: 0, duplicate: 0, absent: 0, rejected: 0, backtest: 0, degraded: 0, mismatch: 0, pending_baseline: 0, pre_live: 0, test: 0, off_benchmark: 0 }))
    for (const el of segs()) expect(el.style.width).not.toMatch(/NaN|Infinity/)
  })

  it('shows a purged group\'s coverage as purged, not as a zero-report bar', () => {
    const r = resp({ d5: { state: 'purged' }, d20: { state: 'purged' }, d60: { state: 'purged' }, d120: { state: 'purged' }, h12: { state: 'purged' } } as Record<string, ForecastCell>)
    r.groups[0].purged_at = '2026-12-01T00:00:00Z'
    r.groups[0].coverage = { ...full, reports: 0, member: 0, duplicate: 0, absent: 0, rejected: 0, backtest: 0, degraded: 0, mismatch: 0, pending_baseline: 0, pre_live: 0, test: 0, off_benchmark: 0 }
    show(r)
    expect(segs()).toHaveLength(0)
    expect(document.body.textContent).not.toContain('forecast.cov.summary')
  })

  it('has no bar, no chip and no count anywhere for a reader whose response carries no coverage', () => {
    show(withheld({ ...empty, d5: { state: 'mature', min_n_met: true, spread_bp: 310, spread_p: 0.012, spread_q: 0.041 } }))
    expect(segs()).toHaveLength(0)
    expect(document.querySelectorAll('[data-chip]')).toHaveLength(0)
  })

  it('explains the buckets in a legend only when coverage is drawn', () => {
    show(withCoverage({}))
    expect(screen.getByRole('list', { name: 'forecast.legend.coverage_title' })).toBeTruthy()
  })

  it('does not draw the coverage legend where there is no coverage', () => {
    show(withheld({ ...empty, d5: { state: 'mature', min_n_met: true, spread_bp: 310, spread_p: 0.012, spread_q: 0.041 } }))
    expect(screen.queryByRole('list', { name: 'forecast.legend.coverage_title' })).toBeNull()
  })
})

