import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Spin, theme } from 'antd'
import { useNavigate, useParams, useLocation } from 'react-router'
import { useTranslation } from 'react-i18next'
import { usePrefs } from '../prefs'
import { api } from '../api/client'
import { parseRouteSearch } from '../lib/route-query-v2'
import { parseBridgePresentation } from '../lib/bridge-v2'
import { parseAppLaunchV2, validInstallationId, type AppLaunchV2 } from '../lib/appLaunchV2'
import { AppDisplayIcon, appDisplayText } from '../lib/appDisplayV2'
import AppPageHeader from '../components/apps/AppPageHeader'
import type { PageActionSnapshot } from '../lib/page-v2'
import AppFrameV2 from '../components/apps/AppFrameV2'
import { AppDraftPrompt, useAppDraftBlocker } from '../components/apps/AppDraftGuard'

export default function AppViewV2() {
  const { installation = '' } = useParams()
  // Retire the old frame before another installation's metadata can arrive.
  return <LaunchPage key={installation} installation={installation} />
}

function LaunchPage({ installation }: { installation: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const location = useLocation()
  const { lang, dark } = usePrefs()
  const { token } = theme.useToken()
  const [launch, setLaunch] = useState<AppLaunchV2 | null>(null)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [page, setPage] = useState<PageActionSnapshot|null>(null)
  const [dirty, setDirty] = useState(false)
  const blocker = useAppDraftBlocker(dirty)
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
    api.get<unknown>(`/api/app-platform/installations/${installation}/launch`, { signal: controller.signal })
      .then(value => {
        if (!live) return
        const parsed = parseAppLaunchV2(value, installation)
        if (!parsed) { setFailed(true); return }
        setLaunch(parsed)
      })
      .catch(() => { if (live) setFailed(true) })
    return () => { live = false; controller.abort() }
  }, [installation, attempt])

  const base = `/apps/v2/${installation}`
  const path = location.pathname === base ? '/'
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
  }) : null, [launch,lang,dark,layout,path,location.search,token])

  const copy = launch ? appDisplayText(launch.applicationId, launch.display, lang) : null

  return (
    <section style={{ display: 'flex', flexDirection: 'column', flex: layout === 'viewport' ? '1 1 0' : '0 0 auto', minHeight: 0, width: '100%', gap: 16 }}>
      <AppDraftPrompt dirty={dirty} blocker={blocker} />
      <AppPageHeader name={copy?.name ?? ''} icon={launch ? <AppDisplayIcon display={launch.display} /> : null}
        state={route ? page : null} onBack={() => { void navigate('/apps') }} />
      {failed || (launch && (!route || !presentation)) ? (
        <Alert type="error" showIcon title={t(launch && !route ? 'apps.v2InvalidRoute' : 'apps.v2Unavailable')}
          action={launch && !route ? <Button onClick={() => { void navigate(base, { replace:true }) }}>{t('apps.v2Home')}</Button> : <Button onClick={() => { setLaunch(null); setAttempt(value => value + 1) }}>{t('apps.v2Retry')}</Button>} />
      ) : !launch ? (
        <div role="status" style={{ display: 'grid', placeItems: 'center', minHeight: 160 }}>
          <Spin /><span>{t('apps.v2Loading')}</span>
        </div>
      ) : (
        <div ref={panel} style={{ flex: layout === 'viewport' ? '1 1 0' : '0 0 auto', minHeight: 64, minWidth: 0, overflow: 'hidden' }}>
          <AppFrameV2 source={launch.source} title={copy!.name} binding={launch.binding}
            capabilities={launch.capabilities} backendAvailable={launch.backendAvailable} onPageChange={setPage} onDraftChange={setDirty} onNavigate={(target, replace, search) => {
              const destination=launch.routes.find(route=>route.path===target)
              if (blocker.state !== 'unblocked' || !destination || !parseRouteSearch(destination.query??[],search)) return
              void navigate((target === '/' ? base : base + target)+search, { replace })
            }} presentation={presentation!} height={height} />
        </div>
      )}
    </section>
  )
}
