// Experimental presentation host only. The caller must already authorize and
// verify an immutable asset source and generation; this component grants no API
// token, capability, installation or backend authority. No route registers it.
import { useCallback, useLayoutEffect, useRef, useState } from 'react'
import { Alert, Button, Spin } from 'antd'
import { useTranslation } from 'react-i18next'
import { BRIDGE_DEADLINE_MS, createBridgeHost, parseBridgePresentation, type BridgeBinding, type BridgePresentation } from '../../lib/bridge-v2'
import './AppFrameV2.css'

export interface AppFrameV2Props {
  source: string
  title: string
  binding: BridgeBinding
  presentation: BridgePresentation
  capabilities: readonly string[]
  // Supplied by the host layout. Automatic document sizing is not implemented.
  height: number
}
function safeSource(source: string): boolean {
  if (source.length > 2048 || !source.startsWith('/app-assets/') || /[\\\s?#]/.test(source)) return false
  try {
    const url = new URL(source, window.location.origin)
    const decoded = decodeURIComponent(url.pathname)
    return url.origin === window.location.origin && url.pathname === source && !decoded.includes('//') && !decoded.includes('\\') && !/[\x00-\x20\x7f]/.test(decoded) && !decoded.split('/').some(part => part === '..' || part === '.')
  } catch { return false }
}
export default function AppFrameV2(props: AppFrameV2Props) {
  const { t } = useTranslation()
  if (!safeSource(props.source) || !parseBridgePresentation(props.presentation) || !Number.isInteger(props.height) || props.height < 64 || props.height > 16384) {
    return <Alert type="error" showIcon title={t('apps.v2Failed')} />
  }
  const identity = JSON.stringify([props.source, props.binding.installationId, props.binding.generation, props.binding.policyRevision, props.binding.releaseSha256, [...props.capabilities].sort()])
  return <FrameDocument key={identity} {...props} />
}
function FrameDocument({ source, title, binding, presentation, capabilities, height }: AppFrameV2Props) {
  const { t } = useTranslation()
  const frame = useRef<HTMLIFrameElement>(null)
  const session = useRef<ReturnType<typeof createBridgeHost> | null>(null)
  const assetTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const live = useRef(true)
  const epoch = useRef(0)
  const latest = useRef(presentation)
  latest.current = presentation
  const [ready, setReady] = useState(false)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const close = useCallback(() => {
    clearTimeout(assetTimer.current)
    epoch.current++
    session.current?.close()
    session.current = null
  }, [])
  const fail = useCallback(() => {
    if (!live.current) return
    close()
    setReady(false)
    setFailed(true)
  }, [close])
  function loaded() {
    close()
    setReady(false)
    if (!frame.current?.contentWindow) { fail(); return }
    const generation = epoch.current
    session.current = createBridgeHost({
      localWindow: window,
      frameWindow: frame.current.contentWindow,
      binding,
      presentation: latest.current,
      capabilities,
      onReady: () => { if (live.current && epoch.current === generation) setReady(true) },
      onFailure: () => { if (live.current && epoch.current === generation) fail() },
    })
  }
  useLayoutEffect(() => {
    live.current = true
    return () => { live.current = false; close() }
  }, [close])
  useLayoutEffect(() => {
    assetTimer.current = setTimeout(fail, BRIDGE_DEADLINE_MS)
    return () => clearTimeout(assetTimer.current)
  }, [attempt, fail])
  useLayoutEffect(() => {
    if (session.current && !session.current.updatePresentation(presentation)) fail()
  }, [presentation, fail])
  if (failed) return <Alert type="error" showIcon title={t('apps.v2Failed')} action={<Button onClick={() => { close(); setReady(false); setFailed(false); setAttempt(value => value + 1) }}>{t('apps.v2Retry')}</Button>} />
  return <div className="portal-app-frame-v2" aria-busy={!ready} style={{ height, background: presentation.theme.colorBgLayout, color: presentation.theme.colorText, colorScheme: presentation.theme.dark ? 'dark' : 'light' }}>
    <iframe key={attempt} ref={frame} src={source} title={title} sandbox="allow-scripts" referrerPolicy="no-referrer" onLoad={loaded} aria-hidden={ready ? undefined : true} tabIndex={ready ? 0 : -1} style={{ visibility: ready ? 'visible' : 'hidden', border: 0, background: presentation.theme.colorBgLayout, colorScheme: presentation.theme.dark ? 'dark' : 'light' }} />
    {!ready && <div className="portal-app-frame-v2-cover" role="status"><Spin aria-hidden="true" /><span>{t('apps.v2Loading')}</span></div>}
  </div>
}
