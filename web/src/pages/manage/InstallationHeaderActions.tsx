import { useEffect, useMemo, useState } from 'react'
import { Button, Space } from 'antd'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { notifyError } from '../../lib/notify'
import type { AdminApp } from './appsAdminModel'
import type { Settings } from './AppPlatformSettingsPage'
import type { AppsConsoleFlow } from './appsConsoleFlow'
import InstallationActions, { RECOVERY_STATES } from './InstallationActions'
import { controlState, runState } from './installationState'
import type { LifecycleAction, PickMode } from './platformLifecycle'
import { loadInstalledRelease } from './platformMarket'

/** The installation as the shared lifecycle flow addresses an app: the same shape an app card hands it. */
export function installationApp(settings: Settings | null, name: string): AdminApp | null {
  if (!settings) return null
  return {
    key: `platform:${settings.installation_id}`, builtin: false, platform: true, installationId: settings.installation_id, appId: settings.app_id,
    publisherId: settings.publisher_id, state: controlState(settings), name, version: settings.version, scopes: [], access: [],
  }
}

/**
 * The actions of an installation, in the page header so every tab reaches them: Update, Switch version, Disable or
 * Enable, Repair and Uninstall (the app card's own rules and flows), and Open application. Each opens the page's
 * lifecycle dialog.
 */
export default function InstallationHeaderActions({ settings, app, name, flow, update }: { settings: Settings; app: AdminApp; name: string; flow: AppsConsoleFlow; update?: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [uninstallable, setUninstallable] = useState(false)
  const state = app.state ?? ''
  // An app that needs recovery offers an uninstall only when its release can still be uninstalled.
  useEffect(() => {
    setUninstallable(false)
    if (!RECOVERY_STATES.includes(state)) return
    let live = true
    loadInstalledRelease(settings.installation_id, t).then(r => { if (live) setUninstallable(r.uninstallable === true) }).catch(() => undefined)
    return () => { live = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [settings.installation_id, state])
  const refuse = () => notifyError(t(flow.busyReason === 'pending' ? 'appStore.busyPending' : 'appStore.busyWorking'))
  const onPick = (mode: PickMode, opener?: HTMLElement | null) => { if (flow.busy) { refuse(); return } flow.pickVersion(app, name, mode, opener) }
  const onLifecycle = (action: LifecycleAction, opener?: HTMLElement | null) => { if (flow.busy) { refuse(); return } void flow.act(app, action, opener) }
  const open = runState(settings) === 'running'
  return <Space wrap className="rp-installation-header-actions">
    <InstallationActions variant="toolbar" state={state} uninstallable={uninstallable} repairEligible={settings.repair_eligible} versioned={!!settings.version} updateTo={update}
      busy={flow.acting === app.key} blocked={flow.busyReason} onRefuse={refuse} onPick={onPick} onLifecycle={onLifecycle} />
    {open && <Button onClick={() => void navigate(`/apps/v2/${settings.installation_id}`)}>{t('appPlatform.settings.openApplication')}</Button>}
  </Space>
}

/** The repair flow of the page, for any place that offers it (the Data tab's recovery card): the same one the header runs. */
export function useInstallationRepair(app: AdminApp | null, flow: AppsConsoleFlow): () => void {
  return useMemo(() => () => { if (app) void flow.act(app, 'repair') },
    // The flow object is new on every render; `act` reads its current state when called.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [app, flow.act])
}
