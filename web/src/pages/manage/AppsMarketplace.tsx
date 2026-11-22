import { useEffect, useMemo, useRef, useState, type ChangeEvent } from 'react'
import { Alert, Button, Collapse, Dropdown, Empty, Input, Modal, Select, Space, Spin, Typography } from 'antd'
import { CloudUploadOutlined, DownOutlined, LinkOutlined, OrderedListOutlined, PlusOutlined, ShopOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { errText } from '../../api/client'
import type { PlatformMarketApp, PlatformRelease } from '../../api/appPlatform'
import { appDisplayText } from '../../lib/appDisplayV2'
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
export default function AppsMarketplace({ apps, reload, onOpenTrust }: { apps: AdminApp[]; reload: (opts?: { quiet?: boolean }) => Promise<boolean>; onOpenTrust: () => void }) {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const labels = useAppLabels()
  const lifecycle = usePlatformLifecycle(() => { void reload({ quiet: true }) })
  const market = usePlatformMarket()
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
  useEffect(() => {
    for (const a of apps) {
      const id = a.installationId
      if (!a.platform || !id || !RECOVERY_STATES.includes(a.state ?? '') || probed.current.has(id)) continue
      probed.current.add(id)
      loadInstalledRelease(id).then(r => setUninstallable(v => ({ ...v, [id]: r.uninstallable === true }))).catch(() => probed.current.delete(id))
    }
  }, [apps])
  const catalogueApps = market.catalogue?.status === 'ready' ? market.catalogue.apps : []
  const activeSource = market.sources.find(s => s.url === market.active)
  const customValid = (() => { try { return new URL(customUrl.trim()).protocol === 'https:' } catch { return false } })()
  const matches = (text: string) => !search.trim() || text.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())
  const busy = lifecycle.busy || !!lifecycle.intent || !!acting || !!market.staging || market.uploading

  const catalogueOf = (a: AdminApp) => a.platform && a.appId ? catalogueApps.find(c => c.app_id === a.appId) : undefined
  const installedIds = useMemo(() => new Set(apps.filter(a => a.platform && a.appId).map(a => a.appId)), [apps])
  const shownInstalled = apps.filter(a => {
    const cat = catalogueOf(a)
    const copy = cat ? appDisplayText(cat.app_id, cat.display, lang) : null
    return matches(`${copy?.name ?? labels.title(a)} ${a.key} ${a.appId ?? ''} ${a.name}`)
  })
  const shownMarket = catalogueApps.filter(c => !installedIds.has(c.app_id) && matches(`${appDisplayText(c.app_id, c.display, lang).name} ${c.app_id} ${c.publisher_id}`))

  // A staged release goes to the lifecycle modal as an install, or as an update when the app is already installed.
  const openStaged = (release: PlatformRelease, installation?: string): string => {
    // A version chosen for an installed app is always an update: the server, not this page, refuses a downgrade.
    if (installation) { lifecycle.begin({ ...release, installation_id: installation }, 'update'); return '' }
    if (release.installable) { lifecycle.begin(release, 'install'); return '' }
    const target = release.installation_id
    if (target) { lifecycle.begin({ ...release, installation_id: target }, 'update'); return '' }
    return t(`appPlatform.reason.${release.reason}`, { defaultValue: t('appMarket.verificationChanged') })
  }

  const install = async (app: PlatformMarketApp, version: string) => {
    setDetailError('')
    const result = await market.stage(app, version)
    if (!result) return
    if ('error' in result) return
    const problem = openStaged(result.release)
    if (problem) setDetailError(problem)
    else setDetail(null)
  }

  const switchVersion = async (a: AdminApp, catalogue: PlatformMarketApp, version: string) => {
    setActing(a.key)
    try {
      const result = await market.stage(catalogue, version)
      if (!result) return
      if ('error' in result) { notifyError(result.error); return }
      const problem = openStaged(result.release, a.installationId)
      if (problem) notifyError(problem)
    } finally { setActing('') }
  }

  const act = async (a: AdminApp, action: LifecycleAction) => {
    if (!a.installationId) return
    setActing(a.key)
    try { lifecycle.begin(await loadInstalledRelease(a.installationId), action) }
    catch (e) { notifyError(errText(e, t)) }
    finally { setActing('') }
  }

  const uploaded = async (e: ChangeEvent<HTMLInputElement>) => {
    const chosen = e.target.files?.[0]
    e.target.value = ''
    if (!chosen) return
    const result = await market.stageOffline(chosen)
    if (!result) return
    if ('error' in result) { notifyError(result.error); return }
    const problem = openStaged(result.release)
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
    if (!market.sources.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={market.sourcesLoaded ? t('appStore.sourceNone') : <Spin />} />
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
            source={a.builtin ? t('appStore.sourceBuiltin') : cat && activeSource ? sourceName(activeSource.name, activeSource.url, activeSource.official) : undefined}
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
      <Collapse activeKey={open} onChange={keys => setOpen(Array.isArray(keys) ? keys : [keys])} items={[{
        key: 'market',
        label: <Typography.Text strong>{t('appStore.fromCatalogue')}</Typography.Text>,
        extra: market.sources.length > 0 && <Space onClick={e => e.stopPropagation()} onKeyDown={e => e.stopPropagation()}>
          <Select size="small" aria-label={t('appStore.source')} value={market.active} onChange={market.setActive} popupMatchSelectWidth={false} style={{ minWidth: 160, maxWidth: 260 }}
            options={market.sources.map(s => ({ value: s.url, label: `${t('appStore.source')}: ${sourceName(s.name, s.url, s.official)}` }))} />
        </Space>,
        children: marketBody(),
      }]} />
    </section>

    <MarketDetailModal app={detail} lang={lang} stagingVersion={market.staging && detail && market.staging.appId === detail.app_id ? market.staging.version : null}
      blocked={busy} error={detailError || market.stageError} onClose={() => { if (!market.staging) setDetail(null) }}
      onInstall={version => detail && void install(detail, version)} onOpenTrust={() => { setDetail(null); onOpenTrust() }} />

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
