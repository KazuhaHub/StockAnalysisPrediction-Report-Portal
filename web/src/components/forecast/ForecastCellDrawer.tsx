import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Drawer, Table, Tag, Typography } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText, qs } from '../../api/client'
import { formatProbability } from '../../lib/forecastPresentation'
import type {
  ForecastCell,
  ForecastCheckpoint,
  ForecastGrouping,
  ForecastMember,
  ForecastMembersResponse,
  ForecastMode,
} from '../../api/types'

// The drawer over a mature cell (plan §8.1). The numbers come from the cell that was clicked — the
// server is not asked to recompute them — and only the member list is fetched, so what the drawer
// states and what the matrix showed cannot disagree.
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

export default function ForecastCellDrawer({ open, onClose, mode, from, to, group, label, checkpoint, cell }: Props) {
  const { t } = useTranslation()
  const key = JSON.stringify([mode, from, to, group, label, checkpoint])
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

  const fmtP = formatProbability
  // A count the reader may not know is replaced by the gate's assurance, never by a zero.
  const sample = (n: number | undefined): number | string =>
    n !== undefined ? n : cell?.min_n_met ? t('forecast.cell.min_n_met') : '—'
  const rows: { key: string; metric: string; value: string; nullText: string; p: string; q: string; n: number | string }[] = []
  if (cell) {
    if (cell.spread_bp !== undefined) {
      rows.push({
        key: 'spread',
        metric: t('forecast.metric.spread'),
        value: `${(cell.spread_bp / 100).toFixed(1)} pt`,
        nullText: '0',
        p: fmtP(cell.spread_p),
        q: fmtP(cell.spread_q),
        n: sample(cell.n),
      })
    }
    if (cell.ic !== undefined) {
      rows.push({
        key: 'ic',
        metric: t('forecast.metric.ic'),
        value: cell.ic.toFixed(2),
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
    }
    if (cell.band !== undefined) {
      rows.push({
        key: 'band',
        metric: t('forecast.metric.band'),
        value: `${((cell.band ?? 0) * 100).toFixed(0)}%`,
        nullText: '—',
        p: '—',
        q: '—',
        n: sample(cell.band_n),
      })
    }
    if (cell.calib !== undefined) {
      rows.push({
        key: 'calib',
        metric: t('forecast.metric.calib'),
        value: ((cell.calib ?? 0) * 100).toFixed(0) + '%',
        nullText: '—',
        p: '—',
        q: '—',
        n: sample(cell.calib_n),
      })
    }
  }

  const columns = [
    { title: t('forecast.drawer.stock'), dataIndex: 'symbol', key: 'symbol' },
    { title: t('forecast.drawer.received'), dataIndex: 'made_on', key: 'made_on' },
    { title: t('forecast.drawer.stance'), dataIndex: 'stance', key: 'stance' },
    { title: t('forecast.drawer.upside'), dataIndex: 'upside', key: 'upside' },
    { title: t('forecast.drawer.excess'), dataIndex: 'excess_bp', key: 'excess_bp' },
    { title: t('forecast.drawer.agreement'), dataIndex: 'direction', key: 'direction' },
  ]

  return (
    <Drawer
      open={open}
      onClose={onClose}
      size={760}
      title={`${label} · ${t(`forecast.cp.${checkpoint}`)}${mode === 'custom' ? ` · ${t('forecast.tag.exploratory')}` : ''}`}
      destroyOnHidden
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        {cell && cell.n !== undefined && (
          <Typography.Text type="secondary">
            {t('forecast.drawer.headline', {
              members: cell.n,
              refused: cell.refused ?? 0,
              corp: cell.corp_action ?? 0,
            })}
          </Typography.Text>
        )}
        {cell && cell.n === undefined && cell.min_n_met && (
          <Typography.Text type="secondary">{t('forecast.cell.min_n_met')}</Typography.Text>
        )}

        <Table
          size="small"
          rowKey="key"
          pagination={false}
          dataSource={rows}
          columns={[
            { title: t('forecast.drawer.metric'), dataIndex: 'metric', key: 'metric' },
            { title: t('forecast.drawer.value'), dataIndex: 'value', key: 'value' },
            { title: t('forecast.drawer.null'), dataIndex: 'nullText', key: 'nullText' },
            { title: t('forecast.drawer.p'), dataIndex: 'p', key: 'p' },
            { title: t('forecast.drawer.q'), dataIndex: 'q', key: 'q' },
            { title: t('forecast.drawer.n'), dataIndex: 'n', key: 'n' },
          ]}
        />

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
          <Table<ForecastMember>
            size="small"
            rowKey="set_id"
            loading={current.loading}
            pagination={false}
            dataSource={current.members}
            columns={[
              {
                ...columns[0],
                render: (_, m) =>
                  m.report_id ? (
                    <Link to={`/stock/${encodeURIComponent(m.symbol)}${qs({ date: m.report_date, kind: m.kind ?? '' })}`}>
                      {m.name || m.symbol}
                    </Link>
                  ) : (
                    <span>{m.name || m.symbol}</span>
                  ),
              },
              {
                ...columns[1],
                render: (_, m) => <Typography.Text type="secondary">{m.made_on}</Typography.Text>,
              },
              {
                ...columns[2],
                render: (_, m) =>
                  m.stance === 'long' ? <Tag color="red">{t('forecast.stance.long')}</Tag> : <Tag>{t('forecast.stance.no_position')}</Tag>,
              },
              {
                ...columns[3],
                render: (_, m) => (m.upside === undefined ? '—' : `${(m.upside * 100).toFixed(1)}%`),
              },
              {
                ...columns[4],
                render: (_, m) => (m.excess_bp === undefined ? '—' : `${(m.excess_bp / 100).toFixed(1)}%`),
              },
              {
                ...columns[5],
                render: (_, m) => {
                  // Text, not colour alone: the four states have to be readable in print and to a
                  // reader who cannot tell the tags apart.
                  const d = m.direction
                  if (d === undefined) return '—'
                  const label = t(`forecast.direction.${d}`)
                  const color = d === 'agree' ? 'green' : d === 'disagree' ? 'red' : d === 'flat' ? 'default' : 'default'
                  return <Tag color={color}>{label}</Tag>
                },
              },
            ]}
          />
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
