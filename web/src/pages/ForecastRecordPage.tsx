import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Card, Empty, Result, Skeleton, Space, Tabs, Typography, theme } from 'antd'
import { DownloadOutlined } from '@ant-design/icons'
import { useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { errText } from '../api/client'
import type { ForecastCell, ForecastCheckpoint, ForecastGroup, ForecastGroupsResponse } from '../api/types'
import { useAuth } from '../auth'
import ForecastCellDrawer from '../components/forecast/ForecastCellDrawer'
import ForecastEvaluatorPill from '../components/forecast/ForecastEvaluatorPill'
import ForecastMatrix from '../components/forecast/ForecastMatrix'
import ForecastMethodology from '../components/forecast/ForecastMethodology'
import ForecastRetentionNote from '../components/forecast/ForecastRetentionNote'
import ForecastSummary from '../components/forecast/ForecastSummary'
import ForecastToolbar from '../components/forecast/ForecastToolbar'
import { downloadCSV } from '../lib/csv'
import {
  benchRuns,
  civilDate,
  defaultRange,
  forecastCsv,
  groupsQuery,
  rangeProblem,
  readView,
  upcomingDate,
  visibleGroups,
  withRetention,
  type RangeLimits,
} from '../lib/forecast'
import { forecastLinkColor } from '../lib/forecastPresentation'
import { useForecastRecord } from '../lib/useForecastRecord'
import Quiet from '../components/forecast/Quiet'

// Forecast verification for readers: the record of what each report predicted against what
// the market did (ADR 0029, ADR 0039).
//
// This page asks the reader routes and nothing else, with one exception that is not the record: an
// administrator's evaluator pill reads the evaluator's own status, and its failure never reaches
// the matrix. Who may see what is decided by the server on every request; the navigation hint in
// `me.forecast` only decides what this page offers, so a stale hint produces a refusal that the page
// shows, not a leak.
//
// Two views over one engine. The monthly view is the RECORD: the calendar decides its groups, and a
// mature cell can never change again. The custom range is EXPLORATORY: because that freedom lets a
// reader search for a flattering window, every p in it is p* and the export says so in its header.
// External readers get the record only; a custom range would let them difference the ledger's volume
// out of it.
//
// Everything about the view lives in the URL (tab, range, grouping, metric, report type), so a link
// reproduces what the sender saw. What the response says about itself (the covered report types,
// the server's limits and clock, the ledger start) is kept between requests, because the custom tab
// needs it before it has asked for anything.

type ReaderMeta = Pick<ForecastGroupsResponse, 'rtypes' | 'limits' | 'generated_at' | 'ledger_start'>

const metaOf = (d: ForecastGroupsResponse): ReaderMeta => ({
  rtypes: d.rtypes, limits: d.limits, generated_at: d.generated_at, ledger_start: d.ledger_start,
})

const limitsOf = (m: ReaderMeta | undefined): RangeLimits | undefined =>
  m?.limits ? { maxMonths: m.limits.max_range_months, maxWeekGroups: m.limits.max_week_groups } : undefined

export default function ForecastRecordPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { user, admin, forecast } = useAuth()
  const { token } = theme.useToken()
  const [sp, setSp] = useSearchParams()
  const view = useMemo(() => readView(sp), [sp])

  // The hint is a navigation aid, so an absent one (an older server) reads as no access. An
  // administrator is never scoped.
  const scope = admin ? 'all' : (forecast?.scope ?? 'none')
  const allowed = admin || scope === 'all' || scope === 'own'
  const external = !!forecast?.external
  const own = !admin && scope === 'own'
  const mode = external ? 'month' : view.mode

  // Everything that changes whose numbers these are, so a held result can never outlive it.
  const identity = [user, scope, external, forecast?.ou ?? ''].join('|')

  const patch = useCallback(
    (next: Record<string, string>) => {
      const p = new URLSearchParams(sp)
      for (const [k, v] of Object.entries(next)) {
        if (v) p.set(k, v)
        else p.delete(k)
      }
      setSp(p, { replace: true })
    },
    [sp, setSp],
  )

  // What the last answer said about itself. It outlives the answer (a tab switch is a new request)
  // and dies with the identity it described.
  const [heldMeta, setHeldMeta] = useState<ReaderMeta>()
  useEffect(() => setHeldMeta(undefined), [identity])

  // A custom view waits until both dates are present, so an empty tab does not spend a permutation
  // run on the whole ledger, and until the range is one the server would accept. Whether to ask is
  // decided from what earlier answers said about the limits; the answer on screen, below, is what
  // the controls and the defaults use.
  const problem = mode === 'custom' ? rangeProblem(view.from, view.to, view.group, limitsOf(heldMeta)) : ''
  const query =
    mode === 'month'
      ? groupsQuery('month', '', '', 'month', view.rtype)
      : view.from && view.to && !problem
        ? groupsQuery('custom', view.from, view.to, view.group, view.rtype)
        : null
  const record = useForecastRecord(query, identity, allowed)

  const data: ForecastGroupsResponse | undefined = record.kind === 'ready' ? record.data : undefined
  useEffect(() => {
    if (data) setHeldMeta(metaOf(data))
  }, [data])
  const meta = data ? metaOf(data) : heldMeta
  const limits = limitsOf(meta)

  // The panel's own clock when the server has told us, the browser's until then.
  const today = civilDate(meta?.generated_at) || civilDate(new Date().toISOString())
  const ledgerStart = civilDate(meta?.ledger_start) || undefined

  // A custom tab opens on a range, never a blank form.
  const openRange = () => {
    const r = defaultRange(today, ledgerStart, limits)
    return { from: r.from, to: r.to }
  }
  const changeMode = (key: string) => {
    if (key === 'custom' && !view.from && !view.to) patch({ mode: key, ...openRange() })
    else patch({ mode: key })
  }
  const arrived = useRef(false)
  useEffect(() => {
    if (arrived.current) return
    arrived.current = true
    if (allowed && !external && view.mode === 'custom' && !view.from && !view.to) patch(openRange())
    // Once, on arrival: later edits to the range are the reader's.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // With several covered report types the server cannot tell us which it drew when the link names
  // none, so the page names one and asks again rather than label an answer it cannot vouch for.
  const rtypes = meta?.rtypes
  useEffect(() => {
    if (rtypes && rtypes.length > 1 && !view.rtype) patch({ rtype: rtypes[0] })
  }, [rtypes, view.rtype, patch])

  const [drawer, setDrawer] = useState<{ g: ForecastGroup; cp: ForecastCheckpoint; cell: ForecastCell } | null>(null)
  useEffect(() => setDrawer(null), [query, identity])

  // A month the retention policy removed from a custom range is gone, not under the gate: the groups
  // are read through the response's own disclosure, so everything below (the "held back" note, the
  // matrix, the toolbar's benchmark line) agrees about which cells are waiting to grow and which are
  // not coming back.
  const groups = useMemo(() => withRetention(data?.groups ?? [], data?.excluded_months), [data])
  const { shown, hidden } = visibleGroups(groups, mode, view.older)
  // A cell with a result is a mature one. Open, pending, small and purged cells carry state and no
  // numbers, so a record made only of them has nothing to show yet however many groups it lists.
  const hasResult = groups.some((g) => Object.values(g.cells ?? {}).some((c) => c?.state === 'mature'))
  const heldBack = !hasResult && groups.some((g) => Object.values(g.cells ?? {}).some((c) => c?.state === 'small'))
  const paused = data?.evaluator?.mode === '0' || data?.evaluator?.mode === 'dry'

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

  // antd draws the selected tab's label in its primary colour, which is under AA on both surfaces; the
  // link colour used elsewhere on this page is the step that clears it in each theme.
  const tabLabel = (key: string, text: string) => (
    <span style={key === mode ? { color: forecastLinkColor(token) } : undefined}>{text}</span>
  )

  const exportCsv = () => {
    if (!data) return
    const name = `forecast-${data.mode}-${data.range.from || 'monthly'}-${data.range.to || ''}.csv`
    downloadCSV(
      name,
      // The whole record, not only the months on screen: folding older months is a way of looking,
      // and a file that silently lost them would be a different record.
      forecastCsv(data, new Date().toISOString(), {
        population,
        disclaimer: t('forecast.disclaimer'),
        publicationGate: t('forecast.csv.header.publication_gate'),
        // An external reader with the all scope must never export volume. The export also follows
        // the response itself: a group without coverage means the server withheld it.
        hideCounts: external && scope === 'all',
        rtype: view.rtype || (rtypes?.length === 1 ? rtypes[0] : undefined),
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
            {problem === 'inverted' && <Alert type="error" showIcon title={t('forecast.err.range_inverted')} />}
            {problem === 'too_long' && (
              <Alert type="error" showIcon title={t('forecast.err.range_too_long', { months: limits?.maxMonths })} />
            )}
            {problem === 'week_too_long' && (
              <Alert type="error" showIcon title={t('forecast.err.week_range_too_long', { weeks: limits?.maxWeekGroups })} />
            )}
            {!problem && <Empty description={t('forecast.record.pick_range')} />}
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
            action={
              <Space>
                {/* A report type the ledger does not cover cannot be retried into working. */}
                {isBadRtype(record.error) && (
                  <Button size="small" onClick={() => patch({ rtype: '' })}>{t('forecast.record.default_rtype')}</Button>
                )}
                <Button size="small" onClick={record.retry}>{t('common.retry')}</Button>
              </Space>
            }
          />
        )
      case 'ready': {
        // "No result yet" is a property of the cells, not of the group list: a young ledger has its
        // open month as a group, and every cell in it is waiting. Without metadata (an external
        // reader with the all scope) there is no date to give, and the page does not make one up.
        const next = upcomingDate(record.data.summary?.next_result_not_before, record.data.generated_at)
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
            {heldBack && <Alert type="info" showIcon role="note" title={t('forecast.record.not_enough')} />}
            {/* Only the exploratory view loses months this way: the record draws a purged month as a
                group of its own. */}
            {mode === 'custom' && <ForecastRetentionNote months={record.data.excluded_months} titleKey="forecast.custom.excluded_title" />}
            <ForecastMatrix
              resp={{ ...record.data, groups: shown }}
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
      <Card className="rp-fc-card">
        <div data-part="header" style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ flex: '1 1 320px', minWidth: 0 }}>
            <Typography.Title level={4} style={{ margin: 0 }}>
              {t('forecast.record.title')}
            </Typography.Title>
            <Quiet>{t('forecast.record.subtitle')}</Quiet>
            <div style={{ marginTop: 6 }}>
              <Typography.Text strong>{population}</Typography.Text>
              <br />
              <Quiet style={{ fontSize: 12 }}>
                {t('forecast.disclaimer')}
              </Quiet>
            </div>
          </div>
          <Space wrap>
            {/* The evaluator is an operator's concern. Its status is its own request and its own
                failure: the record below never waits for it. */}
            {admin && <ForecastEvaluatorPill />}
            <Button icon={<DownloadOutlined />} disabled={!data || groups.length === 0} onClick={exportCsv}>
              {t('forecast.export.button')}
            </Button>
          </Space>
        </div>

        {data?.summary && (
          <div style={{ marginTop: 12 }}>
            <ForecastSummary
              summary={{ ...data.summary, next_result_not_before: upcomingDate(data.summary.next_result_not_before, data.generated_at) }}
            />
          </div>
        )}

        {/* The exploratory view is offered to internal readers only (decision V14). */}
        <Tabs
          activeKey={mode}
          onChange={changeMode}
          items={[
            { key: 'month', label: tabLabel('month', t('forecast.tab.month')) },
            ...(external ? [] : [{ key: 'custom', label: tabLabel('custom', t('forecast.tab.custom')) }]),
          ]}
        />

        <ForecastToolbar
          mode={mode}
          metric={view.metric}
          rtypes={rtypes}
          rtype={view.rtype}
          runs={benchRuns(shown)}
          total={groups.length}
          hidden={hidden}
          showOlder={view.older}
          from={view.from}
          to={view.to}
          group={view.group}
          limits={limits}
          today={today}
          ledgerStart={ledgerStart}
          onChange={patch}
        />

        {mode === 'custom' && (
          <Alert type="warning" showIcon closable={false} title={t('forecast.custom.banner')} style={{ marginBottom: 12 }} />
        )}
        {paused && <Alert type="warning" showIcon role="note" title={t('forecast.phase.settlement_paused')} style={{ marginBottom: 12 }} />}

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
        rtype={view.rtype}
      />
    </div>
  )
}

function isBusy(error: unknown): boolean {
  return typeof error === 'object' && error !== null && (error as { code?: string }).code === 'forecast_busy'
}

function isBadRtype(error: unknown): boolean {
  return typeof error === 'object' && error !== null && (error as { code?: string }).code === 'bad_rtype'
}
