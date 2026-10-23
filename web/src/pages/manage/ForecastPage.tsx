import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, DatePicker, Descriptions, Empty, Segmented, Space, Spin, Tabs, Tag, Typography } from 'antd'
import { DownloadOutlined, SettingOutlined } from '@ant-design/icons'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import dayjs, { type Dayjs } from 'dayjs'
import { api, errText } from '../../api/client'
import type {
  ForecastCell,
  ForecastCheckpoint,
  ForecastGroup,
  ForecastGrouping,
  ForecastGroupsResponse,
  ForecastMetric,
  ForecastSettings,
} from '../../api/types'
import LoadGate from '../../components/LoadGate'
import { downloadCSV } from '../../lib/csv'
import { forecastCsv, groupsQuery, readView } from '../../lib/forecast'
import { readPrefetched } from '../../lib/prefetch'
import ForecastCellDrawer from '../../components/forecast/ForecastCellDrawer'
import ForecastMatrix from '../../components/forecast/ForecastMatrix'
import ForecastMethodology from '../../components/forecast/ForecastMethodology'
import ForecastSettingsModal from './ForecastSettingsModal'
import ForecastEvaluatorStatus from './ForecastEvaluatorStatus'
import ForecastAlerts from './ForecastAlerts'
import ForecastLedgerStart from './ForecastLedgerStart'

// 预测验证 (forecast verification): the admin page for the prediction ledger (ADR 0029).
//
// Two views over one engine (plan §8.5). 按月 is the RECORD: the calendar decides its groups, and a
// mature cell can never change again. 自定义区间 is EXPLORATORY: it answers "since the prompt change"
// or "this quarter", but because that freedom lets a reader search for a flattering window, every
// p in it is marked p* and the export says so in its header. The label is about how the range was
// chosen, not about the numbers: a custom range that equals whole months returns the same figures.
//
// The active view lives in the URL, so a view can be linked and reloaded. The server is the
// authority on every bound; the client checks only what it can tell instantly (an inverted date
// range), so a reader is not made to wait for a round trip to learn that.

// How stale a hover-prefetched answer may be and still be shown while the real request is in flight.
const WARM_MAX_AGE = 5 * 60_000

const GROUPS = ['week', 'month', 'quarter', 'all'] as const
const METRICS = ['spread', 'ic', 'hit', 'fill'] as const

