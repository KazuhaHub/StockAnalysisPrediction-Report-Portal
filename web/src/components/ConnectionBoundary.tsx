import { useCallback, useEffect, useState, useSyncExternalStore, type ReactNode } from 'react'
import { Button, Result, theme } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, clearConnectionError, getConnectionError, subscribeConnection } from '../api/client'
import { useAuth } from '../auth'

export default function ConnectionBoundary({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const { refresh } = useAuth()
  const error = useSyncExternalStore(subscribeConnection, getConnectionError)
  const [busy, setBusy] = useState(false)
  const retry = useCallback(async () => {
    if (busy) return
    setBusy(true)
    try {
      await api.get('/api/site')
      clearConnectionError()
      await refresh()
    } catch { /* Keep the connection page until the server responds. */ }
    finally { setBusy(false) }
  }, [busy, refresh])
  useEffect(() => {
    if (!error) return
    const online = () => { void retry() }
    window.addEventListener('online', online)
    return () => window.removeEventListener('online', online)
  }, [error, retry])
  return <>
    <div style={{ height: '100%', display: error ? 'none' : 'contents' }}>{children}</div>
    {error && <div style={{ minHeight: '100dvh', display: 'grid', placeItems: 'center', padding: 24, background: token.colorBgLayout }}>
      <Result status="warning" title={t('connection.title')} subTitle={t(`err.${error}`)}
        extra={<Button type="primary" loading={busy} onClick={retry}>{t('connection.retry')}</Button>} />
    </div>}
  </>
}
