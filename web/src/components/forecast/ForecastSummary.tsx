import { theme } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ForecastGroupsResponse } from '../../api/types'

// One line over the matrix that says how much there is and what it is waiting for: since when the
// ledger holds rows, how many sets, how many were accepted, how many stocks, what the checkpoints are
// doing, and when the next result can appear.
//
// Every number in it is volume, so it exists only where the response carries a summary; a reader who
// may not know the ledger's volume gets no strip at all, and the page never fills the gap.

type Summary = NonNullable<ForecastGroupsResponse['summary']>

// The phases a checkpoint can be in, in reading order. Settled and waiting are always named (a young
// ledger's honest answer is "none settled, so many waiting"); the others only when something is in them.
const PHASES = ['resolved', 'waiting_s0', 'waiting', 'settling', 'retrying', 'overdue', 'final', 'unknown'] as const
const ALWAYS = new Set<string>(['resolved', 'waiting'])

const count = (n: number) => n.toLocaleString('en-US')

export default function ForecastSummary({ summary }: { summary: Summary }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const phases = PHASES.flatMap((phase) => {
    const n = summary.marks_by_phase?.[phase] ?? 0
    return n > 0 || ALWAYS.has(phase) ? [t('forecast.summary.phase', { phase: t(`forecast.phase.${phase}`), n: count(n) })] : []
  })
  const parts = [
    ...(summary.since ? [t('forecast.summary.since', { date: summary.since })] : []),
    t('forecast.summary.sets', { n: count(summary.sets) }),
    t('forecast.summary.accepted', { n: count(summary.accepted) }),
    t('forecast.summary.stocks', { n: count(summary.stocks) }),
    `${t('forecast.summary.checkpoints')}${phases.join(' · ')}`,
    ...(summary.next_result_not_before
      ? [t(summary.expected ? 'forecast.summary.next_expected' : 'forecast.summary.next', { date: summary.next_result_not_before })]
      : []),
  ]
  return (
    <div
      data-part="summary"
      style={{
        background: token.colorFillQuaternary,
        color: token.colorTextSecondary,
        borderRadius: token.borderRadiusLG,
        padding: '8px 12px',
        fontSize: 13,
        overflowWrap: 'anywhere',
      }}
    >
      {parts.join(' · ')}
    </div>
  )
}
