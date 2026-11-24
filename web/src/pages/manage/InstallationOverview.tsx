import { useEffect, useState } from 'react'
import { Card, Descriptions, Progress, Space, Typography } from 'antd'
import { Link, useOutletContext } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import ReleaseDigest from '../../components/ReleaseDigest'
import StatusTag, { type StatusTone } from '../../components/StatusTag'
import { installationPath, type InstallationTab } from './installationPaths'
import InstallationOperations from './InstallationOperations'
import { actionLabel, failureReason, operationTitle } from './InstallationOperationLine'
import InstallationRuntimeMetrics from './InstallationRuntimeMetrics'
import type { InstallationSettingsContext } from './AppInstallationTabs'
import { formatBytes } from './AppNetworkProxyPanel'
import type { Settings } from './AppPlatformSettingsPage'
import { RUN_TONE, runState } from './installationState'
import { platformRoot } from './platformLifecycle'
import { operationOutcome, operationProgress } from './operationSummary'

type ProxyToday = { bytes: number; budget: number; reported: boolean }

/** The tab where a configuration gap is fixed: grants, credentials and bindings are Permissions & network, retained data is Data. */
export function gapTab(gap: string): InstallationTab {
  return gap.startsWith('data_') ? 'data' : 'permissions'
}

/** Whether the app can serve now, in a word: ready, paused, still installing, not ready (setup or recovery), or off. */
export function readiness(settings: Pick<Settings, 'phase' | 'desired_state' | 'control_paused'>): { tone: StatusTone; key: string } {
  if (settings.desired_state === 'disabled' || settings.phase === 'disabled') return { tone: 'neutral', key: 'appPlatform.overview.readyOff' }
  if (settings.control_paused) return { tone: 'warning', key: 'appPlatform.overview.readyPaused' }
  if (settings.phase === 'ready') return { tone: 'success', key: 'appPlatform.overview.healthReady' }
  if (settings.phase === 'pending') return { tone: 'processing', key: 'appPlatform.overview.readyPending' }
  return { tone: 'warning', key: 'appPlatform.overview.readyNot' }
}

/**
 * The Overview tab of an installation: one Status card (run state, readiness, configuration, backend, version, release,
 * generation, data against its quota, the day's proxy traffic against its budget, then its newest operation and the
 * runtime metrics slot), and below it the recent operations. The actions that change the app are in the page header.
 */
