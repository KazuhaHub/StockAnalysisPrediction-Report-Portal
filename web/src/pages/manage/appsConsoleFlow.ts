import { useEffect, useRef, useState, type ChangeEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { errText } from '../../api/client'
import type { PlatformMarketApp, PlatformRelease } from '../../api/appPlatform'
import { useAuth } from '../../auth'
import { notifyError } from '../../lib/notify'
import { latestVersion } from '../../lib/semver'
import type { AdminApp } from './appsAdminModel'
import { usePlatformLifecycle, type LifecycleAction } from './platformLifecycle'
import { loadInstalledRelease, usePlatformMarket } from './platformMarket'

/** How a catalogue source is named: the official one by its name, a custom one by its host. */
export const sourceName = (name: string, url: string, official: boolean) => {
  if (official) return name
  try { return new URL(url).host } catch { return url }
}

// An application id is only unique within one publisher, so an installed app and a catalogue entry are the same
// app only when both match.
export const sameApp = (a: AdminApp, c: { publisher_id: string; app_id: string }) => a.platform && !!a.publisherId && a.publisherId === c.publisher_id && a.appId === c.app_id

/** The control that has focus right now, if it is one: what a flow returns focus to when it ends. */
const focusedControl = (): HTMLElement | null => document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : null

/**
 * What the installed page and the install page share: the lifecycle flow, the catalogue, and the steps that turn
 * a catalogue entry or an offline package into a flow (download and verify, then review). Nothing an earlier
 * account started may land on the next account's page.
 */
export function useAppsConsoleFlow({ apps, reload, active = true }: { apps: AdminApp[]; reload: (opts?: { quiet?: boolean }) => Promise<boolean>; active?: boolean }) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const lifecycle = usePlatformLifecycle(() => { void reload({ quiet: true }) })
  const market = usePlatformMarket(active)
  const owner = useRef(user)
  owner.current = user
  const mounted = useRef(false)
  const generation = useRef<object>({})
  const live = (account: string | null, epoch: object) => mounted.current && owner.current === account && generation.current === epoch
  const [acting, setActing] = useState('')
  const [detail, setDetailState] = useState<PlatformMarketApp | null>(null)
  // The dialog can be closed while its download runs on; a failure then goes to the app's card.
  const detailOpen = useRef<PlatformMarketApp | null>(null)
  const setDetail = (app: PlatformMarketApp | null) => { detailOpen.current = app; setDetailState(app) }
  const [detailError, setDetailError] = useState('')
  // A failed Install on a catalogue card is said on that card, not far away in a notification.
  const [cardErrors, setCardErrors] = useState<Record<string, string>>({})

  useEffect(() => {
    mounted.current = true
    generation.current = {}
    setActing(''); setDetail(null); setDetailError(''); setCardErrors({})
    return () => { mounted.current = false; generation.current = {} }
  }, [user])

  const catalogueApps = market.catalogue?.status === 'ready' ? market.catalogue.apps : []
  const catalogueOf = (a: AdminApp) => catalogueApps.find(c => sameApp(a, c))
  const installedOf = (c: PlatformMarketApp) => apps.find(a => sameApp(a, c))
  const busy = lifecycle.busy || !!lifecycle.intent || !!acting || !!market.staging || market.uploading

  // A staged release goes to the lifecycle modal: as an install, as an update when it is for an installed app, as
  // an enable when it is the disabled installation's own release, or as an uninstall when that is all it allows.
  // Returns why not, when it cannot.
  const openStaged = (release: PlatformRelease, installation?: string, opener?: HTMLElement | null): string => {
    if (release.installed) return t('appStore.alreadyInstalled')
    const target = installation ?? release.installation_id
    if (release.uninstallable && target) { lifecycle.begin({ ...release, installation_id: target }, 'uninstall', opener); return '' }
    if (release.enableable && target) { lifecycle.begin({ ...release, installation_id: target }, 'enable', opener); return '' }
    // A version chosen for an installed app is always an update: the server, not this page, refuses a downgrade.
    if (installation) { lifecycle.begin({ ...release, installation_id: installation }, 'update', opener); return '' }
    if (release.installable) { lifecycle.begin(release, 'install', opener); return '' }
    if (target) { lifecycle.begin({ ...release, installation_id: target }, 'update', opener); return '' }
    return t(`appPlatform.reason.${release.reason}`, { defaultValue: t('appMarket.verificationChanged') })
  }

  /** Downloads and verifies one version, then opens its flow. Resolves to the failure text, '' for success, null if nothing happened. */
  const stageAndOpen = async (app: PlatformMarketApp, version: string, installation?: string): Promise<string | null> => {
    const opener = focusedControl()
    const account = user, epoch = generation.current
    const result = await market.stage(app, version)
    if (!live(account, epoch) || !result) return null
    if ('error' in result) return result.error
    return openStaged(result.release, installation, opener)
  }

  const installFromDetail = async (app: PlatformMarketApp, version: string) => {
    setDetailError('')
    const account = user, epoch = generation.current
    const problem = await stageAndOpen(app, version, installedOf(app)?.installationId)
    if (problem === null || !live(account, epoch)) return
    const stillOpen = detailOpen.current === app
    if (problem && stillOpen) setDetailError(problem)
    else if (problem) setCardErrors(v => ({ ...v, [`${app.publisher_id}/${app.app_id}`]: problem }))
    else if (stillOpen) setDetail(null)
  }

  /** The Install button on a catalogue card: the default version, with a failure reported as a notification. */
  const quickInstall = async (app: PlatformMarketApp) => {
    const version = latestVersion(app.versions.map(v => v.version))
    if (!version) return
    const account = user, epoch = generation.current
    const key = `${app.publisher_id}/${app.app_id}`
    setCardErrors(v => { const { [key]: _gone, ...rest } = v; return rest })
    const problem = await stageAndOpen(app, version, installedOf(app)?.installationId)
    if (problem && live(account, epoch)) setCardErrors(v => ({ ...v, [key]: problem }))
  }

  const switchVersion = async (a: AdminApp, catalogue: PlatformMarketApp, version: string) => {
    const account = user, epoch = generation.current
    setActing(a.key)
    try {
      const problem = await stageAndOpen(catalogue, version, a.installationId)
      if (problem && live(account, epoch)) notifyError(problem)
    } finally { if (live(account, epoch)) setActing('') }
  }

  const act = async (a: AdminApp, action: LifecycleAction, from?: HTMLElement | null) => {
    if (!a.installationId) return
    const opener = from ?? focusedControl()
    const account = user, epoch = generation.current
    setActing(a.key)
    try {
      const release = await loadInstalledRelease(a.installationId, t)
      if (live(account, epoch)) lifecycle.begin(release, action, opener)
    } catch (e) { if (live(account, epoch)) notifyError(errText(e, t)) }
    finally { if (live(account, epoch)) setActing('') }
  }

  const uploaded = async (e: ChangeEvent<HTMLInputElement>) => {
    const chosen = e.target.files?.[0]
    e.target.value = ''
    if (!chosen) return
    const opener = focusedControl()
    const account = user, epoch = generation.current
    const result = await market.stageOffline(chosen)
    if (!live(account, epoch) || !result) return
    const problem = 'error' in result ? result.error : openStaged(result.release, undefined, opener)
    if (problem) notifyError(problem)
  }

  const openDetail = (app: PlatformMarketApp) => { market.clearStageError(); setDetailError(''); setDetail(app) }

  return {
    user, live, generation, lifecycle, market, busy, acting,
    detail, setDetail, detailError, openDetail, cardErrors,
    catalogueApps, catalogueOf, installedOf,
    stageAndOpen, installFromDetail, quickInstall, switchVersion, act, uploaded,
  }
}
export type AppsConsoleFlow = ReturnType<typeof useAppsConsoleFlow>
