import { useEffect, useState } from 'react'
import { Alert, Drawer, Table, Tag, Typography } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, qs } from '../../api/client'
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

export default function ForecastCellDrawer({ open, onClose, mode, from, to, group, label, checkpoint, cell }: Props) {
  const { t } = useTranslation()
  const [members, setMembers] = useState<ForecastMember[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    if (!open || !label) return
    let cancelled = false
    setLoading(true)
    setErr('')
    const base = new URLSearchParams({ mode })
    if (mode === 'custom') {
      base.set('from', from)
      base.set('to', to)
      base.set('group', group)
    }
    base.set('label', label)
    base.set('checkpoint', checkpoint)
    api
      .get<ForecastMembersResponse>(`/api/admin/forecast/groups/members?${base.toString()}`)
      .then((r) => {
        if (!cancelled) setMembers(r.members ?? [])
      })
      .catch(() => {
        if (!cancelled) setErr(t('common.loadFailed'))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, mode, from, to, group, label, checkpoint])

  const fmtP = formatProbability
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
        n: cell.n ?? '—',
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
        n: cell.ic_n ?? cell.n ?? '—',
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
        n: cell.hit_n ?? cell.n ?? '—',
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
        n: cell.n ?? '—',
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
        n: cell.band_n ?? '—',
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
        n: cell.calib_n ?? '—',
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
        {cell && (
          <Typography.Text type="secondary">
            {t('forecast.drawer.headline', {
              members: cell.n ?? 0,
              refused: cell.refused ?? 0,
              corp: cell.corp_action ?? 0,
            })}
          </Typography.Text>
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
          {err && <Alert type="error" showIcon title={err} />}
          <Table<ForecastMember>
            size="small"
            rowKey="set_id"
            loading={loading}
            pagination={{ pageSize: 20, hideOnSinglePage: true }}
            dataSource={members}
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
        </div>
      </div>
    </Drawer>
  )
}
