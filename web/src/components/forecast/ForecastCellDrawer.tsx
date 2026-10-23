import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Drawer, Space, Table, Tag, Typography, theme } from 'antd'
import type { ColumnType } from 'antd/es/table'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText, qs } from '../../api/client'
import { forecastLinkColor, forecastPalette, formatSigned } from '../../lib/forecastPresentation'
import { MetricsTable, SlicesTable } from './ForecastDrawerMetrics'
import ScrollRegion from './ScrollRegion'
import type {
  ForecastCell,
  ForecastCheckpoint,
  ForecastGrouping,
  ForecastMember,
  ForecastMembersResponse,
  ForecastMode,
} from '../../api/types'
import ExploratoryTag from './ExploratoryTag'
import Quiet from './Quiet'
import WarnTag from './WarnTag'

// The drawer over a matrix cell (plan §8.1). The numbers come from the cell that was clicked — the
// server is not asked to recompute them — and only the member list is fetched, so what the drawer
// states and what the matrix showed cannot disagree.
//
// It has two modes, chosen by the cell. A MATURE cell opens the scored view: every metric, the
// policy slices, and each member's outcome. Any other cell opens the MEMBERSHIP view: who is in the
// cohort and what each is waiting on, with no metric and no outcome column, because a cell that has
// not passed its gates publishes no result and a half-settled cohort invites a reader to average
// what the matrix refuses to. An exploratory range is marked in the title and reports p* alone.
//
// It reads the reader members route and nothing else. That route pages by cursor and, for a reader
// who may not know the ledger's volume, sends no totals or counts, so the drawer never states a
// total and reads every count as possibly absent.

const PAGE = 100

interface Props {
  open: boolean
  onClose: () => void
  mode: ForecastMode
  from: string
  to: string
  group: ForecastGrouping
  label: string
  checkpoint: ForecastCheckpoint
  cell: ForecastCell | null
  /** The report type the matrix was drawn for, when the reader chose one; absent is the default. */
  rtype?: string
}

// One loaded list, tagged with the cell it belongs to. Anything tagged for another cell is never
// drawn, so opening B cannot show A's rows for the moment before B's answer arrives.
interface Loaded {
  key: string
  members: ForecastMember[]
  next: string
  loading: boolean
  error: string
  errorAt: 'first' | 'more' | ''
}

// The reasons a member of a not-yet-mature cell is not a scored result yet. A closed list written by
// the server; anything else is read as an unknown status, never echoed.
const REASONS = ['pending_baseline', 'non_standard_horizon', 'missing_mark', 'pending', 'refused', 'refused_final', 'corp_action', 'unknown_mark_status']

