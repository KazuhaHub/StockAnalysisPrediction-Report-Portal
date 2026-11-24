import { useEffect, useState } from 'react'
import { Alert, Card, Descriptions, Progress, Space, Typography } from 'antd'
import { KeyOutlined } from '@ant-design/icons'
import { Link, useOutletContext } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import ReleaseDigest from '../../components/ReleaseDigest'
import StatusTag, { type StatusTone } from '../../components/StatusTag'
import { notifyError } from '../../lib/notify'
import type { AdminApp } from './appsAdminModel'
import InstallationActions, { RECOVERY_STATES } from './InstallationActions'
import InstallationOperations from './InstallationOperations'
import { actionLabel, failureReason, operationTitle } from './InstallationOperationLine'
import InstallationRuntimeMetrics from './InstallationRuntimeMetrics'
import type { InstallationSettingsContext } from './AppInstallationTabs'
import { formatBytes } from './AppNetworkProxyPanel'
import type { Settings } from './AppPlatformSettingsPage'
import type { LifecycleAction, PickMode } from './platformLifecycle'
import { platformRoot } from './platformLifecycle'
import { loadInstalledRelease } from './platformMarket'
import { operationOutcome, operationProgress } from './operationSummary'
import { useOperationFeed } from './useOperationFeed'

export type RunState = 'running' | 'stopped' | 'disabled' | 'recovery' | 'pending' | 'setup'

/** Whether the app is serving: running, stopped (its backend is not), disabled, in recovery, or still being set up. */
export function runState(settings: Pick<Settings, 'phase' | 'desired_state' | 'backend'>): RunState {
  if (settings.desired_state === 'disabled' || settings.phase === 'disabled') return 'disabled'
  if (['recovery_required', 'quarantined', 'review_required'].includes(settings.phase)) return 'recovery'
  if (settings.phase === 'pending') return 'pending'
  if (settings.phase === 'setup_required') return 'setup'
  if (settings.backend.required && !(settings.backend.registered && settings.backend.enabled)) return 'stopped'
  return 'running'
}
const RUN_TONE: Record<RunState, StatusTone> = { running: 'success', stopped: 'warning', disabled: 'neutral', recovery: 'error', pending: 'processing', setup: 'warning' }

/** The control state the app cards use (`AdminApp.state`), from an installation's settings, so card and page offer the same actions. */
export function controlState(settings: Pick<Settings, 'phase' | 'desired_state'>): string {
  if (settings.desired_state === 'disabled' || settings.phase === 'disabled') return 'disabled'
  if (settings.phase === 'ready') return 'installed'
  if (settings.phase === 'pending') return 'pending'
  if (settings.phase === 'setup_required') return 'setup_required'
  return 'attention'
}

type ProxyToday = { bytes: number; budget: number; reported: boolean }

/**
 * The Overview tab of an installation: what the app is doing now (run state, readiness, data against its quota, the
 * day's proxy traffic against its budget, its newest operation), the actions that change it, and its recent
 * operations. The actions are the app card's, in their toolbar form, running in the same lifecycle dialog.
 */