export default function ForecastPage() {
  const { t } = useTranslation()
  const [sp, setSp] = useSearchParams()
  const view = useMemo(() => readView(sp), [sp])

  const [settings, setSettings] = useState<ForecastSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [ledgerOpen, setLedgerOpen] = useState(false)

  const [groups, setGroups] = useState<ForecastGroupsResponse | null>(null)
  const [groupsLoading, setGroupsLoading] = useState(false)
  const [groupsErr, setGroupsErr] = useState('')
  const [drawer, setDrawer] = useState<{ g: ForecastGroup; cp: ForecastCheckpoint; cell: ForecastCell } | null>(null)

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return api
      .get<ForecastSettings>('/api/admin/forecast')
      .then((st) => {
        setSettings(st)
        setLoaded(true)
      })
      .catch((e) => setLoadErr(errText(e, t)))
      .finally(() => setLoading(false))
  }

  // Empty deps on purpose: the request goes out once, and a reload behind a save calls load()
  // directly. `t` is stable in the app but not under a test double, and a dependency on it would
  // turn a mocked translation into a fetch loop.
  useEffect(() => {
    const warm = readPrefetched<ForecastSettings>('/api/admin/forecast', WARM_MAX_AGE)
    if (warm) {
      setSettings(warm)
      setLoaded(true)
      setLoading(false)
    }
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The matrix request. A custom view waits until both dates are present, so an empty tab does not
  // spend a permutation run on the whole ledger.
  useEffect(() => {
    if (view.mode === 'custom' && (!view.from || !view.to)) {
      setGroups(null)
      setGroupsErr('')
      return
    }
    let cancelled = false
    setGroupsLoading(true)
    setGroupsErr('')
    api
      .get<ForecastGroupsResponse>(`/api/admin/forecast/groups?${groupsQuery(view.mode, view.from, view.to, view.group)}`)
      .then((r) => {
        if (!cancelled) setGroups(r)
      })
      .catch((e) => {
        if (!cancelled) {
          setGroups(null)
          setGroupsErr(errText(e, t))
        }
      })
      .finally(() => {
        if (!cancelled) setGroupsLoading(false)
      })
    return () => {
      cancelled = true
    }
    // `t` is stable in the app; under a test double it is not, and depending on it turns every
    // render into another round trip.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view.mode, view.from, view.to, view.group])

  const patch = (next: Record<string, string>) => {
    const p = new URLSearchParams(sp)
    for (const [k, v] of Object.entries(next)) {
      if (v) p.set(k, v)
      else p.delete(k)
    }
    setSp(p, { replace: true })
  }

  const setMode = (mode: string) => patch({ mode })
  const setGroup = (group: ForecastGrouping) => patch({ group })
  const setMetric = (metric: ForecastMetric) => patch({ metric })
  const setRange = (v: [Dayjs | null, Dayjs | null] | null) => {
    patch({
      from: v?.[0]?.format('YYYY-MM-DD') ?? '',
      to: v?.[1]?.format('YYYY-MM-DD') ?? '',
    })
  }

  const rangeValue: [Dayjs | null, Dayjs | null] | null =
    view.from && view.to ? [dayjs(view.from), dayjs(view.to)] : null

  // Instant feedback only. The server owns the bounds; this catches the one mistake a reader makes
  // in the picker itself and spares a round trip for it.
  const clientRangeErr =
    view.mode === 'custom' && view.from && view.to && dayjs(view.from).isAfter(dayjs(view.to))
      ? t('forecast.err.range_inverted')
      : ''

  const exportCsv = () => {
    if (!groups) return
    const name = `forecast-${view.mode}-${groups.range.from || 'monthly'}-${groups.range.to || ''}.csv`
    downloadCSV(name, forecastCsv(groups, new Date().toISOString()))
  }

  const row = (label: string, value: React.ReactNode) => <Descriptions.Item label={label}>{value}</Descriptions.Item>

  return (
    <LoadGate loading={loading && !loaded} error={loaded ? undefined : loadErr} onRetry={load}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ flex: '1 1 320px', minWidth: 0 }}>
            <Typography.Title level={4} style={{ margin: 0 }}>
              {t('forecast.page.title')}
            </Typography.Title>
            <Typography.Text type="secondary">{t('forecast.page.subtitle')}</Typography.Text>
          </div>
          <Space wrap>
            <Button icon={<DownloadOutlined />} disabled={!groups || groups.groups.length === 0} onClick={exportCsv}>
              {t('forecast.export.button')}
            </Button>
            <Button icon={<SettingOutlined />} onClick={() => setSettingsOpen(true)}>
              {t('forecast.settings.button')}
            </Button>
            <Button onClick={() => setLedgerOpen(true)}>{t('forecast.population.button')}</Button>
          </Space>
        </div>

        {settings && <ForecastEvaluatorStatus settings={settings} onChanged={load} />}
        {settings && <ForecastAlerts settings={settings} onChanged={load} />}

        <Card title={t('forecast.eval.title')}>
          {settings && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              {settings.enabled ? (
                <Tag color="green">{t('forecast.eval.on')}</Tag>
              ) : (
                <Tag>{t('forecast.eval.off')}</Tag>
              )}
              {!settings.enabled && <Alert type="info" showIcon title={t('forecast.eval.offHint')} />}
              <Descriptions column={1} size="small" bordered>
                {row(t('forecast.eval.bench'), <Typography.Text code>{settings.bench}</Typography.Text>)}
                {row(t('forecast.eval.due'), settings.due_sets)}
                {row(t('forecast.eval.pending'), settings.pending)}
                {row(
                  t('forecast.eval.refused'),
                  settings.refused > 0 ? <Tag color="red">{settings.refused}</Tag> : 0,
                )}
                {row(
                  t('forecast.eval.unsettled'),
                  settings.unsettled_baseline > 0 ? (
                    <Tag color="orange">{settings.unsettled_baseline}</Tag>
                  ) : (
                    0
                  ),
                )}
              </Descriptions>
              <Typography.Text type="secondary">{t('forecast.eval.hint')}</Typography.Text>
              {(settings.unrecorded ?? [])
                .filter((u) => u.ok && (u.n > 0 || u.store_failed_30d > 0))
                .map((u) => (
                  <Alert
                    key={u.rtype}
                    type="warning"
                    showIcon
                    title={
                      <>
                        <Typography.Text strong>{u.rtype}</Typography.Text>{' '}
                        {t('forecast.cov.unrecorded_since', {
                          since: dayjs(u.since).utcOffset(8 * 60).format('YYYY-MM-DD'),
                          n: u.n,
                          m: u.store_failed_30d,
                        })}{' '}
                        <Link to="/manage/audit?action=predictions.store_failed">
                          {t('forecast.cov.unrecorded_audit')}
                        </Link>
                      </>
                    }
                  />
                ))}
            </div>
          )}
        </Card>

        <Card title={t('forecast.matrix.title')}>
          <Tabs
            activeKey={view.mode}
            onChange={setMode}
            items={[
              { key: 'month', label: t('forecast.tab.month') },
              { key: 'custom', label: t('forecast.tab.custom') },
            ]}
          />

          {view.mode === 'custom' && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 12 }}>
              <Alert type="warning" showIcon closable={false} title={t('forecast.custom.banner')} />
              <Space wrap>
                <DatePicker.RangePicker value={rangeValue} onChange={setRange} allowEmpty={[true, true]} />
                <Segmented
                  value={view.group}
                  onChange={(v) => setGroup(v as ForecastGrouping)}
                  options={GROUPS.map((g) => ({ value: g, label: t(`forecast.group.${g}`) }))}
                />
              </Space>
            </div>
          )}

          <div style={{ marginBottom: 12 }}>
            <Segmented
              value={view.metric}
              onChange={(v) => setMetric(v as ForecastMetric)}
              options={METRICS.map((m) => ({ value: m, label: t(`forecast.metric.${m}`) }))}
            />
          </div>

          {clientRangeErr && <Alert type="error" showIcon title={clientRangeErr} style={{ marginBottom: 12 }} />}
          {groupsErr && <Alert type="error" showIcon title={groupsErr} style={{ marginBottom: 12 }} />}
          {groupsLoading && <Spin />}
          {!groupsLoading && groups && groups.groups.length === 0 && (
            <Empty description={t('forecast.empty')} />
          )}
          {!groupsLoading && groups && groups.groups.length > 0 && (
            <ForecastMatrix
              resp={groups}
              metric={view.metric}
              mode={view.mode}
              onOpen={(g, cp) => setDrawer({ g, cp, cell: g.cells[cp] })}
            />
          )}
        </Card>
        <ForecastMethodology />
      </div>

      {settings && (
        <ForecastSettingsModal
          open={settingsOpen}
          value={settings}
          onClose={() => setSettingsOpen(false)}
          onSaved={load}
        />
      )}

      {settings && (
        <ForecastLedgerStart
          open={ledgerOpen}
          value={settings.population}
          onClose={() => setLedgerOpen(false)}
          onSaved={load}
        />
      )}

      <ForecastCellDrawer
        open={drawer !== null}
        onClose={() => setDrawer(null)}
        mode={view.mode}
        from={groups?.range.from ?? view.from}
        to={groups?.range.to ?? view.to}
        group={view.group}
        label={drawer?.g.label ?? ''}
        checkpoint={drawer?.cp ?? 'd5'}
        cell={drawer?.cell ?? null}
      />
    </LoadGate>
  )
}
