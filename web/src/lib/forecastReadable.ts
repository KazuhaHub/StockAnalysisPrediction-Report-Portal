import type { ForecastMember } from '../api/types'

const finite = (v: number | undefined): v is number => v !== undefined && Number.isFinite(v)

// These are descriptions of the loaded, visible members of an already mature cell.
// They are never substitutes for the cell's published metrics or a partial cohort result.
export function readableMembers(rows: ForecastMember[]) {
  const members = [...new Map(rows.map((m) => [m.set_id, m])).values()]
    .filter((m) => m.status === 'resolved' && !m.excluded_reason)
  const up = { tested: 0, agreed: 0 }
  const down = { tested: 0, agreed: 0 }
  for (const m of members) {
    if (!finite(m.upside) || !m.direction || m.direction === 'untested') continue
    const bucket = m.upside > 0 ? up : down
    bucket.tested++
    if (m.direction === 'agree') bucket.agreed++
  }
  const returns = (['long', 'no_position'] as const).map((stance) => {
    const paired = members.filter((m) => m.stance === stance && finite(m.ret_bp) && finite(m.bench_ret_bp))
    const values = paired.map((m) => m.ret_bp!).sort((a, b) => a - b)
    const count = values.length
    const stock = count ? values.reduce((a, b) => a + b, 0) / count : undefined
    const bench = count ? paired.reduce((a, m) => a + m.bench_ret_bp!, 0) / count : undefined
    const median = count ? (values[Math.floor((count - 1) / 2)] + values[Math.floor(count / 2)]) / 2 : undefined
    return { stance, count, stock, bench, median, excess: stock === undefined || bench === undefined ? undefined : stock - bench }
  })
  const points = members.filter((m) => finite(m.upside) && finite(m.excess_bp))
  return { members, up, down, returns, points }
}