export default function InstallationOverview() {
  const { t } = useTranslation()
  const shared = useOutletContext<InstallationSettingsContext>()
  const { settings, flow, operationsKey } = shared
  const installation = settings?.installation_id ?? ''
  const [uninstallable, setUninstallable] = useState(false)
  const [proxy, setProxy] = useState<ProxyToday | null>(null)
  const feed = useOperationFeed(`installation=${installation}&limit=20`, !!settings, `${operationsKey}`)
  const state = settings ? controlState(settings) : ''
  const proxyGranted = !!settings?.capabilities.some(c => c.capability === 'network.proxy' && c.granted)

  // An app that needs recovery offers an uninstall only when its release can still be uninstalled.
  useEffect(() => {
    setUninstallable(false)
    if (!settings || !RECOVERY_STATES.includes(state)) return
    let live = true
    loadInstalledRelease(installation, t).then(r => { if (live) setUninstallable(r.uninstallable === true) }).catch(() => undefined)
    return () => { live = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [installation, state])
  useEffect(() => {
    setProxy(null)
    if (!proxyGranted) return
    const abort = new AbortController()
    api.get<{ today?: ProxyToday }>(`${platformRoot}/installations/${installation}/network-proxy`, { signal: abort.signal })
      .then(view => { if (!abort.signal.aborted && view.today?.reported && view.today.budget > 0) setProxy(view.today) })
      .catch(() => undefined)
    return () => abort.abort()
  }, [installation, proxyGranted, operationsKey])

  if (!settings || !flow) return null
  const app: AdminApp = {
    key: `platform:${installation}`, builtin: false, platform: true, installationId: installation, appId: settings.app_id, publisherId: settings.publisher_id,
    state, name: settings.app_id, version: settings.version, scopes: [], access: [],
  }
  const name = shared.name
  const refuse = () => notifyError(t(flow.busyReason === 'pending' ? 'appStore.busyPending' : 'appStore.busyWorking'))
  const onPick = (mode: PickMode, opener?: HTMLElement | null) => { if (flow.busy) { refuse(); return } flow.pickVersion(app, name, mode, opener) }
  const onLifecycle = (action: LifecycleAction, opener?: HTMLElement | null) => { if (flow.busy) { refuse(); return } void flow.act(app, action, opener) }

  const run = runState(settings)
  const gaps = settings.configuration_gaps.length
  const latest = feed.rows?.[0]
  const outcome = latest ? operationOutcome(latest) : undefined
  const progress = latest ? operationProgress(latest) : null
  const dataPercent = settings.data && Number(settings.data.quota_bytes) > 0 ? Math.min(100, Math.round(Number(settings.data.observed_bytes) * 100 / Number(settings.data.quota_bytes))) : 0
  const gapLabel = (gap: string) => {
    if (gap.startsWith('capability:')) return t('appPlatform.settings.missingCapability', { capability: t(`appPlatform.capability.${gap.slice('capability:'.length)}`, { defaultValue: gap.slice('capability:'.length) }) })
    if (gap.startsWith('secret_binding:')) return t('appPlatform.settings.missingSecretBinding', { binding: gap.slice('secret_binding:'.length) })
    return t(`appPlatform.settings.gap.${gap}`, { defaultValue: t('appPlatform.settings.gap.unknown') })
  }
  const setupRequired = settings.configuration_gaps.some(gap => gap === 'setup_required' || gap.startsWith('secret_binding:'))
  const items = [
    { key: 'run', label: t('appPlatform.overview.runState'), children: <StatusTag tone={RUN_TONE[run]}>{t(`appPlatform.overview.run.${run}`)}</StatusTag> },
    { key: 'health', label: t('appPlatform.overview.health'), children: gaps ? <StatusTag tone="warning">{t('appPlatform.overview.healthGaps', { count: gaps })}</StatusTag> : <StatusTag tone="success">{t('appPlatform.overview.healthReady')}</StatusTag> },
    ...(settings.data ? [{ key: 'data', label: t('appPlatform.overview.dataUsage'), children: <div className="rp-installation-usage">
      <Progress size="small" percent={dataPercent} status={dataPercent >= 90 ? 'exception' : 'normal'} showInfo={false} aria-label={t('appPlatform.overview.dataUsage')} />
      <Typography.Text type="secondary">{t('appPlatform.overview.usageValue', { used: formatBytes(Number(settings.data.observed_bytes)), total: formatBytes(Number(settings.data.quota_bytes)) })}</Typography.Text>
    </div> }] : []),
    ...(proxy ? [{ key: 'proxy', label: t('appPlatform.overview.proxyToday'), children: <div className="rp-installation-usage">
      <Progress size="small" percent={Math.min(100, Math.round(proxy.bytes * 100 / proxy.budget))} showInfo={false} aria-label={t('appPlatform.overview.proxyToday')} />
      <Typography.Text type="secondary">{t('appPlatform.overview.usageValue', { used: formatBytes(proxy.bytes), total: formatBytes(proxy.budget) })}</Typography.Text>
    </div> }] : []),
    { key: 'operation', label: t('appPlatform.overview.operation'), children: !latest
      ? <Typography.Text type="secondary">{feed.rows ? t('appPlatform.overview.noOperation') : '—'}</Typography.Text>
      : <Space orientation="vertical" size={2}>
        <Space size={6} wrap>
          <Typography.Text>{operationTitle(t, latest)}</Typography.Text>
          {outcome === 'running' && <StatusTag tone="processing">{progress ? `${t('appPlatform.ops.step', { step: progress.step, total: progress.total })} · ${t(`appPlatform.stage.${latest.stage}`, { defaultValue: latest.stage })}` : t('appPlatform.ops.running')}</StatusTag>}
          {outcome === 'succeeded' && <StatusTag tone="success">{t('appPlatform.ops.succeeded', { action: actionLabel(t, latest.action) })}</StatusTag>}
          {outcome === 'failed' && <StatusTag tone="error">{t('appPlatform.ops.failed', { action: actionLabel(t, latest.action) })}</StatusTag>}
        </Space>
        {outcome === 'failed' && <Typography.Text type="danger">{failureReason(t, latest.error_code)}</Typography.Text>}
      </Space> },
  ]
  return <Space orientation="vertical" size={16} style={{ width: '100%' }} className="rp-installation-overview">
    <Card>
      <Descriptions className="rp-installation-facts" column={{ xs: 1, sm: 2, lg: 3 }} size="small" items={items} />
      <div className="rp-installation-toolbar">
        <InstallationActions variant="toolbar" state={state} uninstallable={uninstallable} repairEligible={settings.repair_eligible} versioned={!!settings.version}
          busy={flow.acting === app.key} blocked={flow.busyReason} onRefuse={refuse} onPick={onPick} onLifecycle={onLifecycle} />
        {run === 'running' && <Link to={`/apps/v2/${installation}`}>{t('appPlatform.settings.openApplication')}</Link>}
      </div>
    </Card>
    <InstallationRuntimeMetrics installation={installation} />
    <Card title={t('appPlatform.settings.configuration')} extra={setupRequired
      ? <Link to={`/manage/apps/${encodeURIComponent(settings.app_id)}/credentials`}><KeyOutlined aria-hidden="true" /> {t('appPlatform.settings.manageCredentials')}</Link>
      : undefined}>
      {gaps
        ? <Alert type="warning" showIcon title={t('appPlatform.settings.configurationNeeded')} description={<ul style={{ margin: 0, paddingInlineStart: 20 }}>{settings.configuration_gaps.map(gap => <li key={gap}>{gapLabel(gap)}</li>)}</ul>} />
        : <Alert type="success" showIcon title={t('appPlatform.settings.configurationReady')} />}
    </Card>
    <Card title={t('appPlatform.settings.backend')}>
      <Descriptions column={1} size="small" items={[
        { key: 'required', label: t('appPlatform.settings.backendRequired'), children: settings.backend.required ? t('appPlatform.settings.yes') : t('appPlatform.settings.staticOnly') },
        { key: 'state', label: t('appPlatform.settings.backendState'), children: settings.backend.required
          ? settings.backend.registered
            ? settings.backend.enabled ? t('appPlatform.settings.backendEnabled') : t('appPlatform.settings.backendDisabled')
            : t('appPlatform.settings.backendNotRegistered')
          : t('appPlatform.settings.notApplicable') },
      ]} />
    </Card>
    <Card title={t('appPlatform.settings.currentRelease')}>
      <Descriptions column={1} size="small" items={[
        { key: 'version', label: t('appPlatform.version'), children: settings.version || '—' },
        ...(settings.current_release_sha256 ? [{ key: 'release', label: t('appPlatform.settings.currentRelease'), children: <ReleaseDigest value={settings.current_release_sha256} /> }] : []),
        { key: 'generation', label: t('appPlatform.settings.generation'), children: settings.generation },
        { key: 'policy', label: t('appPlatform.settings.policyRevision'), children: settings.policy_revision },
      ]} />
    </Card>
    <InstallationOperations feed={feed} />
  </Space>
}
