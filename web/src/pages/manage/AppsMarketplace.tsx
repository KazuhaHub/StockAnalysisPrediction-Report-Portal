import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Empty, Input, Segmented, Space, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { AppstoreOutlined, BarsOutlined, OrderedListOutlined, PlusOutlined } from '@ant-design/icons'
import { Link, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import CardTable from '../../components/CardTable'
import { appDisplayText, publisherLabel } from '../../lib/appDisplayV2'
import { usePrefs } from '../../prefs'
import AppReorderModal from './AppReorderModal'
import PlatformLifecycleModal from './PlatformLifecycleModal'
import { InstalledCard, InstalledControls, InstalledState, MarketDetailModal, RECOVERY_STATES } from './appStoreParts'
import { appPath, useAppLabels, type AdminApp } from './appsAdminModel'
import { AppIcon, ServiceStatus } from './appsAdminParts'
import { sourceName, useAppsConsoleFlow } from './appsConsoleFlow'
import { loadInstalledRelease } from './platformMarket'

type View = 'cards' | 'list'
const VIEW_KEY = 'rp.apps.view'
const readView = (): View => {
  try { return localStorage.getItem(VIEW_KEY) === 'list' ? 'list' : 'cards' } catch { return 'cards' }
}

/**
 * The installed apps: built-ins and platform installations, as compact cards (or a dense list), in the
 * administrator's order. The catalogue is read quietly so a card can offer an update and a version switch;
 * installing something new is a page of its own (/manage/apps/install).
 */
export default function AppsMarketplace({ apps, reload, onOpenTrust, active = true }: { apps: AdminApp[]; reload: (opts?: { quiet?: boolean }) => Promise<boolean>; onOpenTrust: () => void; active?: boolean }) {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const navigate = useNavigate()
  const labels = useAppLabels()
  const flow = useAppsConsoleFlow({ apps, reload, active })
  const { lifecycle, market, busy, acting, user, live, generation } = flow
  const [search, setSearch] = useState('')
  const [reordering, setReordering] = useState(false)
  const [view, setView] = useState<View>(readView)
  const chooseView = (next: View) => {
    setView(next)
    try { localStorage.setItem(VIEW_KEY, next) } catch { /* Storage is optional. */ }
  }
  // An app that needs recovery offers nothing but Manage, unless its release can still be uninstalled.
  const [uninstallable, setUninstallable] = useState<Record<string, boolean>>({})
  const probed = useRef(new Set<string>())
  useEffect(() => { setUninstallable({}); probed.current = new Set() }, [user])
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

  const activeSource = market.sources.find(s => s.url === market.active)
  const matches = (text: string) => !search.trim() || text.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())
  const shown = apps.filter(a => {
    const cat = flow.catalogueOf(a)
    const copy = cat ? appDisplayText(cat.app_id, cat.display, lang) : null
    return matches(`${copy?.name ?? labels.title(a)} ${a.key} ${a.appId ?? ''} ${a.name}`)
  })

  // Everything one app needs to show itself, in either view.
  const describe = (a: AdminApp) => {
    const cat = flow.catalogueOf(a)
    const copy = cat ? appDisplayText(cat.app_id, cat.display, lang) : null
    return {
      app: a, catalogue: cat,
      name: copy?.name && copy.name !== cat?.app_id ? copy.name : labels.title(a),
      description: a.builtin ? labels.description(a) : copy?.description ?? '',
      publisher: cat ? publisherLabel(cat.publisher_name, cat.publisher_id) : undefined,
      source: !a.builtin && cat && activeSource ? sourceName(activeSource.name, activeSource.url, activeSource.official) : undefined,
      uninstallable: !!a.installationId && uninstallable[a.installationId],
      busy: acting === a.key || (!!market.staging && market.staging.appId === a.appId),
      onSwitchVersion: (version: string) => { if (cat && !busy) void flow.switchVersion(a, cat, version) },
      onLifecycle: (action: Parameters<typeof flow.act>[1], opener?: HTMLElement | null) => { if (!busy) void flow.act(a, action, opener) },
      onViewInCatalogue: () => { if (cat) flow.openDetail(cat) },
    }
  }

  const columns: ColumnsType<AdminApp> = [
    {
      title: t('appStore.col.app'),
      render: (_: unknown, a: AdminApp) => {
        const d = describe(a)
        return <Space size={10} className="rp-app-list-app">
          <AppIcon app={a} size={28} />
          <div className="rp-app-list-name">
            <Link to={appPath(a.key)}>{d.name}</Link>
            <div><Typography.Text type="secondary" className="rp-app-card-sub">{[d.publisher ?? a.appId, a.version].filter(Boolean).join(' · ')}</Typography.Text></div>
          </div>
        </Space>
      },
    },
    { title: t('appStore.col.status'), render: (_: unknown, a: AdminApp) => <InstalledState app={a} /> },
    { title: t('appStore.col.access'), render: (_: unknown, a: AdminApp) => <ServiceStatus app={a} /> },
    { title: '', align: 'right', render: (_: unknown, a: AdminApp) => { const { app, name: _name, description: _desc, publisher: _pub, source: _src, ...controls } = describe(a); return <InstalledControls app={app} {...controls} /> } },
  ]

  return <Space orientation="vertical" size={16} style={{ width: '100%' }}>
    <div className="rp-app-toolbar">
      <Input.Search allowClear className="rp-app-search" aria-label={t('apps.search')} placeholder={t('apps.search')} value={search} onChange={e => setSearch(e.target.value)} />
      <Space wrap>
        <Segmented<View> value={view} onChange={chooseView} options={[
          { value: 'cards', label: <span aria-label={t('appStore.view.cards')} title={t('appStore.view.cards')}><AppstoreOutlined aria-hidden="true" /></span> },
          { value: 'list', label: <span aria-label={t('appStore.view.list')} title={t('appStore.view.list')}><BarsOutlined aria-hidden="true" /></span> },
        ]} />
        <Button icon={<OrderedListOutlined aria-hidden="true" />} onClick={() => setReordering(true)} disabled={apps.length < 2}>{t('apps.reorder')}</Button>
        <Button type="primary" icon={<PlusOutlined aria-hidden="true" />} onClick={() => navigate('/manage/apps/install')}>{t('appStore.install')}</Button>
      </Space>
    </div>

    {lifecycle.hiddenPending && <Alert type="warning" showIcon title={t('appPlatform.uncertain')} action={<Button onClick={lifecycle.reopen}>{t('appPlatform.retrySame')}</Button>} />}

    <section aria-label={t('appStore.installed')}>
      {!shown.length
        ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={search.trim() ? t('appStore.noMatches') : t('apps.empty')} />
        : view === 'cards'
          ? <div className="rp-app-grid">{shown.map(a => <InstalledCard key={a.key} {...describe(a)} />)}</div>
          : <CardTable<AdminApp> className="rp-app-list" rowKey="key" dataSource={shown} columns={columns} pagination={false} size="small" filtered={!!search.trim()} onClearFilters={() => setSearch('')} />}
    </section>

    <MarketDetailModal app={flow.detail} installed={flow.detail ? flow.installedOf(flow.detail) : undefined} lang={lang} staging={market.staging && flow.detail && market.staging.appId === flow.detail.app_id ? market.staging : null}
      blocked={busy} error={flow.detailError || market.stageError} onClose={() => flow.setDetail(null)}
      onInstall={version => flow.detail && void flow.installFromDetail(flow.detail, version)} onOpenTrust={() => { flow.setDetail(null); onOpenTrust() }} />

    <PlatformLifecycleModal lifecycle={lifecycle} />
    <AppReorderModal open={reordering} apps={apps} onClose={() => setReordering(false)} onSaved={() => void reload({ quiet: true })} />
  </Space>
}