export default function ForecastCellDrawer({ open, onClose, mode, from, to, group, label, checkpoint, cell, rtype = '' }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  // No cell (a caller that opens the list on its own) is the scored view, as it always was.
  const membership = !!cell && cell.state !== 'mature'
  const key = JSON.stringify([mode, from, to, group, label, checkpoint, rtype, membership])
  const [stored, setStored] = useState<Loaded>()
  const [revision, setRevision] = useState(0)
  // The live request scope: replaced whenever the cell changes or the drawer closes, which retires
  // every request still in flight for the old one.
  const scope = useRef<{ live: boolean }>({ live: false })

  const request = (cursor?: string) => {
    const query = new URLSearchParams({ mode })
    if (mode === 'custom') {
      query.set('from', from)
      query.set('to', to)
      query.set('group', group)
    }
    if (rtype) query.set('rtype', rtype)
    if (membership) query.set('population', 'all')
    query.set('label', label)
    query.set('checkpoint', checkpoint)
    query.set('limit', String(PAGE))
    if (cursor) query.set('cursor', cursor)
    return api.get<ForecastMembersResponse>(`/api/forecast/groups/members?${query.toString()}`)
  }

  useEffect(() => {
    if (!open || !label) return
    const mine = { live: true }
    scope.current = mine
    setStored({ key, members: [], next: '', loading: true, error: '', errorAt: '' })
    request()
      .then((r) => {
        if (mine.live) setStored({ key, members: r.members ?? [], next: r.next_cursor ?? '', loading: false, error: '', errorAt: '' })
      })
      .catch((e) => {
        if (mine.live) setStored({ key, members: [], next: '', loading: false, error: errText(e, t), errorAt: 'first' })
      })
    return () => {
      mine.live = false
    }
    // `request` closes over exactly the values in `key`; `t` is stable in the app.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, key, revision])

  const current: Loaded =
    stored?.key === key ? stored : { key, members: [], next: '', loading: open && !!label, error: '', errorAt: '' }

  const more = () => {
    if (!current.next) return
    const mine = scope.current
    setStored({ ...current, loading: true, error: '', errorAt: '' })
    request(current.next)
      .then((r) => {
        if (mine.live) {
          setStored((s) => (s ? { ...s, members: [...s.members, ...(r.members ?? [])], next: r.next_cursor ?? '', loading: false } : s))
        }
      })
      .catch((e) => {
        if (mine.live) setStored((s) => (s ? { ...s, loading: false, error: errText(e, t), errorAt: 'more' } : s))
      })
  }

  const retry = () => (current.errorAt === 'more' ? more() : setRevision((n) => n + 1))

  const reasonText = (code: string | undefined) =>
    t(`forecast.drawer.reason.${!code ? 'settled' : REASONS.includes(code) ? code : 'unknown_mark_status'}`)
  // Where the membership view comes from, said once: how far the cohort has got, if the reader may
  // know. A reader who may not know the ledger's volume gets the note alone.
  const stateLine =
    cell?.state === 'pending' && cell.total !== undefined
      ? t('forecast.cell.pending', { done: cell.resolved ?? 0, total: cell.total })
      : cell?.state === 'small' && cell.n !== undefined
        ? t('forecast.cell.small', { n: cell.n })
        : ''

  const stock: ColumnType<ForecastMember> = {
    title: t('forecast.drawer.stock'),
    dataIndex: 'symbol',
    key: 'symbol',
    render: (_, m) => (
      <Space size={4} wrap>
        {m.report_id ? (
          <Link
            to={`/stock/${encodeURIComponent(m.symbol)}${qs({ date: m.report_date, kind: m.kind ?? '', r: m.report_id })}`}
            style={{ color: forecastLinkColor(token) }}
          >
            {m.name || m.symbol}
          </Link>
        ) : (
          <span>{m.name || m.symbol}</span>
        )}
        {/* A halted stock settles on its last traded close: its flat return is not a quiet stock. */}
        {!membership && m.halted && <WarnTag>{t('forecast.mark.halted')}</WarnTag>}
      </Space>
    ),
  }
  const received: ColumnType<ForecastMember> = {
    title: t('forecast.drawer.received'),
    dataIndex: 'made_on',
    key: 'made_on',
    render: (_, m) => <Quiet>{m.made_on}</Quiet>,
  }
  const stance: ColumnType<ForecastMember> = {
    title: t('forecast.drawer.stance'),
    dataIndex: 'stance',
    key: 'stance',
    render: (_, m) => (m.stance === 'long' ? <Tag color="red">{t('forecast.stance.long')}</Tag> : <Tag>{t('forecast.stance.no_position')}</Tag>),
  }
  const scoredColumns: ColumnType<ForecastMember>[] = [
    stock,
    received,
    stance,
    {
      title: t('forecast.drawer.upside'),
      dataIndex: 'upside',
      key: 'upside',
      render: (_, m) => (m.upside === undefined ? '—' : `${formatSigned(m.upside * 100, 1)}%`),
    },
    {
      title: t('forecast.drawer.excess'),
      dataIndex: 'excess_bp',
      key: 'excess_bp',
      render: (_, m) => (m.excess_bp === undefined ? '—' : `${formatSigned(m.excess_bp / 100, 1)}%`),
    },
    {
      title: t('forecast.drawer.agreement'),
      dataIndex: 'direction',
      key: 'direction',
      render: (_, m) => {
        // Text, not colour alone: the four states have to be readable in print and to a reader who
        // cannot tell the tags apart.
        const d = m.direction
        if (d === undefined) return '—'
        if (d === 'agree') {
          // antd's green preset measures 3.4:1 on white; the palette's green is checked at AA.
          const agree = forecastPalette(token).negative
          return <Tag style={{ color: agree.foreground, background: agree.background, borderColor: token.colorSuccessBorder }}>{t('forecast.direction.agree')}</Tag>
        }
        return <Tag color={d === 'disagree' ? 'red' : 'default'}>{t(`forecast.direction.${d}`)}</Tag>
      },
    },
  ]
  // No outcome column of any kind: not upside, not excess, not agreement, whatever the payload says.
  const membershipColumns: ColumnType<ForecastMember>[] = [
    stock,
    received,
    stance,
    {
      title: t('forecast.drawer.status'),
      dataIndex: 'excluded_reason',
      key: 'excluded_reason',
      render: (_, m) => reasonText(m.excluded_reason),
    },
  ]

  return (
    <Drawer
      open={open}
      onClose={onClose}
      size={760}
      title={
        <Space wrap size={8}>
          <span>{`${label} · ${t(`forecast.cp.${checkpoint}`)}`}</span>
          {mode === 'custom' && <ExploratoryTag />}
          {membership && <Tag style={{ margin: 0 }}>{t('forecast.drawer.mode_membership')}</Tag>}
        </Space>
      }
      destroyOnHidden
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        {mode === 'custom' && !membership && <Quiet>{t('forecast.custom.banner')}</Quiet>}
        {membership && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <Quiet>{t('forecast.drawer.membership_note')}</Quiet>
            {stateLine && <Typography.Text>{stateLine}</Typography.Text>}
          </div>
        )}
        {!membership && cell && cell.n !== undefined && (
          <Quiet>
            {t('forecast.drawer.headline', {
              members: cell.n,
              refused: cell.refused ?? 0,
              corp: cell.corp_action ?? 0,
            })}
          </Quiet>
        )}
        {!membership && cell && cell.n === undefined && cell.min_n_met && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <Quiet>{t('forecast.cell.min_n_met')}</Quiet>
            <Quiet style={{ fontSize: 12 }}>
              {t('forecast.cell.min_n_met_hint')}
            </Quiet>
          </div>
        )}

        {!membership && <MetricsTable cell={cell ?? { state: 'mature' }} mode={mode} />}
        {!membership && cell && <SlicesTable cell={cell} mode={mode} />}

        <div>
          <Typography.Title level={5} style={{ marginTop: 0 }}>
            {t('forecast.drawer.members')}
          </Typography.Title>
          {current.error && (
            <Alert
              type="error"
              showIcon
              title={current.error}
              style={{ marginBottom: 8 }}
              action={<Button size="small" onClick={retry}>{t('common.retry')}</Button>}
            />
          )}
          <ScrollRegion label={t('forecast.drawer.members')}>
            <Table<ForecastMember>
              size="small"
              rowKey="set_id"
              loading={current.loading}
              pagination={false}
              dataSource={current.members}
              columns={membership ? membershipColumns : scoredColumns}
            />
          </ScrollRegion>
          {current.next && (
            <div style={{ textAlign: 'center', marginTop: 12 }}>
              <Button onClick={more} loading={current.loading}>
                {t('forecast.drawer.load_more')}
              </Button>
            </div>
          )}
        </div>
      </div>
    </Drawer>
  )
}
