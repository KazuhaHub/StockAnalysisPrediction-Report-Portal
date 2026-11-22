import { useEffect, useRef, useState } from 'react'
import { Alert, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../../auth'
import { api, errText } from '../../api/client'
import { platformRoot } from './platformLifecycle'
import { parseSources, type OfficialSource } from './platformMarket'

/** Turns the built-in official catalogue on or off. It is a source setting, so it lives with the trusted publishers. */
export default function OfficialCatalogueSwitch() {
  const { t } = useTranslation()
  const { user } = useAuth()
  const [official, setOfficial] = useState<OfficialSource | null>(null)
  const [switching, setSwitching] = useState(false)
  const [error, setError] = useState('')
  const owner = useRef(user)
  owner.current = user
  const mounted = useRef(false)

  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])
  useEffect(() => {
    const abort = new AbortController()
    setOfficial(null); setError('')
    // The built-in catalogue is a preset only; a failed read leaves the switch out.
    api.get<unknown>(`${platformRoot}/market/sources`, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) setOfficial(parseSources(value)) }).catch(() => {})
    return () => abort.abort()
  }, [user])

  if (!official) return null
  const toggle = async (enabled: boolean) => {
    if (switching) return
    const account = user
    setSwitching(true); setError('')
    try {
      await api.post(`${platformRoot}/market/sources`, { official_enabled: enabled })
      if (mounted.current && owner.current === account) setOfficial({ ...official, enabled })
    } catch (e) { if (mounted.current && owner.current === account) setError(errText(e, t)) }
    finally { if (mounted.current && owner.current === account) setSwitching(false) }
  }
  return <Space orientation="vertical" size="small" style={{ width: '100%' }}>
    <Space wrap align="center">
      <Switch size="small" aria-label={t('appMarket.officialEnabled')} checked={official.enabled} loading={switching} onChange={checked => void toggle(checked)} />
      <Typography.Text strong>{official.name}</Typography.Text>
      <Typography.Text type="secondary">{official.enabled ? t('appMarket.officialEnabled') : t('appMarket.officialDisabled')}</Typography.Text>
    </Space>
    {error && <Alert type="error" showIcon title={error} />}
  </Space>
}
