import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Alert, Button, theme } from 'antd'
import { LoadingPlaceholder } from '../LoadGate'
import { useNavigate, useLocation } from 'react-router'
import { useTranslation } from 'react-i18next'
import { usePrefs } from '../../prefs'
import { useIsAdmin } from '../../auth'
import { api } from '../../api/client'
import { parseRouteSearch } from '../../lib/route-query-v2'
import { parseBridgePresentation, type BridgeSurface } from '../../lib/bridge-v2'
import { parseAppLaunchV2, validInstallationId, type AppLaunchV2 } from '../../lib/appLaunchV2'
import { AppDisplayIcon, appDisplayText } from '../../lib/appDisplayV2'
import AppPageHeader from './AppPageHeader'
import { useCrumbTail } from '../../lib/crumbTail'
import type { PageActionSnapshot } from '../../lib/page-v2'
import AppFrameV2 from './AppFrameV2'
import { AppDraftPrompt, useAppDraftBlocker } from './AppDraftGuard'

/**
 * One app document on one surface. A reader's page (`/apps/v2/:id`) and an administrator's page (the console tab and
 * `/apps/manage/:id`) are this same component: they differ in the launch they ask for, the endpoint the frame
 * relays backend requests to, and the chrome around the frame. The page waits for its launch and renders once, with
 * the app's name from the launch data.
 */
export interface AppSurfaceViewProps {
  installation: string
  surface: BridgeSurface
  /** The URL the surface is mounted at; the app's own routes extend it. */
  base: string
  /** Back leads the title. Omitted where the surrounding page already has its own way out. */
  onBack?: () => void
  /** Rendered in the header beside the app's own actions, once the launch has named the app. */
  headerExtra?: ReactNode | ((launch: AppLaunchV2) => ReactNode)
  /** The shell breadcrumb ends with the app's name (an app page), or is left alone (a console tab has its own). */
  crumbTail?: boolean
}

export default function AppSurfaceView(props: AppSurfaceViewProps) {
  // Retire the old frame before another installation's (or surface's) metadata can arrive.
  return <LaunchPage key={`${props.surface}:${props.installation}`} {...props} />
}

function LaunchPage({ installation, surface, base, onBack, headerExtra, crumbTail = true }: AppSurfaceViewProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const location = useLocation()
  const { lang, dark } = usePrefs()
  const { token } = theme.useToken()
  const admin = useIsAdmin()
  const [launch, setLaunch] = useState<AppLaunchV2 | null>(null)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [page, setPage] = useState<PageActionSnapshot|null>(null)
  const [dirty, setDirty] = useState(false)
  const allowDraftNavigation = useRef(false)
  const blocker = useAppDraftBlocker(dirty, allowDraftNavigation)
  const panel = useRef<HTMLDivElement>(null)
  const [height, setHeight] = useState(64)

  useEffect(() => {
    let live = true
    const controller = new AbortController()
    setLaunch(null)
    setFailed(false)
    if (!validInstallationId(installation)) {
      setFailed(true)
      return
    }
    api.get<unknown>(`/api/app-platform/installations/${installation}/${surface === 'admin' ? 'admin/' : ''}launch`, { signal: controller.signal })
      .then(value => {
        if (!live) return
        const parsed = parseAppLaunchV2(value, installation)
        // A launch for the other surface is never mounted on this one.
        if (!parsed || parsed.surface !== surface) { setFailed(true); return }
        setLaunch(parsed)
      })
      .catch(() => { if (live) setFailed(true) })
    return () => { live = false; controller.abort() }
  }, [installation, surface, attempt])

  const entry = launch?.entryPath ?? '/'
  const path = location.pathname === base ? entry
    : location.pathname.startsWith(base + '/') ? location.pathname.slice(base.length) : null
  const registered = launch?.routes.find(route => route.path === path)
  const route = !location.hash && registered && parseRouteSearch(registered.query ?? [], location.search) ? registered : undefined
  const layout = route?.layout
  useLayoutEffect(() => {
    const element = panel.current
    if (!element || layout !== 'viewport') return
    const measure = () => {
      const next = Math.floor(element.getBoundingClientRect().height)
      if (next >= 64) setHeight(Math.min(next, 16384))
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [launch, layout])

  const presentation = useMemo(() => launch ? parseBridgePresentation({
    locale: lang, timeZone: launch.timeZone, layout,
    navigation: { path, search:location.search, routes: launch.routes },
    theme: {
      dark, colorPrimary: token.colorPrimary, colorBg: token.colorBgContainer,
      colorText: token.colorText, colorBorder: token.colorBorder,
      colorBgLayout: token.colorBgLayout, borderRadius: token.borderRadius,
    },
  }, surface) : null, [launch,lang,dark,layout,path,location.search,token,surface])

  const copy = launch ? appDisplayText(launch.applicationId, launch.display, lang) : null
  // The shell's breadcrumb ends with the app's name, rendered with the page from the same launch data.
  useCrumbTail(crumbTail ? (copy ? copy.name : failed ? null : undefined) : null)

  return (
    <section style={{ display: 'flex', flexDirection: 'column', flex: layout === 'viewport' ? '1 1 0' : '0 0 auto', minHeight: 0, width: '100%', gap: 16 }}>
      <AppDraftPrompt dirty={dirty} blocker={blocker} />
      {launch || failed ? <AppPageHeader name={copy?.name ?? ''} icon={launch ? <AppDisplayIcon display={launch.display} /> : null}
        state={route ? page : null} onBack={onBack} extra={launch ? (typeof headerExtra === 'function' ? headerExtra(launch) : headerExtra) : null} /> : null}
      {failed || (launch && (!route || !presentation)) ? (
        <Alert type="error" showIcon title={t(launch && !route ? 'apps.v2InvalidRoute' : 'apps.v2Unavailable')}
          action={launch && !route ? <Button className="rp-safe-primary" type="primary" onClick={() => { void navigate(base, { replace:true }) }}>{t('apps.v2Home')}</Button> : <Button className="rp-safe-primary" type="primary" onClick={() => { setLaunch(null); setAttempt(value => value + 1) }}>{t('apps.v2Retry')}</Button>} />
      ) : !launch ? (
        <LoadingPlaceholder minHeight={320} label={t('apps.v2Loading')} />
      ) : (
        <div ref={panel} style={{ flex: layout === 'viewport' ? '1 1 0' : '0 0 auto', minHeight: 64, minWidth: 0, overflow: 'hidden' }}>
          <AppFrameV2 source={launch.source} title={copy!.name} binding={launch.binding}
            capabilities={launch.capabilities} backendAvailable={launch.backendAvailable} surface={surface} canReviewNetwork={admin}
            // The policy revision is part of the document URL, so a changed policy needs a fresh launch.
            onNetworkPolicyChanged={() => { setLaunch(null); setAttempt(value => value + 1) }} onPageChange={setPage} onDraftChange={setDirty} onNavigate={(target, replace, search, bypassDraft) => {
              const destination=launch.routes.find(route=>route.path===target)
              if ((blocker.state !== 'unblocked' && !bypassDraft) || !destination || !parseRouteSearch(destination.query??[],search)) return
              if (bypassDraft) allowDraftNavigation.current = true
              void navigate((target === entry ? base : base + target)+search, { replace })
              if (bypassDraft) queueMicrotask(() => { allowDraftNavigation.current = false })
            }} presentation={presentation!} height={height} />
        </div>
      )}
    </section>
  )
}
