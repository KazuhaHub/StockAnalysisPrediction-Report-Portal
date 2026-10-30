import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { Button, Result, Spin, theme } from 'antd'
import { useLocation } from 'react-router'
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

/**
 * A compact floating status in the shell: it never pushes the page down, so the current reading
 * surface stays mounted, in place and usable. The dot turns into a spinner while a probe runs.
 */
export function ConnectionNotice() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const { error, blocked, busy, retry } = useContext(ConnectionContext)
  if (!error || blocked) return null
  return (
    <div
      role="alert"
      style={{
        position: 'fixed', top: 72, left: '50%', transform: 'translateX(-50%)', zIndex: token.zIndexPopupBase + 10,
        display: 'flex', alignItems: 'center', gap: 10, maxWidth: 'calc(100vw - 32px)',
        padding: '6px 6px 6px 14px', borderRadius: 999, background: token.colorBgElevated,
        border: `1px solid ${token.colorBorderSecondary}`, boxShadow: token.boxShadowSecondary,
        fontSize: token.fontSizeSM, lineHeight: '20px', color: token.colorText, whiteSpace: 'nowrap',
      }}
    >
      {busy ? (
        <Spin size="small" />
      ) : (
        <span aria-hidden style={{ width: 8, height: 8, borderRadius: '50%', flex: 'none', background: token.colorWarning, boxShadow: `0 0 0 3px ${token.colorWarningBg}` }} />
      )}
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{t('connection.notice')}</span>
      <Button aria-label={t('connection.retry')} type="text" size="small" disabled={busy} onClick={retry}
        style={{ borderRadius: 999, color: token.colorPrimary, fontSize: token.fontSizeSM }}>
        {t('connection.retryNow')}
      </Button>
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
