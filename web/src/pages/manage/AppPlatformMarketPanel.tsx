import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Descriptions, Empty, Input, Space, Switch, Table, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../../auth'
import { api, errText } from '../../api/client'
import { parsePlatformMarket, type PlatformMarketHint, type PlatformRelease } from '../../api/appPlatform'

const root = '/api/admin/app-platform'
type OfficialSource = { name: string; url: string; enabled: boolean }
function parseSources(value: unknown): OfficialSource | null {
  const official = value && typeof value === 'object' ? (value as { official?: unknown }).official : undefined
  if (!official || typeof official !== 'object') return null
  const { name, url, enabled } = official as Record<string, unknown>
  return typeof name === 'string' && typeof url === 'string' && url.startsWith('https://') && typeof enabled === 'boolean' ? { name, url, enabled } : null
}

export default function AppPlatformMarketPanel({ disabled, onReady, onBusyChange }: { disabled: boolean; onReady: (release: PlatformRelease) => void; onBusyChange: (busy: boolean) => void }) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const [address, setAddress] = useState('')
  const [source, setSource] = useState('')
  const [entries, setEntries] = useState<PlatformMarketHint[] | null>(null)
  const [busy, setBusy] = useState<'discover' | 'stage' | null>(null)
  const [error, setError] = useState('')
  const [ready, setReady] = useState(false)
  const [verifiedRelease, setVerifiedRelease] = useState<PlatformRelease | null>(null)
  const [official, setOfficial] = useState<OfficialSource | null>(null)
  const [switching, setSwitching] = useState(false)
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
    setAddress(''); setSource(''); setEntries(null); setBusy(null); setError('')
    setReady(false); setVerifiedRelease(null)
    reportBusy.current(false)
    return () => { mounted.current = false; generation.current = {}; reportBusy.current(false) }
  }, [user])

  useEffect(() => {
    const abort = new AbortController()
    setOfficial(null)
    // The built-in catalogue is a preset only; a failed read leaves the URL field as the way in.
    api.get<unknown>(`${root}/market/sources`, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) setOfficial(parseSources(value)) }).catch(() => {})
    return () => abort.abort()
  }, [user])

  const toggleOfficial = async (enabled: boolean) => {
    if (!official || switching) return
    const account = user
    setSwitching(true); setError('')
    try {
      await api.post(`${root}/market/sources`, { official_enabled: enabled })
      if (mounted.current && owner.current === account) setOfficial({ ...official, enabled })
    } catch (e) { if (mounted.current && owner.current === account) setError(errText(e, t)) }
    finally { if (mounted.current && owner.current === account) setSwitching(false) }
  }

  const changeAddress = (next: string) => {
    generation.current = {}
    active.current = false
    setAddress(next); setSource(''); setEntries(null); setError('')
    setReady(false); setVerifiedRelease(null); setBusy(null)
    reportBusy.current(false)
  }

  const discover = async (preset?: string) => {
    const requested = (preset ?? address).trim()
    if (active.current || disabled || !requested) return
    const account = user, epoch = generation.current
    active.current = true; setBusy('discover'); setError(''); setReady(false)
    setVerifiedRelease(null); setEntries(null)
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
    active.current = true; setBusy('stage'); setError(''); setReady(false)
    setVerifiedRelease(null); reportBusy.current(true)
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
      setVerifiedRelease(verified)
      setReady(true)
    } catch (e) { if (live(account, epoch)) setError(errText(e, t)) }
    finally { if (live(account, epoch)) { active.current = false; setBusy(null); reportBusy.current(false) } }
  }

  return <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
    <Typography.Title level={5}>{t('appMarket.title')}</Typography.Title>
    <Typography.Paragraph type="secondary">{t('appMarket.description')}</Typography.Paragraph>
    {official && <Space wrap align="center">
      <Tag color="blue">{t('appMarket.builtin')}</Tag>
      <Typography.Text strong>{official.name}</Typography.Text>
      {official.enabled
        ? <Button disabled={disabled || busy !== null} loading={busy === 'discover' && address === official.url} onClick={() => { changeAddress(official.url); void discover(official.url) }}>{t('appMarket.browseOfficial')}</Button>
        : <Typography.Text type="secondary">{t('appMarket.officialDisabled')}</Typography.Text>}
      <Switch size="small" aria-label={t('appMarket.officialEnabled')} checked={official.enabled} loading={switching} disabled={disabled || busy !== null} onChange={checked => void toggleOfficial(checked)} />
    </Space>}
    <Space.Compact style={{ width: '100%' }}>
      <Input aria-label={t('appMarket.catalogueURL')} placeholder="https://…/index.json" value={address} maxLength={2048} disabled={disabled || busy === 'stage'} onChange={e => changeAddress(e.target.value)} onPressEnter={() => void discover()} />
      <Button loading={busy === 'discover'} disabled={disabled || busy === 'stage' || !address.trim()} onClick={() => void discover()}>{t('appMarket.fetch')}</Button>
    </Space.Compact>
    {error && <Alert type="error" showIcon title={error} />}
    {ready && <Alert type="success" showIcon title={t('appMarket.ready')} />}
    {verifiedRelease && <Descriptions title={t('appPlatform.application')} column={1} items={[
      { key: 'publisher', label: t('appPlatform.publisher'), children: verifiedRelease.publisher_id },
      { key: 'release', label: t('appPlatform.releaseDigest'), children: <Typography.Text code style={{ wordBreak: 'break-all' }}>{verifiedRelease.release_sha256}</Typography.Text> },
      { key: 'compatibility', label: t('appPlatform.compatibility'), children: verifiedRelease.compatibility ? `${verifiedRelease.compatibility.platform_api} · ${verifiedRelease.compatibility.runner_protocol}` : '—' },
      { key: 'required', label: t('appPlatform.requiredPermissions'), children: verifiedRelease.required_capabilities?.length ? verifiedRelease.required_capabilities.join(' · ') : t('appPlatform.none') },
      { key: 'optional', label: t('appPlatform.optionalPermissions'), children: verifiedRelease.optional_capabilities?.length ? verifiedRelease.optional_capabilities.join(' · ') : t('appPlatform.none') },
      { key: 'resources', label: t('appPlatform.resources'), children: verifiedRelease.backend && verifiedRelease.resources ? t('appPlatform.resourceSummary', { platforms: verifiedRelease.platforms?.join(', ') ?? t('appPlatform.none'), memory: verifiedRelease.resources.memory_mib, cpu: verifiedRelease.resources.cpu_millis, pids: verifiedRelease.resources.pids }) : t('appPlatform.frontendOnly') },
      { key: 'schema', label: t('appPlatform.storage'), children: t('appPlatform.dataSchema', { schema: verifiedRelease.data_schema ?? 0 }) },
    ]} />}
    {entries !== null && <>
      <Alert type="warning" showIcon title={t('appMarket.unverified')} description={t('appMarket.trustHint')} />
      <Table<PlatformMarketHint> rowKey="descriptor_sha256" dataSource={entries} pagination={{ pageSize: 10, showSizeChanger: false }} locale={{ emptyText: <Empty description={t('appMarket.empty')} /> }} columns={[
        { title: t('appPlatform.application'), render: (_, item) => <><Typography.Text strong>{item.app_id}</Typography.Text><div><Typography.Text type="secondary">{item.publisher_id} · {item.version}</Typography.Text></div><Tag>{t('appMarket.unverifiedTag')}</Tag></> },
        { title: t('appPlatform.action'), render: (_, item) => <Button disabled={disabled || busy !== null} loading={busy === 'stage'} onClick={() => void stage(item)}>{t('appMarket.verifyDownload')}</Button> },
      ]} />
    </>}
  </Space>
}
