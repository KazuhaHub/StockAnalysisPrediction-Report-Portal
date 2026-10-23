import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, DatePicker, Empty, Result, Segmented, Skeleton, Space, Tabs, Typography } from 'antd'
import { DownloadOutlined } from '@ant-design/icons'
import { useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import dayjs, { type Dayjs } from 'dayjs'
import { errText } from '../api/client'
import type { ForecastCell, ForecastCheckpoint, ForecastGroup, ForecastGrouping, ForecastGroupsResponse, ForecastMetric } from '../api/types'
import { useAuth } from '../auth'
import ForecastCellDrawer from '../components/forecast/ForecastCellDrawer'
import ForecastMatrix from '../components/forecast/ForecastMatrix'
import ForecastMethodology from '../components/forecast/ForecastMethodology'
import { downloadCSV } from '../lib/csv'
import { forecastCsv, groupsQuery, readView } from '../lib/forecast'
import { useForecastRecord } from '../lib/useForecastRecord'

// Forecast verification for readers: the record of what each report predicted against what
// the market did (ADR 0029, ADR 0039).
//
// This page asks the reader routes and nothing else. Who may see what is decided by the server on
// every request; the navigation hint in `me.forecast` only decides what this page offers, so a stale
// hint produces a refusal that the page shows, not a leak.
//
// Two views over one engine. The monthly view is the RECORD: the calendar decides its groups, and a
// mature cell can never change again. The custom range is EXPLORATORY: because that freedom lets a
// reader search for a flattering window, every p in it is p* and the export says so in its header.
// External readers get the record only; a custom range would let them difference the ledger's volume
// out of it.

const GROUPS = ['week', 'month', 'quarter', 'all'] as const
const METRICS = ['spread', 'ic', 'hit', 'fill'] as const

export default function ForecastRecordPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { user, admin, forecast } = useAuth()
  const [sp, setSp] = useSearchParams()
  const view = useMemo(() => readView(sp), [sp])

  // The hint is a navigation aid, so an absent one (an older server) reads as no access. An
  // administrator is never scoped.
  const scope = admin ? 'all' : (forecast?.scope ?? 'none')
  const allowed = admin || scope === 'all' || scope === 'own'
  const external = !!forecast?.external
  const own = !admin && scope === 'own'
  const mode = external ? 'month' : view.mode

  // A custom view waits until both dates are present, so an empty tab does not spend a permutation
  // run on the whole ledger.
  const rangeInverted = mode === 'custom' && !!view.from && !!view.to && dayjs(view.from).isAfter(dayjs(view.to))
  const query =
    mode === 'month'
      ? groupsQuery('month', '', '', 'month')
      : view.from && view.to && !rangeInverted
        ? groupsQuery('custom', view.from, view.to, view.group)
        : null
  // Everything that changes whose numbers these are, so a held result can never outlive it.
  const identity = [user, scope, external, forecast?.ou ?? ''].join('|')
  const record = useForecastRecord(query, identity, allowed)

  const [drawer, setDrawer] = useState<{ g: ForecastGroup; cp: ForecastCheckpoint; cell: ForecastCell } | null>(null)
  useEffect(() => setDrawer(null), [query, identity])

  const patch = (next: Record<string, string>) => {
    const p = new URLSearchParams(sp)
    for (const [k, v] of Object.entries(next)) {
      if (v) p.set(k, v)
      else p.delete(k)
    }
    setSp(p, { replace: true })
  }
  const setRange = (v: [Dayjs | null, Dayjs | null] | null) =>
    patch({ from: v?.[0]?.format('YYYY-MM-DD') ?? '', to: v?.[1]?.format('YYYY-MM-DD') ?? '' })
  const rangeValue: [Dayjs | null, Dayjs | null] | null = view.from && view.to ? [dayjs(view.from), dayjs(view.to)] : null

  const data: ForecastGroupsResponse | undefined = record.kind === 'ready' ? record.data : undefined
  const groups = data?.groups ?? []
  // A cell with a result is a mature one. Open, pending, small and purged cells carry state and no
  // numbers, so a record made only of them has nothing to show yet however many groups it lists.
  const hasResult = groups.some((g) => Object.values(g.cells ?? {}).some((c) => c?.state === 'mature'))

  // The population is the one the answer states, because the hint was read when the session started
  // and the scope may have changed since. The hint speaks only until an answer arrives (and for a
  // server that states none). An answer that says "own" without a unit name stays unnamed: it must
  // not borrow a name from a hint that may describe another unit.
  const stated = data?.scope
  const populationOwn = stated ? stated === 'own' : own
  const populationOu = stated ? data?.scope_ou : forecast?.ou
  const population = populationOwn
    ? populationOu
      ? t('forecast.record.population_own', { ou: populationOu })
      : t('forecast.record.population_own_unnamed')
    : t('forecast.record.population_all')

  const exportCsv = () => {
    if (!data) return
    const name = `forecast-${data.mode}-${data.range.from || 'monthly'}-${data.range.to || ''}.csv`
    downloadCSV(
      name,
      forecastCsv(data, new Date().toISOString(), {
        population,
        disclaimer: t('forecast.disclaimer'),
        // An external reader with the all scope must never export volume. The export also follows
        // the response itself: a group without coverage means the server withheld it.
        hideCounts: external && scope === 'all',
      }),
    )
  }

  const body = () => {
    switch (record.kind) {
      case 'loading':
        return (
          <div aria-busy="true">
            <Skeleton active title paragraph={{ rows: 6 }} />
          </div>
        )
      case 'idle':
        return (
          <>
            {rangeInverted && <Alert type="error" showIcon title={t('forecast.err.range_inverted')} />}
            {!rangeInverted && <Empty description={t('forecast.record.pick_range')} />}
          </>
        )
      case 'unavailable':
        // Terminal: the server decided, and asking again cannot change it.
        return (
          <Result
            status="403"
            title={t('forecast.record.unavailable')}
            subTitle={t(record.code === 'forecast_custom_not_allowed' ? 'err.forecast_custom_not_allowed' : 'err.forecast_unavailable')}
            extra={
              record.code === 'forecast_custom_not_allowed' ? (
                <Button onClick={() => patch({ mode: '', from: '', to: '', group: '' })}>{t('forecast.record.show_monthly')}</Button>
              ) : (
                <Button onClick={() => navigate('/')}>{t('nav.home')}</Button>
              )
            }
          />
        )
      case 'error':
        return (
          <Alert
            type="error"
            showIcon
            title={errText(record.error, t)}
            description={isBusy(record.error) ? t('forecast.record.busy') : undefined}
            action={<Button size="small" onClick={record.retry}>{t('common.retry')}</Button>}
          />
        )
      case 'ready': {
        // "No result yet" is a property of the cells, not of the group list: a young ledger has its
        // open month as a group, and every cell in it is waiting. Without metadata (an external
        // reader with the all scope) there is no date to give, and the page does not make one up.
        const next = record.data.summary?.next_result_not_before
        const emptyText = next
          ? t(record.data.summary?.expected ? 'forecast.record.empty_expected' : 'forecast.record.empty_dated', { date: next })
          : t('forecast.record.empty')
        if (groups.length === 0) return <Empty description={emptyText} />
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {!hasResult && (
              // A note, not an alert: the status line is the page's one announcement.
              <Alert type="info" showIcon role="note" title={emptyText} />
            )}
            <ForecastMatrix
              resp={record.data}
              metric={view.metric}
              mode={mode}
              auditLink={admin}
              onOpen={(g, cp) => setDrawer({ g, cp, cell: g.cells[cp] })}
            />
          </div>
        )
      }
    }
  }

  // A reader the server turned away has no population to describe and nothing to filter or export:
  // only the title and the refusal, so the header cannot claim a population they cannot see.
  if (record.kind === 'unavailable') {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <Typography.Title level={4} style={{ margin: 0 }}>
          {t('forecast.record.title')}
        </Typography.Title>
        <Card>{body()}</Card>
      </div>
    )
  }

  // What a screen reader is told about the page's state. It is deliberately not the content: the
  // matrix is dozens of buttons, and a metric switch rewrites every one of them. A failure is left to
  // the alert, which announces itself.
  const status =
    record.kind === 'loading'
      ? t('common.loading')
      : record.kind === 'ready'
        ? t(hasResult ? 'forecast.record.status_ready' : 'forecast.record.status_empty')
        : ''

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'flex-start', gap: 12 }}>
        <div style={{ flex: '1 1 320px', minWidth: 0 }}>
          <Typography.Title level={4} style={{ margin: 0 }}>
            {t('forecast.record.title')}
          </Typography.Title>
          <Typography.Text type="secondary">{t('forecast.record.subtitle')}</Typography.Text>
          <div style={{ marginTop: 6 }}>
            <Typography.Text strong>{population}</Typography.Text>
            <br />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('forecast.disclaimer')}
            </Typography.Text>
          </div>
        </div>
        <Button icon={<DownloadOutlined />} disabled={!data || groups.length === 0} onClick={exportCsv}>
          {t('forecast.export.button')}
        </Button>
      </div>

      <Card>
        {/* The exploratory view is offered to internal readers only (decision V14). */}
        <Tabs
          activeKey={mode}
          onChange={(key) => patch({ mode: key })}
          items={[
            { key: 'month', label: t('forecast.tab.month') },
            ...(external ? [] : [{ key: 'custom', label: t('forecast.tab.custom') }]),
          ]}
        />

        {mode === 'custom' && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 12 }}>
            <Alert type="warning" showIcon closable={false} title={t('forecast.custom.banner')} />
            <Space wrap>
              <DatePicker.RangePicker value={rangeValue} onChange={setRange} allowEmpty={[true, true]} />
              <Segmented
                value={view.group}
                onChange={(v) => patch({ group: v as ForecastGrouping })}
                options={GROUPS.map((g) => ({ value: g, label: t(`forecast.group.${g}`) }))}
              />
            </Space>
          </div>
        )}

        {/* Four long labels do not fit a phone: scroll inside the card instead of hanging out of it. */}
        <div style={{ marginBottom: 12, maxWidth: '100%', overflowX: 'auto' }}>
          <Segmented
            value={view.metric}
            onChange={(v) => patch({ metric: v as ForecastMetric })}
            options={METRICS.map((m) => ({ value: m, label: t(`forecast.metric.${m}`) }))}
          />
        </div>

        <span className="rp-sr-only" role="status">{status}</span>
        {body()}
      </Card>

      <ForecastMethodology />

      <ForecastCellDrawer
        open={drawer !== null}
        onClose={() => setDrawer(null)}
        mode={mode}
        from={data?.range.from ?? view.from}
        to={data?.range.to ?? view.to}
        group={view.group}
        label={drawer?.g.label ?? ''}
        checkpoint={drawer?.cp ?? 'd5'}
        cell={drawer?.cell ?? null}
      />
    </div>
  )
}

function isBusy(error: unknown): boolean {
  return typeof error === 'object' && error !== null && (error as { code?: string }).code === 'forecast_busy'
}
