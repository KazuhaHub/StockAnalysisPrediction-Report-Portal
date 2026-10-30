import { useEffect, useRef, useState } from 'react'
import { Alert, App, Button, Card, ConfigProvider, Divider, Empty, InputNumber, Select, Space, Switch, Table, Tag, TimePicker, Typography, theme } from 'antd'
import { AuditOutlined, DatabaseOutlined, ExportOutlined, FileTextOutlined, FundProjectionScreenOutlined, HistoryOutlined, KeyOutlined, MessageOutlined, ThunderboltOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import type { LoadOpts } from '../../lib/loadFailure'
import type { CleanupConfig, CleanupResult, CleanupRun, CleanupUsage, CleanupUsageCategory } from '../../api/types'
import LoadGate from '../../components/LoadGate'
import SettingsWorkspace, { SettingsPanel } from './SettingsWorkspace'
import SettingsSaveBar from './SettingsSaveBar'
import CompactNumberInput from '../../components/CompactNumberInput'
import LoginHistoryRetentionCard from '../../components/LoginHistoryRetentionCard'

// Storage management console (docs/adr/0017-storage-cleanup.md): a per-category usage dashboard (icon
// cards + a proportion bar), a self-explanatory manual cleanup (the button names what and how old),
// an optional daily/weekly/monthly scheduled retention pass, and the cleanup_runs audit history.
// Reports (core content) are fail-closed, floored, and guarded by a live-count confirmation.

// Category identity colors — the dataviz categorical palette (validated CVD-safe as an ordered set;
// identity is also carried by the icon + name, satisfying the relief rule for the sub-3:1 slots).
const CAT_COLOR: Record<string, { light: string; dark: string }> = {
  batch: { light: '#2a78d6', dark: '#3987e5' }, // blue
  tokens: { light: '#1baf7a', dark: '#199e70' }, // aqua
  reports: { light: '#eda100', dark: '#c98500' }, // yellow
  chat: { light: '#008300', dark: '#008300' }, // green
  audit: { light: '#8452d6', dark: '#9366e0' }, // violet
  revisions: { light: '#c8447a', dark: '#d65a8c' }, // magenta — the next slot in the ordered set
  predictions: { light: '#6b7fd6', dark: '#8395e0' }, // indigo
  exports: { light: '#0f8f9e', dark: '#2eb0bf' }, // teal — generated export files
}
const CAT_ICON: Record<string, React.ReactNode> = {
  batch: <ThunderboltOutlined />,
  tokens: <KeyOutlined />,
  reports: <FileTextOutlined />,
  chat: <MessageOutlined />,
  audit: <AuditOutlined />,
  revisions: <HistoryOutlined />,
  predictions: <FundProjectionScreenOutlined />,
  exports: <ExportOutlined />,
}

// fmtBytes renders an approximate byte count in human units.
function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

// isDarkSurface decides light/dark from the resolved antd container color, so the category hues track
// whichever theme is active (not the OS preference).
function isDarkSurface(c: string): boolean {
  const m = /^#?([0-9a-fA-F]{6})$/.exec(c.trim())
  if (!m) return false
  const n = parseInt(m[1], 16)
  const r = (n >> 16) & 255
  const g = (n >> 8) & 255
  const b = n & 255
  return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255 < 0.5
}

export default function StoragePage() {
  const { t } = useTranslation()
  const { failure, catchLoad, retryKey } = useLoadFailure()
  const { message, modal } = App.useApp()
  const { token } = theme.useToken()
  const dark = isDarkSurface(token.colorBgContainer)
  const catColor = (k: string) => (CAT_COLOR[k] ? (dark ? CAT_COLOR[k].dark : CAT_COLOR[k].light) : token.colorPrimary)

  const [freq, setFreq] = useState<CleanupConfig['freq']>('off')
  const [time, setTime] = useState('03:00')
  const [weekday, setWeekday] = useState(1)
  const [monthday, setMonthday] = useState(1)
  const [loading, setLoading] = useState(true)
  // Separate from `loading`: the reloads that follow a save or a cleanup run must refresh the page
  // in place. Replacing a page that has just saved successfully with a full-page load error —
  // because the refresh behind it happened to fail — loses the form the admin is looking at.
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [auditEnabled, setAuditEnabled] = useState(false)
  const [auditDays, setAuditDays] = useState(365)
  const [batchEnabled, setBatchEnabled] = useState(false)
  const [batchDays, setBatchDays] = useState(90)
  const [tokensEnabled, setTokensEnabled] = useState(false)
  const [tokensGraceDays, setTokensGraceDays] = useState(30)
  const [reportsEnabled, setReportsEnabled] = useState(false)
  const [reportsDays, setReportsDays] = useState(730)
  const [batchFloor, setBatchFloor] = useState(7)
  const [reportsFloor, setReportsFloor] = useState(365)
  const [auditFloor, setAuditFloor] = useState(30)
  const [revisionsEnabled, setRevisionsEnabled] = useState(false)
  const [revisionsDays, setRevisionsDays] = useState(180)
  const [revisionsFloor, setRevisionsFloor] = useState(14)
  // 0 is a real value here, not "unset": it means keep every version, and it is the shipped state.
  const [revisionsKeep, setRevisionsKeep] = useState(0)
  // The forecast ledger. 1095 days (three years) is past h12 for every set that can be fully
  // settled, so the default never truncates a live horizon.
  const [predictionsEnabled, setPredictionsEnabled] = useState(false)
  const [predictionsDays, setPredictionsDays] = useState(1095)
  const [predictionsFloor, setPredictionsFloor] = useState(180)
  const [revisionsKeepMax, setRevisionsKeepMax] = useState(1000)
  const [cfg, setCfg] = useState<CleanupConfig | null>(null) // last-saved view, drives the usage cards
  const [usage, setUsage] = useState<CleanupUsage | null>(null)
  const [history, setHistory] = useState<CleanupRun[]>([])
  const [historyError, setHistoryError] = useState('')
  const [historyLoaded, setHistoryLoaded] = useState(false)
  const [section, setSection] = useState('usage')
  const [saving, setSaving] = useState(false)
  const values = { freq, time, weekday, monthday, batch_enabled: batchEnabled, batch_days: batchDays,
    tokens_enabled: tokensEnabled, tokens_grace_days: tokensGraceDays, reports_enabled: reportsEnabled,
    reports_days: reportsDays, audit_enabled: auditEnabled, audit_days: auditDays, revisions_enabled: revisionsEnabled,
    revisions_days: revisionsDays, revisions_keep: revisionsKeep, predictions_enabled: predictionsEnabled, predictions_days: predictionsDays }
  const dirty = !!cfg && Object.entries(values).some(([key, value]) => cfg[key as keyof CleanupConfig] !== value)
  const restore = (r: CleanupConfig) => {
    setFreq(r.freq); setTime(r.time); setWeekday(r.weekday); setMonthday(r.monthday)
    setBatchEnabled(r.batch_enabled); setBatchDays(r.batch_days); setTokensEnabled(r.tokens_enabled)
    setTokensGraceDays(r.tokens_grace_days); setReportsEnabled(r.reports_enabled); setReportsDays(r.reports_days)
    setAuditEnabled(r.audit_enabled); setAuditDays(r.audit_days); setRevisionsEnabled(r.revisions_enabled)
    setRevisionsDays(r.revisions_days); setRevisionsKeep(r.revisions_keep)
    setPredictionsEnabled(r.predictions_enabled); setPredictionsDays(r.predictions_days)
  }

  // The schedule fields start at this file's defaults — cleanup off, 365-day audit retention —
  // and the form is live, so until the server has answered nothing here may be shown as if it
  // were the configured policy.
  const loadConfig = (opts?: LoadOpts) => {
    setLoadErr('')
    return api
      .get<CleanupConfig>('/api/admin/cleanup/config')
      .then((r) => {
        setCfg(r)
        setFreq(r.freq)
        setTime(r.time)
        setWeekday(r.weekday)
        setMonthday(r.monthday)
        setBatchEnabled(r.batch_enabled)
        setBatchDays(r.batch_days)
        setTokensEnabled(r.tokens_enabled)
        setTokensGraceDays(r.tokens_grace_days)
        setReportsEnabled(r.reports_enabled)
        setReportsDays(r.reports_days)
        setAuditEnabled(r.audit_enabled)
        setAuditDays(r.audit_days)
        setBatchFloor(r.batch_floor)
        setReportsFloor(r.reports_floor)
        setAuditFloor(r.audit_floor)
        setRevisionsEnabled(r.revisions_enabled)
        setRevisionsDays(r.revisions_days)
        setRevisionsFloor(r.revisions_floor)
        setRevisionsKeep(r.revisions_keep)
        setRevisionsKeepMax(r.revisions_keep_max)
        setPredictionsEnabled(r.predictions_enabled)
        setPredictionsDays(r.predictions_days)
        setPredictionsFloor(r.predictions_floor)
      })
      .then(() => setLoaded(true))
      .catch((e) => onFail(e, opts))
  }
  // A lost connection is not an answer: the page keeps its spinner and loads again when the
  // connection returns.
  const offline = useRef(false)
  const onFail = (e: unknown, opts?: LoadOpts) => {
    const text = failure(e, opts)
    if (text === null) offline.current = true
    else setLoadErr(text)
  }
  const loadUsage = () => api.get<CleanupUsage>('/api/admin/cleanup/usage').then(setUsage)
  const loadHistory = () => {
    setHistoryError('')
    return api.get<{ runs: CleanupRun[] }>('/api/admin/cleanup/history')
      .then((r) => { setHistory(r.runs ?? []); setHistoryLoaded(true) })
      .catch(catchLoad(setHistoryError))
  }

  // Usage belongs inside the gate with the config: the card above reads `usage?.db_bytes ?? 0`,
  // and "0 B" over an empty proportion bar is this page's headline number reporting a database
  // it has not measured. History stays outside — its own table spins.
  const load = () => {
    setLoading(true)
    setLoadErr('')
    loadHistory().catch(() => {})
    offline.current = false
    return Promise.all([loadConfig(), loadUsage().catch(onFail)]).finally(() => { if (!offline.current) setLoading(false) })
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [retryKey])

  const save = async () => {
    setSaving(true)
    try {
      await api.post('/api/admin/cleanup/config', values)
      setCfg((current) => current && ({ ...current, ...values }))
      message.success(t('common.saved'))
      await loadConfig({ quiet: true })
      await loadUsage().catch(() => {})
    } catch (e) { message.error(errText(e, t)) } finally { setSaving(false) }
  }

  const doRun = async (targets: string[]) => {
    const r = await api.post<CleanupResult>('/api/admin/cleanup/run', { targets })
    // Every target the run could have touched. Leaving audit out reported an audit purge as
    // "cleaned 0 of everything" — the one category whose rows cannot be regenerated.
    message.success(t('storage.cleaned', {
      batch: r.batch, reports: r.reports, tokens: r.tokens, audit: r.audit ?? 0,
      revisions: r.revisions ?? 0,
    }))
    // The rows are what was deleted; this is what the disk got back, which is the number the pass
    // was run for. Postgres always answers 0 and says why, rather than leaving it looking broken.
    if (r.driver === 'postgres') {
      message.info(t('storage.reclaimPostgres'))
    } else if ((r.reclaimed ?? 0) > 0) {
      message.success(t('storage.reclaimed', { size: fmtBytes(r.reclaimed) }))
    }
    loadUsage().catch(() => {})
    loadHistory().catch(() => {})
  }

  // Reports (core content): preview the live count, then a strict confirm before arming or running.
  const guardReports = async (onConfirm: () => void) => {
    const r = await api.post<CleanupResult>('/api/admin/cleanup/preview', { targets: ['reports'] })
    modal.confirm({
      title: t('storage.confirmReportsTitle'),
      content: t('storage.confirmReportsBody', { count: r.reports, days: cfg?.reports_days ?? reportsDays }),
      okText: t('storage.doClean'),
      cancelText: t('common.cancel'),
      okButtonProps: { danger: true },
      onOk: onConfirm,
    })
  }

  // The ledger is the second danger-zone target, and for a stronger reason than reports: a report
  // that is deleted can be re-ingested from the workflow that produced it, while a set records what
  // was claimed at a moment that has passed. Confirmed like reports, with its own wording.
  const guardPredictions = async (onConfirm: () => void) => {
    const r = await api.post<CleanupResult>('/api/admin/cleanup/preview', { targets: ['predictions'] })
    modal.confirm({
      title: t('storage.confirmPredictionsTitle'),
      content: t('storage.confirmPredictionsBody', { count: r.predictions, days: cfg?.predictions_days ?? predictionsDays }),
      okText: t('storage.doClean'),
      cancelText: t('common.cancel'),
      okButtonProps: { danger: true },
      onOk: onConfirm,
    })
  }

  const onTogglePredictions = (checked: boolean) => {
    if (!checked) {
      setPredictionsEnabled(false)
      return
    }
    guardPredictions(() => setPredictionsEnabled(true))
  }

  const onToggleReports = (checked: boolean) => {
    if (!checked) {
      setReportsEnabled(false)
      return
    }
    guardReports(() => setReportsEnabled(true))
  }

  // Manual "clean now" for one category: the button already says what & how old; the confirm restates
  // the live count. Reports route through the stricter guardReports path.
  const cleanCategory = async (key: string, days: number) => {
    if (key === 'reports') {
      guardReports(() => doRun(['reports']))
      return
    }
    if (key === 'predictions') {
      guardPredictions(() => doRun(['predictions']))
      return
    }
    const r = await api.post<CleanupResult>('/api/admin/cleanup/preview', { targets: [key] })
    // Keyed, not a ternary. With two targets a two-way choice was total; the third and fourth
    // silently fell through to the tokens count, so the audit confirm promised to delete however
    // many TOKENS had expired.
    const n = r[key as 'batch' | 'tokens' | 'reports' | 'audit' | 'revisions' | 'predictions'] ?? 0
    modal.confirm({
      title: t('storage.confirmTitle'),
      content: t('storage.confirmBody', { n, days, cat: t(`storage.cat.${key}`) }),
      okText: t('storage.doClean'),
      cancelText: t('common.cancel'),
      okButtonProps: { danger: true },
      onOk: () => doRun([key]),
    })
  }

  // retention/grace currently in effect for each category (last-saved, so labels match what a run does)
  // Keyed, not a ternary chain, for the reason recorded above cleanCategory: a chain's fallthrough
  // silently answers for whichever category is last, so every new one starts out mislabelled.
  const catDays = (key: string) =>
    ({
      batch: cfg?.batch_days ?? batchDays,
      tokens: cfg?.tokens_grace_days ?? tokensGraceDays,
      audit: cfg?.audit_days ?? auditDays,
      revisions: cfg?.revisions_days ?? revisionsDays,
      reports: cfg?.reports_days ?? reportsDays,
      predictions: cfg?.predictions_days ?? predictionsDays,
    })[key] ?? 0

  const cats = usage?.categories ?? []
  const totalBytes = cats.reduce((s, c) => s + c.bytes, 0)

  const renderCard = (c: CleanupUsageCategory) => {
    const color = catColor(c.key)
    const days = catDays(c.key)
    return (
      <div key={c.key} style={{ border: `1px solid ${token.colorBorderSecondary}`, borderRadius: 10, padding: 16, display: 'flex', flexDirection: 'column', gap: 10 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ width: 36, height: 36, borderRadius: 9, display: 'grid', placeItems: 'center', background: color + '22', color, fontSize: 18 }}>{CAT_ICON[c.key]}</span>
          <Typography.Text strong>{t(`storage.cat.${c.key}`)}</Typography.Text>
        </div>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
          <Typography.Title level={4} style={{ margin: 0 }}>
            {fmtBytes(c.bytes)}
          </Typography.Title>
          <Typography.Text type="secondary">{t('storage.rowsN', { n: c.rows })}</Typography.Text>
        </div>
        {c.key === 'chat' ? (
          <Typography.Text type="secondary">{t('storage.ruleChat')}</Typography.Text>
        ) : c.key === 'exports' ? (
          <Typography.Text type="secondary">{t('storage.ruleExports')}</Typography.Text>
        ) : c.eligible > 0 ? (
          <>
            <Tag color="orange" style={{ width: 'fit-content' }}>
              {t('storage.eligibleN', { n: c.eligible })}
            </Tag>
            <Button
              size="small"
              danger={c.key === 'reports' || c.key === 'predictions'}
              type={c.key === 'reports' || c.key === 'predictions' ? 'default' : 'primary'}
              ghost={c.key !== 'reports' && c.key !== 'predictions'}
              onClick={() => cleanCategory(c.key, days)}
              style={{ width: 'fit-content' }}
            >
              {t('storage.act', { days, cat: t(`storage.cat.${c.key}`) })}
            </Button>
          </>
        ) : (
          <Typography.Text type="secondary">{t('storage.noCleanup')}</Typography.Text>
        )}
      </div>
    )
  }

  const freqOptions = [
    { value: 'off', label: t('storage.freqOff') },
    { value: 'daily', label: t('run.freq.daily') },
    { value: 'weekly', label: t('run.freq.weekly') },
    { value: 'monthly', label: t('run.freq.monthly') },
  ]
  const weekdayOptions = [0, 1, 2, 3, 4, 5, 6].map((d) => ({ value: d, label: t(`run.weekday.${d}`) }))

  const row = (label: string, control: React.ReactNode, hint?: string) => (
    <Space wrap>
      <span style={{ display: 'inline-block', minWidth: 120 }}>{label}</span>
      {control}
      {hint ? <Typography.Text type="secondary">{hint}</Typography.Text> : null}
    </Space>
  )

  const histCols = [
    { title: t('storage.histTime'), dataIndex: 'ran_at' },
    { title: t('storage.histTrigger'), dataIndex: 'trigger', render: (v: string) => t(v === 'schedule' ? 'storage.triggerSchedule' : 'storage.triggerManual') },
    {
      title: t('storage.histResult'),
      key: 'result',
      render: (_: unknown, r: CleanupRun) =>
        t('storage.resultLine', {
          batch: r.batch_deleted,
          tokens: r.tokens_deleted,
          reports: r.reports_deleted,
          audit: r.audit_deleted ?? 0,
          revisions: r.revisions_deleted ?? 0,
          predictions: r.predictions_deleted ?? 0,
        }) + ((r.bytes_reclaimed ?? 0) > 0 ? ` · ${t('storage.reclaimedShort', { size: fmtBytes(r.bytes_reclaimed) })}` : ''),
    },
    {
      title: t('storage.histStatus'),
      dataIndex: 'ok',
      render: (ok: boolean, r: CleanupRun) => (ok ? <Tag color="green">{t('storage.statusOk')}</Tag> : <Tag color="red">{r.error || t('storage.statusFailed')}</Tag>),
    },
  ]

  return (
    <LoadGate loading={loading && !loaded} error={loaded ? undefined : loadErr} onRetry={load}>
    <>
    {loaded && loadErr && <Alert type="error" showIcon title={loadErr} style={{ marginBottom: 16 }} />}
    <SettingsWorkspace active={section} onChange={setSection} sections={[
      { key: 'usage', label: t('manage.operation.usage') }, { key: 'policy', label: t('manage.operation.retention') },
      { key: 'login', label: t('manage.operation.loginHistory') }, { key: 'history', label: t('manage.operation.cleanupHistory') },
    ]}>
      <SettingsPanel section="usage">
      <Card title={t('storage.usageTitle')}>
        <Space orientation="vertical" size={14} style={{ width: '100%' }}>
          {/* Total + proportion bar: which category takes the space, at a glance. */}
          <Space align="center" size={8}>
            <DatabaseOutlined style={{ fontSize: 18, color: token.colorTextSecondary }} />
            <Typography.Text strong>{fmtBytes(usage?.db_bytes ?? 0)}</Typography.Text>
            <Typography.Text type="secondary">
              {t('storage.dbTotal')} · {t('storage.approxNote')}
            </Typography.Text>
          </Space>
          <div style={{ display: 'flex', gap: 2, height: 12, width: '100%' }}>
            {totalBytes > 0 ? (
              cats
                .filter((c) => c.bytes > 0)
                .map((c) => (
                  <div
                    key={c.key}
                    title={`${t(`storage.cat.${c.key}`)} ${fmtBytes(c.bytes)}`}
                    style={{ flex: `${Math.max(2, (c.bytes / totalBytes) * 100)} 0 0`, background: catColor(c.key), borderRadius: 3, minWidth: 6 }}
                  />
                ))
            ) : (
              <div style={{ flex: 1, background: token.colorFillSecondary, borderRadius: 3 }} />
            )}
          </div>
          <Space wrap size={16}>
            {cats.map((c) => (
              <span key={c.key} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <span style={{ width: 10, height: 10, borderRadius: 3, background: catColor(c.key), display: 'inline-block' }} />
                <Typography.Text>{t(`storage.cat.${c.key}`)}</Typography.Text>
                <Typography.Text type="secondary">{fmtBytes(c.bytes)}</Typography.Text>
              </span>
            ))}
          </Space>

          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 12 }}>{cats.map(renderCard)}</div>
        </Space>
      </Card>
      </SettingsPanel>
      <SettingsPanel section="login">
      <LoginHistoryRetentionCard />
      </SettingsPanel>
      <SettingsPanel section="policy">
      <Card title={t('storage.title')}>
        <ConfigProvider componentDisabled={saving}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12, width: '100%' }}>
          <Divider style={{ margin: '4px 0' }} titlePlacement="left" plain>
            {t('storage.scheduleTitle')}
          </Divider>
          <Typography.Text type="secondary">{t('storage.scheduleHint')}</Typography.Text>
          {row(t('storage.freq'), <Select style={{ width: 160 }} value={freq} onChange={(v) => setFreq(v)} options={freqOptions} />)}
          {freq !== 'off' && (
            <Space wrap>
              <span style={{ display: 'inline-block', minWidth: 120 }}>{t('storage.time')}</span>
              <TimePicker
                format="HH:mm"
                allowClear={false}
                needConfirm={false}
                value={dayjs('2000-01-01 ' + time)}
                onChange={(d) => setTime(d ? d.format('HH:mm') : '03:00')}
                aria-label={t('storage.time')}
              />
            </Space>
          )}
          {freq === 'weekly' && row(t('storage.weekday'), <Select style={{ width: 160 }} value={weekday} onChange={setWeekday} options={weekdayOptions} />)}
          {freq === 'monthly' && row(t('storage.monthday'), <InputNumber min={1} max={31} value={monthday} onChange={(v) => setMonthday(v ?? 1)} />)}

          <Divider style={{ margin: '4px 0' }} titlePlacement="left" plain>
            {t('storage.targetsTitle')}
          </Divider>
          {row(
            t('storage.batchTarget'),
            <Space>
              <Switch checked={batchEnabled} onChange={setBatchEnabled} />
              <CompactNumberInput min={batchFloor} value={batchDays} onChange={(v) => setBatchDays(v ?? batchFloor)} after={t('batch.admin.days')} />
            </Space>,
            t('storage.batchHint'),
          )}
          {row(
            t('storage.tokensTarget'),
            <Space>
              <Switch checked={tokensEnabled} onChange={setTokensEnabled} />
              <CompactNumberInput min={0} value={tokensGraceDays} onChange={(v) => setTokensGraceDays(v ?? 0)} after={t('batch.admin.days')} />
            </Space>,
            t('storage.tokensHint'),
          )}

          {row(
            t('storage.auditTarget'),
            <Space>
              <Switch checked={auditEnabled} onChange={setAuditEnabled} />
              <CompactNumberInput min={auditFloor} value={auditDays} onChange={(v) => setAuditDays(v ?? auditFloor)} after={t('batch.admin.days')} />
            </Space>,
            // Off means never delete, which is a real choice for an audit trail in a way it is not
            // for batch history — so the hint says so rather than only naming the floor.
            t('storage.auditHint', { n: auditFloor }),
          )}

          {row(
            t('storage.revisionsTarget'),
            <Space>
              <Switch checked={revisionsEnabled} onChange={setRevisionsEnabled} />
              <CompactNumberInput
                min={revisionsFloor}
                value={revisionsDays}
                onChange={(v) => setRevisionsDays(v ?? revisionsFloor)}
                after={t('batch.admin.days')}
              />
            </Space>,
            t('storage.revisionsHint', { n: revisionsFloor }),
          )}
          {row(
            t('storage.revisionsKeep'),
            <InputNumber
              min={0}
              max={revisionsKeepMax}
              value={revisionsKeep}
              onChange={(v) => setRevisionsKeep(v ?? 0)}
            />,
            // The cap is a count enforced when a report is saved, not an age enforced by the pass —
            // so it applies whether or not the switch above is on, and the hint has to say so.
            revisionsKeep > 0
              ? t('storage.revisionsKeepHint', { count: revisionsKeep })
              : t('storage.revisionsKeepUnlimited'),
          )}

          <Divider style={{ margin: '4px 0' }} titlePlacement="left" plain>
            {t('storage.reportsTarget')}
          </Divider>
          <Alert type="warning" showIcon title={t('storage.reportsDanger')} description={t('storage.reportsWarn')} />
          {row(
            t('storage.enable'),
            <Space>
              <Switch checked={reportsEnabled} onChange={onToggleReports} />
              <CompactNumberInput min={reportsFloor} value={reportsDays} onChange={(v) => setReportsDays(v ?? reportsFloor)} after={t('batch.admin.days')} />
            </Space>,
            t('storage.floorHint', { n: reportsFloor }),
          )}

          <Divider style={{ margin: '4px 0' }} titlePlacement="left" plain>
            {t('storage.predictionsTarget')}
          </Divider>
          <Alert type="warning" showIcon title={t('storage.predictionsDanger')} description={t('storage.predictionsWarn')} />
          {row(
            t('storage.enable'),
            <Space>
              <Switch checked={predictionsEnabled} onChange={onTogglePredictions} />
              <CompactNumberInput min={predictionsFloor} value={predictionsDays} onChange={(v) => setPredictionsDays(v ?? predictionsFloor)} after={t('batch.admin.days')} />
            </Space>,
            t('storage.predictionsHint', { n: predictionsFloor }),
          )}

        </div>
        </ConfigProvider>
        <SettingsSaveBar dirty={dirty} saving={saving} onSave={save} onDiscard={() => { if (cfg) restore(cfg) }} />
      </Card>
      </SettingsPanel>
      <SettingsPanel section="history">
      <LoadGate loading={!historyLoaded && !historyError} error={historyError} onRetry={loadHistory}>
      {/* Absent until something has actually been deleted. The server stopped recording no-op
          passes, so an empty history means nothing has ever been removed — a card saying that every
          day is a card nobody reads, and the one that matters would be lost among them. */}
      {history.length > 0 && (
        <Card title={t('storage.historyTitle')}>
          <div style={{ overflowX: 'auto' }}>
            {/* Paginated, not a full dump: this is a ring buffer of up to cleanupRunsKeep rows, and
                a page you have to scroll past to reach anything below it is a page nobody reads. */}
            <Table
              rowKey="id"
              size="small"
              pagination={{ pageSize: 10, size: 'small', hideOnSinglePage: true, showSizeChanger: false }}
              dataSource={history}
              columns={histCols}
            />
          </div>
        </Card>
      )}
      {historyLoaded && history.length === 0 && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('manage.operation.noCleanupHistory')} />}
      </LoadGate>
      </SettingsPanel>
    </SettingsWorkspace>
    </>
    </LoadGate>
  )
}
