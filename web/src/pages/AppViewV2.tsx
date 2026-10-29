import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Alert, Button, Space, Spin, Typography, theme } from 'antd'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { usePrefs } from '../prefs'
import { api } from '../api/client'
import { parseBridgePresentation } from '../lib/bridge-v2'
import { parseAppLaunchV2, validInstallationId, type AppLaunchV2 } from '../lib/appLaunchV2'
import { AppDisplayIcon, appDisplayText } from '../lib/appDisplayV2'
import AppFrameV2 from '../components/apps/AppFrameV2'
import AppDraftGuard from '../components/apps/AppDraftGuard'

export default function AppViewV2() {
  const { installation = '' } = useParams()
  // Retire the old frame before another installation's metadata can arrive.
  return <LaunchPage key={installation} installation={installation} />
}

function LaunchPage({ installation }: { installation: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { lang, dark } = usePrefs()
  const { token } = theme.useToken()
  const [launch, setLaunch] = useState<AppLaunchV2 | null>(null)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [dirty, setDirty] = useState(false)
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

  useLayoutEffect(() => {
    const element = panel.current
    if (!element || launch?.layout !== 'viewport') return
    const measure = () => {
      const next = Math.floor(element.getBoundingClientRect().height)
      if (next >= 64) setHeight(Math.min(next, 16384))
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [launch])

  const presentation = launch ? parseBridgePresentation({
    locale: lang, timeZone: launch.timeZone, layout: launch.layout,
    theme: {
      dark, colorPrimary: token.colorPrimary, colorBg: token.colorBgContainer,
      colorText: token.colorText, colorBorder: token.colorBorder,
      colorBgLayout: token.colorBgLayout, borderRadius: token.borderRadius,
    },
  }) : null

  const copy = launch ? appDisplayText(launch.applicationId, launch.display, lang) : null

  return (
    <section style={{ display: 'flex', flexDirection: 'column', flex: launch?.layout === 'viewport' ? '1 1 0' : '0 0 auto', minHeight: 0, width: '100%', gap: 16 }}>
      <AppDraftGuard dirty={dirty} />
      <Space>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/apps')}>{t('apps.back')}</Button>
        {launch && <><AppDisplayIcon display={launch.display} /><Typography.Title level={4} style={{ margin: 0 }}>{copy?.name}</Typography.Title></>}
      </Space>
      {failed || (launch && !presentation) ? (
        <Alert type="error" showIcon title={t('apps.v2Unavailable')}
          action={<Button onClick={() => { setLaunch(null); setAttempt(value => value + 1) }}>{t('apps.v2Retry')}</Button>} />
      ) : !launch ? (
        <div role="status" style={{ display: 'grid', placeItems: 'center', minHeight: 160 }}>
          <Spin /><span>{t('apps.v2Loading')}</span>
        </div>
      ) : (
        <div ref={panel} style={{ flex: launch.layout === 'viewport' ? '1 1 0' : '0 0 auto', minHeight: 64, minWidth: 0, overflow: 'hidden' }}>
          <AppFrameV2 source={launch.source} title={copy!.name} binding={launch.binding}
            capabilities={launch.capabilities} backendAvailable={launch.backendAvailable} onDraftChange={setDirty} presentation={presentation!} height={height} />
        </div>
      )}
    </section>
  )
}
