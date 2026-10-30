import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { Button, Result, Spin, theme } from 'antd'
import { useLocation } from 'react-router'
import { CheckCircleFilled } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, clearConnectionError, getConnectionError, subscribeConnection } from '../api/client'
import { useAuth } from '../auth'

const ConnectionContext = createContext({ error: null as string | null, blocked: false, busy: false, retry: () => {} })

export default function ConnectionBoundary({ children }: { children: ReactNode }) {
  const { refresh, loading } = useAuth()
  const { pathname, search } = useLocation()
  const route = pathname + search
  const error = useSyncExternalStore(subscribeConnection, getConnectionError)
  // An outage belongs to the page that was already open. It must not replace that page's
  // content, even when a background poll fails or a form submission cannot connect.
  const previousLoading = useRef(loading)
  useLayoutEffect(() => { previousLoading.current = loading }, [loading])
  const origin = useRef<{ route: string; initial: boolean } | null>(null)
  if (!error) origin.current = null
  else if (!origin.current) origin.current = { route, initial: loading || previousLoading.current }
  const blocked = !!error && (origin.current?.initial === true || origin.current?.route !== route)
  const [busy, setBusy] = useState(false)
  const pending = useRef(false)
  const retry = useCallback(async () => {
    if (pending.current) return
    pending.current = true
    setBusy(true)
    try {
      await api.get('/api/site')
      clearConnectionError()
      await refresh()
    } catch { /* Preserve the notice until a connection probe succeeds. */ }
    finally { pending.current = false; setBusy(false) }
  }, [refresh])
  useEffect(() => {
    if (!error) return
    const online = () => { void retry() }
    const probe = () => { if (document.visibilityState === 'visible') void retry() }
    window.addEventListener('online', online)
    const timer = window.setInterval(probe, 15_000)
    return () => { window.removeEventListener('online', online); window.clearInterval(timer) }
  }, [error, retry])
  return <ConnectionContext.Provider value={{ error, blocked, busy, retry }}>{children}</ConnectionContext.Provider>
}

const RECOVERED_MS = 1500
const LEAVE_MS = 220

/**
 * A thin strip pinned right under the top bar, styled like the site-wide update banner: full-width
 * tint, one centred group of status dot, message and a quiet retry. It slides down when the
 * connection drops, its dot pulses while it waits (a spinner while a probe runs), and on recovery it
 * turns green for a moment before sliding away. The current reading surface stays mounted underneath.
 * `top` is where it sticks: under the app header by default, 0 on pages that have no header.
 */
export function ConnectionNotice({ top = 'var(--rp-header-h, 64px)' }: { top?: number | string }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const { error, blocked, busy, retry } = useContext(ConnectionContext)
  const down = !!error && !blocked
  // 'idle' -> 'down' while disconnected; a recovery passes through 'recovered' and 'leaving'. A
  // disconnect that turns into the blocked full-page state skips the confirmation: that page says it.
  const [phase, setPhase] = useState<'idle' | 'down' | 'recovered' | 'leaving'>('idle')
  useEffect(() => {
    if (down) { setPhase('down'); return }
    if (blocked) { setPhase('idle'); return }
    setPhase((p) => (p === 'down' ? 'recovered' : p))
  }, [down, blocked])
  useEffect(() => {
    if (phase !== 'recovered' && phase !== 'leaving') return
    const timer = window.setTimeout(() => setPhase(phase === 'recovered' ? 'leaving' : 'idle'), phase === 'recovered' ? RECOVERED_MS : LEAVE_MS)
    return () => window.clearTimeout(timer)
  }, [phase])
  const recovered = !down && (phase === 'recovered' || phase === 'leaving')
  if (!down && !recovered) return null
  return (
    <div
      role={recovered ? 'status' : 'alert'}
      className={`rp-connection-strip${phase === 'leaving' ? ' rp-connection-strip--leaving' : ''}`}
      style={{
        position: 'sticky', top, zIndex: 20,
        background: recovered ? token.colorSuccessBg : token.colorWarningBg,
        borderBottom: `1px solid ${recovered ? token.colorSuccessBorder : token.colorWarningBorder}`,
        transition: 'background-color .3s ease, border-color .3s ease',
      }}
    >
      <div className="rp-connection-strip__inner">
        <div
          style={{
            display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 10,
            minHeight: 36, padding: '4px 12px', fontSize: 13, color: token.colorText,
          }}
        >
          <span aria-hidden style={{ display: 'inline-flex', width: 14, justifyContent: 'center', flexShrink: 0 }}>
            {recovered ? (
              <CheckCircleFilled style={{ color: token.colorSuccess, fontSize: 14 }} />
            ) : busy ? (
              <Spin size="small" />
            ) : (
              <span className="rp-connection-dot" style={{ width: 8, height: 8, borderRadius: '50%', background: token.colorWarning, color: token.colorWarning }} />
            )}
          </span>
          <span style={{ minWidth: 0 }}>{t(recovered ? 'connection.recovered' : 'connection.notice')}</span>
          {!recovered && (
            <Button aria-label={t('connection.retry')} type="link" size="small" disabled={busy} onClick={retry}
              style={{ padding: 0, height: 'auto', fontSize: 13 }}>
              {t('connection.retryNow')}
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}

/** Only the destination content is unavailable. The signed-in navigation stays outside this boundary. */
export function ConnectionContent({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  const { error, blocked, busy, retry } = useContext(ConnectionContext)
  if (!blocked) return <>{children}</>
  return <div data-testid="connection-page" style={{ width: '100%', padding: '32px 16px' }}>
    <Result status="warning" title={t('connection.title')} subTitle={t(`err.${error}`)}
      extra={<Button aria-label={t('connection.retry')} type="primary" loading={busy} onClick={retry}>{t('connection.retry')}</Button>} />
  </div>
}
