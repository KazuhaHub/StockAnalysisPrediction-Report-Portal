import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { Alert, Button, Result } from 'antd'
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

/** A compact notice in the shell; the current reading surface remains mounted and usable. */
export function ConnectionNotice() {
  const { t } = useTranslation()
  const { error, blocked, busy, retry } = useContext(ConnectionContext)
  if (!error || blocked) return null
  return <Alert style={{ width: '100%' }} type="warning" showIcon banner title={t('connection.notice')}
    action={<Button aria-label={t('connection.retry')} size="small" loading={busy} onClick={retry}>{t('connection.retry')}</Button>} />
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