export default function InstallationOverview() {
  const { t } = useTranslation()
  const shared = useOutletContext<InstallationSettingsContext>()
  const { settings, operationsKey } = shared
  const installation = settings?.installation_id ?? ''
  const [proxy, setProxy] = useState<ProxyToday | null>(null)
  const { feed } = shared
  const proxyGranted = !!settings?.capabilities.some(c => c.capability === 'network.proxy' && c.granted)

  useEffect(() => {
    setProxy(null)
    if (!proxyGranted) return
    const abort = new AbortController()
    api.get<{ today?: ProxyToday }>(`${platformRoot}/installations/${installation}/network-proxy`, { signal: abort.signal })
      .then(view => { if (!abort.signal.aborted && view.today?.reported && view.today.budget > 0) setProxy(view.today) })
      .catch(() => undefined)
    return () => abort.abort()
  }, [installation, proxyGranted, operationsKey])

  if (!settings) return null
  const run = runState(settings)
  const ready = readiness(settings)
  const gaps = settings.configuration_gaps
  const latest = feed.rows?.[0]
  const outcome = latest ? operationOutcome(latest) : undefined
  const progress = latest ? operationProgress(latest) : null
  const percent = (used: number, total: number) => (total > 0 ? Math.min(100, Math.round(used * 100 / total)) : 0)
  const gapLabel = (gap: string) => {
    if (gap.startsWith('capability:')) return t('appPlatform.settings.missingCapability', { capability: t(`appPlatform.capability.${gap.slice('capability:'.length)}`, { defaultValue: gap.slice('capability:'.length) }) })
    if (gap.startsWith('secret_binding:')) return t('appPlatform.settings.missingSecretBinding', { binding: gap.slice('secret_binding:'.length) })
    return t(`appPlatform.settings.gap.${gap}`, { defaultValue: t('appPlatform.settings.gap.unknown') })
  }
  const usage = (label: string, used: number, total: number) => <div className="rp-installation-usage">
    <Progress size="small" percent={percent(used, total)} status={percent(used, total) >= 90 ? 'exception' : 'normal'} showInfo={false} aria-label={label} />
    <Typography.Text type="secondary">{t('appPlatform.overview.usageValue', { used: formatBytes(used), total: formatBytes(total) })}</Typography.Text>
  </div>
  const backend = !settings.backend.required
    ? t('appPlatform.overview.backendStatic')
    : t('appPlatform.overview.backendManaged', { status: settings.backend.registered ? t(settings.backend.enabled ? 'appPlatform.settings.backendEnabled' : 'appPlatform.settings.backendDisabled') : t('appPlatform.settings.backendNotRegistered') })
  const items = [
    { key: 'run', label: t('appPlatform.overview.runState'), children: <StatusTag tone={RUN_TONE[run]}>{t(`appPlatform.overview.run.${run}`)}</StatusTag> },
    { key: 'ready', label: t('appPlatform.overview.health'), children: <StatusTag tone={ready.tone}>{t(ready.key)}</StatusTag> },
    { key: 'config', label: t('appPlatform.settings.configuration'), children: gaps.length
      ? <Space orientation="vertical" size={2}>
        <Space size={8} wrap>
          <StatusTag tone="warning">{t('appPlatform.overview.healthGaps', { count: gaps.length })}</StatusTag>
          <Link to={installationPath(installation, gapTab(gaps[0]))}>{t('appPlatform.overview.configFix')}</Link>
        </Space>
        <ul className="rp-installation-gaps">{gaps.map(gap => <li key={gap}><Link to={installationPath(installation, gapTab(gap))}>{gapLabel(gap)}</Link></li>)}</ul>
      </Space>
      : <StatusTag tone="success">{t('appPlatform.overview.configOk')}</StatusTag> },
    { key: 'backend', label: t('appPlatform.settings.backend'), children: backend },
    { key: 'version', label: t('appPlatform.version'), children: settings.version || '—' },
    ...(settings.current_release_sha256 ? [{ key: 'release', label: t('appPlatform.settings.currentRelease'), children: <ReleaseDigest value={settings.current_release_sha256} /> }] : []),
    { key: 'generation', label: t('appPlatform.settings.generation'), children: settings.generation },
    ...(settings.data ? [{ key: 'data', label: t('appPlatform.overview.dataUsage'), children: usage(t('appPlatform.overview.dataUsage'), Number(settings.data.observed_bytes), Number(settings.data.quota_bytes)) }] : []),
    ...(proxy ? [{ key: 'proxy', label: t('appPlatform.overview.proxyToday'), children: usage(t('appPlatform.overview.proxyToday'), proxy.bytes, proxy.budget) }] : []),
  ]
  return <Space orientation="vertical" size={16} style={{ width: '100%' }} className="rp-installation-overview">
    <Card title={t('appPlatform.overview.status')}>
      <Descriptions className="rp-installation-facts" layout="vertical" colon={false} column={{ xs: 1, sm: 1, md: 2, lg: 2, xl: 3, xxl: 3 }} size="small" items={items} />
      <div className="rp-installation-operation">
        <Typography.Text type="secondary">{t('appPlatform.overview.operation')}</Typography.Text>
        <div>
          {!latest
            ? <Typography.Text type="secondary">{feed.rows ? t('appPlatform.overview.noOperation') : '—'}</Typography.Text>
            : <Space orientation="vertical" size={2}>
              <Space size={8} wrap>
                <Typography.Text>{operationTitle(t, latest)}</Typography.Text>
                {outcome === 'running' && <StatusTag tone="processing">{progress ? `${t('appPlatform.ops.step', { step: progress.step, total: progress.total })} · ${t(`appPlatform.stage.${latest.stage}`, { defaultValue: latest.stage })}` : t('appPlatform.ops.running')}</StatusTag>}
                {outcome === 'succeeded' && <StatusTag tone="success">{t('appPlatform.ops.succeeded', { action: actionLabel(t, latest.action) })}</StatusTag>}
                {outcome === 'failed' && <StatusTag tone="error">{t('appPlatform.ops.failed', { action: actionLabel(t, latest.action) })}</StatusTag>}
              </Space>
              {outcome === 'failed' && <Typography.Text type="danger">{failureReason(t, latest.error_code)}</Typography.Text>}
            </Space>}
        </div>
      </div>
      <InstallationRuntimeMetrics installation={installation} />
    </Card>
    <InstallationOperations feed={feed} />
  </Space>
}
