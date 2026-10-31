import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Empty, Input, Space, Table, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../../auth'
import { api, errText } from '../../api/client'
import { parsePlatformMarket, type PlatformMarketHint, type PlatformRelease } from '../../api/appPlatform'

const root = '/api/admin/app-platform'

export default function AppPlatformMarketPanel({ disabled, onReady, onBusyChange }: { disabled: boolean; onReady: (release: PlatformRelease) => void; onBusyChange: (busy: boolean) => void }) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const [address, setAddress] = useState('')
  const [source, setSource] = useState('')
  const [entries, setEntries] = useState<PlatformMarketHint[] | null>(null)
  const [busy, setBusy] = useState<'discover' | 'stage' | null>(null)
  const [error, setError] = useState('')
  const [ready, setReady] = useState(false)
  const owner = useRef(user)
  owner.current = user
  const mounted = useRef(false)
  const generation = useRef<object>({})
  const active = useRef(false)
  const reportBusy = useRef(onBusyChange)
  reportBusy.current = onBusyChange
  const hostDisabled = useRef(disabled)
  hostDisabled.current = disabled
  const live = (account: string | null, epoch: object) => mounted.current && owner.current === account && generation.current === epoch

  useEffect(() => {
    mounted.current = true
    const epoch = {}
    generation.current = epoch
    active.current = false
    setAddress(''); setSource(''); setEntries(null); setBusy(null); setError(''); setReady(false)
    reportBusy.current(false)
    return () => { mounted.current = false; generation.current = {}; reportBusy.current(false) }
  }, [user])

  const changeAddress = (next: string) => {
    generation.current = {}
    active.current = false
    setAddress(next); setSource(''); setEntries(null); setError(''); setReady(false); setBusy(null)
    reportBusy.current(false)
  }

  const discover = async () => {
    if (active.current || disabled || !address.trim()) return
    const account = user, epoch = generation.current, requested = address.trim()
    active.current = true; setBusy('discover'); setError(''); setReady(false); setEntries(null)
    // Discovery does not change package state and remains independently editable.
    try {
      const response = await api.post<unknown>(`${root}/market/catalogue`, { catalogue_url: requested })
      if (!live(account, epoch)) return
      const parsed = parsePlatformMarket(response)
      if (!parsed) throw new Error(t('appMarket.invalidResponse'))
      setEntries(parsed); setSource(requested)
    } catch (e) { if (live(account, epoch)) setError(errText(e, t)) }
    finally { if (live(account, epoch)) { active.current = false; setBusy(null) } }
  }

  const stage = async (entry: PlatformMarketHint) => {
    if (active.current || disabled || !source) return
    const account = user, epoch = generation.current, requested = source
    active.current = true; setBusy('stage'); setError(''); setReady(false); reportBusy.current(true)
    try {
      const result = await api.post<{ release_sha256: string }>(`${root}/market/stage`, { catalogue_url: requested, release_sha256: entry.descriptor_sha256 })
      if (!live(account, epoch)) return
      if (result.release_sha256 !== entry.descriptor_sha256) throw new Error(t('appMarket.verificationChanged'))
      // Read current verified metadata by exact digest; a newly staged package
      // may be beyond the directory's first cursor page. Catalogue copy is never
      // promoted into a signed display/permission review.
      const response = await api.get<{ items: PlatformRelease[]; next_cursor: string }>(`${root}/releases?release_sha256=${entry.descriptor_sha256}`)
      if (!live(account, epoch)) return
      const verified = response.items?.length === 1 ? response.items[0] : undefined
      if (!verified || verified.release_sha256 !== entry.descriptor_sha256 || verified.app_id !== entry.app_id || verified.publisher_id !== entry.publisher_id || verified.version !== entry.version) throw new Error(t('appMarket.verificationChanged'))
      if (hostDisabled.current) return
      onReady(verified)
      setReady(true)
    } catch (e) { if (live(account, epoch)) setError(errText(e, t)) }
    finally { if (live(account, epoch)) { active.current = false; setBusy(null); reportBusy.current(false) } }
  }

  return <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
    <Typography.Title level={5}>{t('appMarket.title')}</Typography.Title>
    <Typography.Paragraph type="secondary">{t('appMarket.description')}</Typography.Paragraph>
    <Space.Compact style={{ width: '100%' }}>
      <Input aria-label={t('appMarket.catalogueURL')} placeholder="https://…/index.json" value={address} maxLength={2048} disabled={disabled || busy === 'stage'} onChange={e => changeAddress(e.target.value)} onPressEnter={() => void discover()} />
      <Button loading={busy === 'discover'} disabled={disabled || busy === 'stage' || !address.trim()} onClick={() => void discover()}>{t('appMarket.fetch')}</Button>
    </Space.Compact>
    {error && <Alert type="error" showIcon title={error} />}
    {ready && <Alert type="success" showIcon title={t('appMarket.ready')} />}
    {entries !== null && <>
      <Alert type="warning" showIcon title={t('appMarket.unverified')} description={t('appMarket.trustHint')} />
      <Table<PlatformMarketHint> rowKey="descriptor_sha256" dataSource={entries} pagination={{ pageSize: 10, showSizeChanger: false }} locale={{ emptyText: <Empty description={t('appMarket.empty')} /> }} columns={[
        { title: t('appPlatform.application'), render: (_, item) => <><Typography.Text strong>{item.app_id}</Typography.Text><div><Typography.Text type="secondary">{item.publisher_id} · {item.version}</Typography.Text></div><Tag>{t('appMarket.unverifiedTag')}</Tag></> },
        { title: t('appPlatform.action'), render: (_, item) => <Button disabled={disabled || busy !== null} loading={busy === 'stage'} onClick={() => void stage(item)}>{t('appMarket.verifyDownload')}</Button> },
      ]} />
    </>}
  </Space>
}
