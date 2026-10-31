// Experimental app host. The caller supplies an admitted document binding;
// backend calls still require current server authorization. No token is shared.
import { useCallback, useLayoutEffect, useRef, useState } from 'react'
import { Alert, Button, Spin } from 'antd'
import { useTranslation } from 'react-i18next'
import { BRIDGE_DEADLINE_MS, createBridgeHost, parseBridgePresentation, type BridgeBinding, type BridgePresentation, type PageActionSnapshot } from '../../lib/bridge-v2'
import './AppFrameV2.css'
import { createAppBackendTransport } from '../../lib/appBackendV2'
import { createAppNetworkTransport } from '../../lib/appNetworkV2'
import { createAppReportsTransport } from '../../lib/appReportsV2'
import { createAppObjectsTransport } from '../../lib/appObjectsV2'

export interface AppFrameV2Props {
  source: string
  title: string
  binding: BridgeBinding
  presentation: BridgePresentation
  capabilities: readonly string[]
  backendAvailable?: boolean
  onPageChange?: (snapshot:PageActionSnapshot|null)=>void
  onDraftChange?: (dirty: boolean) => void
  onNavigate?: (path:string,replace:boolean,search:string)=>void
  // Viewport height, also used as the initial document loading-cover height.
  height: number
}
function safeSource(source: string, binding: BridgeBinding): boolean {
  if (source.length > 2048 || (!source.startsWith('/app-assets/') && !source.startsWith('/app-documents/')) || /[\\\s?#]/.test(source)) return false
  try {
    const url = new URL(source, window.location.origin)
    const decoded = decodeURIComponent(url.pathname)
    if (source.startsWith('/app-documents/')) {
      const parts = source.split('/')
      if (parts.length < 8 || parts[2] !== binding.installationId || parts[3] !== binding.generation ||
        parts[4] !== binding.policyRevision || parts[5] !== binding.releaseSha256 ||
        !/^[a-f0-9]{64}$/.test(parts[6]) || decoded !== source) return false
    }
    return url.origin === window.location.origin && url.pathname === source && !decoded.includes('//') && !decoded.includes('\\') && !/[\x00-\x20\x7f]/.test(decoded) && !decoded.split('/').some(part => part === '..' || part === '.')
  } catch { return false }
}
export default function AppFrameV2(props: AppFrameV2Props) {
  const { t } = useTranslation()
  if (!safeSource(props.source, props.binding) || !parseBridgePresentation(props.presentation) || !Number.isInteger(props.height) || props.height < 64 || props.height > 16384) {
    return <Alert type="error" showIcon title={t('apps.v2Failed')} />
  }
  const identity = JSON.stringify([props.source, props.binding.installationId, props.binding.generation, props.binding.policyRevision, props.binding.releaseSha256, [...props.capabilities].sort(), props.backendAvailable === true])
  return <FrameDocument key={identity} {...props} />
}
function FrameDocument({ source, title, binding, presentation, capabilities, height, backendAvailable, onDraftChange, onNavigate, onPageChange }: AppFrameV2Props) {
  const { t } = useTranslation()
  const frame = useRef<HTMLIFrameElement>(null)
  const navigationObserver=useRef(onNavigate)
  navigationObserver.current=onNavigate
  const pageObserver=useRef(onPageChange)
  pageObserver.current=onPageChange
  const draftObserver = useRef(onDraftChange)
  draftObserver.current = onDraftChange
  const session = useRef<ReturnType<typeof createBridgeHost> | null>(null)
  const assetTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const live = useRef(true)
  const epoch = useRef(0)
  const latest = useRef(presentation)
  latest.current = presentation
  const [ready, setReady] = useState(false)
  const readyRef = useRef(false)
  readyRef.current = ready
  const [documentHeight, setDocumentHeight] = useState<number | null>(null)
  const measureTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const close = useCallback(() => {
    clearTimeout(assetTimer.current)
    clearTimeout(measureTimer.current)
    epoch.current++
    session.current?.close()
    session.current = null
    draftObserver.current?.(false)
    pageObserver.current?.(null)
  }, [])
  const fail = useCallback(() => {
    if (!live.current) return
    close()
    setReady(false)
    setDocumentHeight(null)
    setFailed(true)
  }, [close])
  function loaded() {
    close()
    setReady(false)
    setDocumentHeight(null)
    if (!frame.current?.contentWindow) { fail(); return }
    const generation = epoch.current
    try { session.current = createBridgeHost({
      localWindow: window,
      frameWindow: frame.current.contentWindow,
      binding,
      presentation: latest.current,
      capabilities,
      onBackendRequest: backendAvailable ? createAppBackendTransport(binding) : undefined,
      onNetworkRequest: capabilities.includes('network.http') ? createAppNetworkTransport(binding) : undefined,
      onReportRequest: capabilities.includes('reports.read') ? createAppReportsTransport(binding) : undefined,
      onObjectRequest: capabilities.includes('storage.files') ? createAppObjectsTransport(binding) : undefined,
      onNavigate: (path,replace,search)=>{if(live.current&&epoch.current===generation)navigationObserver.current?.(path,replace,search)},
      onPageChange: value=>{if(live.current&&epoch.current===generation)pageObserver.current?.(value)},
      onDraftChange: value => { if (live.current && epoch.current === generation) draftObserver.current?.(value) },
      onReady: () => { if (live.current && epoch.current === generation) {
        setReady(true)
        if (latest.current.layout === 'document') measureTimer.current = setTimeout(fail, BRIDGE_DEADLINE_MS)
      } },
      onDocumentHeight: value => { if (live.current && epoch.current === generation) {
        clearTimeout(measureTimer.current)
        setDocumentHeight(Math.max(64, value))
      } },
      onFailure: () => { if (live.current && epoch.current === generation) fail() },
    }) } catch { fail() }
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
  const layout = presentation.layout
  useLayoutEffect(() => {
    setDocumentHeight(null)
    if (readyRef.current && layout === 'document') measureTimer.current = setTimeout(fail, BRIDGE_DEADLINE_MS)
    return () => clearTimeout(measureTimer.current)
    // The initial ready transition starts its own deadline above.
  }, [layout, fail])
  const visible = ready && (layout === 'viewport' || documentHeight !== null)
  if (failed) return <Alert type="error" showIcon title={t('apps.v2Failed')} action={<Button onClick={() => { close(); setReady(false); setFailed(false); setAttempt(value => value + 1) }}>{t('apps.v2Retry')}</Button>} />
  return <div className="portal-app-frame-v2" aria-busy={!visible} style={{ height: layout === 'document' ? documentHeight ?? height : height, background: presentation.theme.colorBgLayout, color: presentation.theme.colorText, colorScheme: presentation.theme.dark ? 'dark' : 'light' }}>
    <iframe key={attempt} ref={frame} src={source} title={title} sandbox="allow-scripts" referrerPolicy="no-referrer" onLoad={loaded} aria-hidden={visible ? undefined : true} tabIndex={visible ? 0 : -1} style={{ visibility: visible ? 'visible' : 'hidden', border: 0, background: presentation.theme.colorBgLayout, colorScheme: presentation.theme.dark ? 'dark' : 'light' }} />
    {!visible && <div className="portal-app-frame-v2-cover" role="status"><Spin aria-hidden="true" /><span>{t('apps.v2Loading')}</span></div>}
  </div>
}
