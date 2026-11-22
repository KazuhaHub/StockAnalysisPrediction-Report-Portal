import { useEffect, useMemo, useRef, useState, type ChangeEvent } from 'react'
import { Alert, Button, Collapse, Dropdown, Empty, Input, Modal, Select, Space, Spin, Typography } from 'antd'
import { CloudUploadOutlined, DownOutlined, LinkOutlined, OrderedListOutlined, PlusOutlined, ShopOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { errText } from '../../api/client'
import type { PlatformMarketApp, PlatformRelease } from '../../api/appPlatform'
import { appDisplayText } from '../../lib/appDisplayV2'
import { useAuth } from '../../auth'
import { notifyError } from '../../lib/notify'
import { usePrefs } from '../../prefs'
import AppReorderModal from './AppReorderModal'
import PlatformLifecycleModal from './PlatformLifecycleModal'
import { InstalledCard, MarketCard, MarketDetailModal, RECOVERY_STATES } from './appStoreParts'
import { useAppLabels, type AdminApp } from './appsAdminModel'
import { usePlatformLifecycle, type LifecycleAction } from './platformLifecycle'
import { loadInstalledRelease, usePlatformMarket } from './platformMarket'

const sourceName = (name: string, url: string, official: boolean) => {
  if (official) return name
  try { return new URL(url).host } catch { return url }
}

/**
 * One page for apps: what is installed, and what the catalogue offers that is not (the layout of Dify's plugin
 * page). Every install, update, disable, enable and uninstall runs in the one lifecycle modal.
 */
const sameApp = (a: AdminApp, c: { publisher_id: string; app_id: string }) => a.platform && !!a.publisherId && a.publisherId === c.publisher_id && a.appId === c.app_id

export default function AppsMarketplace({ apps, reload, onOpenTrust, active = true }: { apps: AdminApp[]; reload: (opts?: { quiet?: boolean }) => Promise<boolean>; onOpenTrust: () => void; active?: boolean }) {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const labels = useAppLabels()
  const lifecycle = usePlatformLifecycle(() => { void reload({ quiet: true }) })
  const market = usePlatformMarket(active)
  const { user } = useAuth()
  const owner = useRef(user)
  owner.current = user
  const mounted = useRef(false)
  const generation = useRef<object>({})
  const live = (account: string | null, epoch: object) => mounted.current && owner.current === account && generation.current === epoch
  const [search, setSearch] = useState('')
  const [reordering, setReordering] = useState(false)
  const [acting, setActing] = useState('')
  const [detail, setDetail] = useState<PlatformMarketApp | null>(null)
  const [detailError, setDetailError] = useState('')
  const [customOpen, setCustomOpen] = useState(false)
  const [customUrl, setCustomUrl] = useState('')
  const [open, setOpen] = useState<string[]>(['market'])
  const file = useRef<HTMLInputElement>(null)
  // An app that needs recovery offers nothing but Manage, unless its release can still be uninstalled.
  const [uninstallable, setUninstallable] = useState<Record<string, boolean>>({})
  const probed = useRef(new Set<string>())
  // Nothing an earlier account started may land on the next one's page.
  useEffect(() => {
    mounted.current = true
    generation.current = {}
    setActing(''); setDetail(null); setDetailError(''); setUninstallable({}); probed.current = new Set()
    return () => { mounted.current = false; generation.current = {} }
  }, [user])
  useEffect(() => {
    const account = user, epoch = generation.current
    const recovering = new Set(apps.filter(a => a.platform && a.installationId && RECOVERY_STATES.includes(a.state ?? '')).map(a => a.installationId!))
    // An app that left the recovery state is probed afresh if it ever returns to it.
    const left = [...probed.current].filter(id => !recovering.has(id))
    for (const id of left) probed.current.delete(id)
    if (left.length) setUninstallable(v => Object.fromEntries(Object.entries(v).filter(([id]) => recovering.has(id))))
    for (const id of recovering) {
      if (probed.current.has(id)) continue
      probed.current.add(id)
      loadInstalledRelease(id, t)
        .then(r => { if (live(account, epoch)) setUninstallable(v => ({ ...v, [id]: r.uninstallable === true })) })
        .catch(() => { if (live(account, epoch)) probed.current.delete(id) })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apps, user])
  const catalogueApps = market.catalogue?.status === 'ready' ? market.catalogue.apps : []
  const activeSource = market.sources.find(s => s.url === market.active)
  const customValid = (() => { try { return new URL(customUrl.trim()).protocol === 'https:' } catch { return false } })()
  const matches = (text: string) => !search.trim() || text.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())
  const busy = lifecycle.busy || !!lifecycle.intent || !!acting || !!market.staging || market.uploading

  // An application id is only unique within one publisher, so an installed app and a catalogue entry are the same
  // app only when both match.
  const catalogueOf = (a: AdminApp) => catalogueApps.find(c => sameApp(a, c))
  const installedOf = (c: PlatformMarketApp) => apps.find(a => sameApp(a, c))
  const installedKeys = useMemo(() => new Set(apps.filter(a => a.platform && a.publisherId && a.appId).map(a => `${a.publisherId}/${a.appId}`)), [apps])
  const shownInstalled = apps.filter(a => {
    const cat = catalogueOf(a)
    const copy = cat ? appDisplayText(cat.app_id, cat.display, lang) : null
    return matches(`${copy?.name ?? labels.title(a)} ${a.key} ${a.appId ?? ''} ${a.name}`)
  })
  const shownMarket = catalogueApps.filter(c => !installedKeys.has(`${c.publisher_id}/${c.app_id}`) && matches(`${appDisplayText(c.app_id, c.display, lang).name} ${c.app_id} ${c.publisher_id}`))

  // A staged release goes to the lifecycle modal: as an install, as an update when it is for an installed app, or as
  // an enable when it is the disabled installation's own release. Returns why not, when it cannot.
  const openStaged = (release: PlatformRelease, installation?: string): string => {
    if (release.installed) return t('appStore.alreadyInstalled')
    const target = installation ?? release.installation_id
    if (release.uninstallable && target) { lifecycle.begin({ ...release, installation_id: target }, 'uninstall'); return '' }
    if (release.enableable && target) { lifecycle.begin({ ...release, installation_id: target }, 'enable'); return '' }
    // A version chosen for an installed app is always an update: the server, not this page, refuses a downgrade.
    if (installation) { lifecycle.begin({ ...release, installation_id: installation }, 'update'); return '' }
    if (release.installable) { lifecycle.begin(release, 'install'); return '' }
    if (target) { lifecycle.begin({ ...release, installation_id: target }, 'update'); return '' }
    return t(`appPlatform.reason.${release.reason}`, { defaultValue: t('appMarket.verificationChanged') })
  }

  /** Downloads and verifies one version, then opens its flow. Resolves to the failure text, '' for success, null if nothing happened. */
  const stageAndOpen = async (app: PlatformMarketApp, version: string, installation?: string): Promise<string | null> => {
    const account = user, epoch = generation.current
    const result = await market.stage(app, version)
    if (!live(account, epoch) || !result) return null
    if ('error' in result) return result.error
    return openStaged(result.release, installation)
  }

  const installFromDetail = async (app: PlatformMarketApp, version: string) => {
    setDetailError('')
    const account = user, epoch = generation.current
    const problem = await stageAndOpen(app, version, installedOf(app)?.installationId)
    if (problem === null || !live(account, epoch)) return
    if (problem) setDetailError(problem)
    else setDetail(null)
  }

  const switchVersion = async (a: AdminApp, catalogue: PlatformMarketApp, version: string) => {
    const account = user, epoch = generation.current
    setActing(a.key)
    try {
      const problem = await stageAndOpen(catalogue, version, a.installationId)
      if (problem && live(account, epoch)) notifyError(problem)
    } finally { if (live(account, epoch)) setActing('') }
  }

  const act = async (a: AdminApp, action: LifecycleAction) => {
    if (!a.installationId) return
    const account = user, epoch = generation.current
    setActing(a.key)
    try {
      const release = await loadInstalledRelease(a.installationId, t)
      if (live(account, epoch)) lifecycle.begin(release, action)
    } catch (e) { if (live(account, epoch)) notifyError(errText(e, t)) }
    finally { if (live(account, epoch)) setActing('') }
  }

  const uploaded = async (e: ChangeEvent<HTMLInputElement>) => {
    const chosen = e.target.files?.[0]
    e.target.value = ''
    if (!chosen) return
    const account = user, epoch = generation.current
    const result = await market.stageOffline(chosen)
    if (!live(account, epoch) || !result) return
    const problem = 'error' in result ? result.error : openStaged(result.release)
    if (problem) notifyError(problem)
  }

  const showCatalogue = () => {
    setOpen(v => v.includes('market') ? v : [...v, 'market'])
    setTimeout(() => document.getElementById('rp-market-section')?.scrollIntoView?.({ block: 'start', behavior: window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' }), 0)
  }
  const openCustom = () => {
    market.addCustom(customUrl)
    setCustomOpen(false); setCustomUrl('')
    setOpen(v => v.includes('market') ? v : [...v, 'market'])
  }

  const marketBody = () => {
    const state = market.catalogue
    if (market.sourcesState === 'uninitialised') return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('appStore.platformNotReady')}><Button onClick={onOpenTrust}>{t('appStore.openTrust')}</Button></Empty>
    if (!market.sources.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={market.sourcesState === 'loading' ? <Spin /> : market.sourcesState === 'failed' ? t('appStore.sourcesFailed') : t('appStore.sourceNone')} />
    if (!state || state.status === 'loading') return <div className="rp-app-grid-state"><Spin /></div>
    if (state.status === 'error') return <Alert type="error" title={state.error} action={<Button onClick={market.reload}>{t('appStore.retry')}</Button>} />
    if (!shownMarket.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={search.trim() ? t('appStore.noMatches') : t('appStore.catalogueEmpty')} />
    return <div className="rp-app-grid rp-app-grid--market">
      {shownMarket.map(c => {
        const copy = appDisplayText(c.app_id, c.display, lang)
        return <MarketCard key={`${c.publisher_id}/${c.app_id}`} app={c} name={copy.name} description={copy.description} onOpen={() => { market.clearStageError(); setDetailError(''); setDetail(c) }} />
      })}
    </div>
  }

  return <Space orientation="vertical" size={16} style={{ width: '100%' }}>
    <div className="rp-app-toolbar">
      <Input.Search allowClear className="rp-app-search" aria-label={t('apps.search')} placeholder={t('apps.search')} value={search} onChange={e => setSearch(e.target.value)} />
      <Dropdown trigger={['click']} menu={{ items: [
        { key: 'catalogue', icon: <ShopOutlined aria-hidden="true" />, label: t('appStore.install.catalogue') },
        { key: 'custom', icon: <LinkOutlined aria-hidden="true" />, label: t('appStore.install.custom') },
        { key: 'offline', icon: <CloudUploadOutlined aria-hidden="true" />, label: t('appStore.install.offline') },
      ], onClick: ({ key }) => { if (key === 'catalogue') showCatalogue(); else if (key === 'custom') setCustomOpen(true); else file.current?.click() } }}>
        <Button type="primary" icon={<PlusOutlined aria-hidden="true" />} loading={market.uploading} disabled={busy && !market.uploading}>
          {market.uploading ? t('appStore.offlineBusy') : t('appStore.install')} <DownOutlined aria-hidden="true" />
        </Button>
      </Dropdown>
      <input ref={file} type="file" accept=".portalapp" hidden data-testid="offline-package" aria-label={t('appStore.install.offline')} onChange={e => void uploaded(e)} />
    </div>

    {lifecycle.hiddenPending && <Alert type="warning" showIcon title={t('appPlatform.uncertain')} action={<Button onClick={lifecycle.reopen}>{t('appPlatform.retrySame')}</Button>} />}

    <section aria-labelledby="rp-installed-title">
      <div className="rp-app-section-head">
        <Typography.Title level={5} id="rp-installed-title" style={{ margin: 0 }}>{t('appStore.installed')} ({apps.length})</Typography.Title>
        <Button icon={<OrderedListOutlined aria-hidden="true" />} onClick={() => setReordering(true)} disabled={apps.length < 2}>{t('apps.reorder')}</Button>
      </div>
      {shownInstalled.length ? <div className="rp-app-grid rp-app-grid--installed">
        {shownInstalled.map(a => {
          const cat = catalogueOf(a)
          const copy = cat ? appDisplayText(cat.app_id, cat.display, lang) : null
          return <InstalledCard key={a.key} app={a}
            name={copy?.name && copy.name !== cat?.app_id ? copy.name : labels.title(a)}
            description={a.builtin ? labels.description(a) : copy?.description ?? ''}
            publisher={cat?.publisher_id}
            source={!a.builtin && cat && activeSource ? sourceName(activeSource.name, activeSource.url, activeSource.official) : undefined}
            catalogue={cat}
            uninstallable={!!a.installationId && uninstallable[a.installationId]}
            busy={acting === a.key || (!!market.staging && market.staging.appId === a.appId)}
            onSwitchVersion={version => { if (cat && !busy) void switchVersion(a, cat, version) }}
            onLifecycle={action => { if (!busy) void act(a, action) }}
            onViewInCatalogue={() => { if (cat) { market.clearStageError(); setDetailError(''); setDetail(cat) } }} />
        })}
      </div> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={search.trim() ? t('appStore.noMatches') : t('apps.empty')} />}
    </section>

    <section id="rp-market-section">
      <Collapse className="rp-market-collapse" activeKey={open} onChange={keys => setOpen(Array.isArray(keys) ? keys : [keys])} items={[{
        key: 'market',
        label: <Typography.Text strong>{t('appStore.fromCatalogue')}</Typography.Text>,
        extra: market.sources.length > 0 && <Space onClick={e => e.stopPropagation()} onKeyDown={e => e.stopPropagation()}>
          <Select size="small" className="rp-market-source" aria-label={t('appStore.source')} value={market.active} onChange={market.setActive} popupMatchSelectWidth={false}
            options={market.sources.map(s => ({ value: s.url, label: `${t('appStore.source')}: ${sourceName(s.name, s.url, s.official)}` }))} />
        </Space>,
        children: marketBody(),
      }]} />
    </section>

    <MarketDetailModal app={detail} installed={detail ? installedOf(detail) : undefined} lang={lang} stagingVersion={market.staging && detail && market.staging.appId === detail.app_id ? market.staging.version : null}
      blocked={busy} error={detailError || market.stageError} onClose={() => { if (!market.staging) setDetail(null) }}
      onInstall={version => detail && void installFromDetail(detail, version)} onOpenTrust={() => { setDetail(null); onOpenTrust() }} />

    <Modal open={customOpen} title={t('appStore.customTitle')} okText={t('appStore.customOpen')} cancelText={t('appPlatform.cancel')} okButtonProps={{ disabled: !customValid }} onOk={openCustom} onCancel={() => setCustomOpen(false)} destroyOnHidden>
      <Space orientation="vertical" style={{ width: '100%' }}>
        <Typography.Text>{t('appStore.customPrompt')}</Typography.Text>
        <Input aria-label={t('appStore.customPrompt')} placeholder="https://…/index.json" maxLength={2048} value={customUrl} status={customUrl && !customValid ? 'error' : undefined} onChange={e => setCustomUrl(e.target.value)} onPressEnter={() => { if (customValid) openCustom() }} />
        {customUrl && !customValid && <Typography.Text type="secondary">{t('appStore.customInvalid')}</Typography.Text>}
      </Space>
    </Modal>

    <PlatformLifecycleModal lifecycle={lifecycle} />
    <AppReorderModal open={reordering} apps={apps} onClose={() => setReordering(false)} onSaved={() => void reload({ quiet: true })} />
  </Space>
}
