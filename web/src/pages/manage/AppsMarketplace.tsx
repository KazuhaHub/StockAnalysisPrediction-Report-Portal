import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Empty, Input, Segmented, Space } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { AppstoreOutlined, BarsOutlined, OrderedListOutlined, PlusOutlined } from '@ant-design/icons'
import { Link, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import CardTable from '../../components/CardTable'
import { usePrefs } from '../../prefs'
import AppReorderModal from './AppReorderModal'
import PlatformLifecycleModal from './PlatformLifecycleModal'
import { InstalledCard, InstalledControls, InstalledOperation, InstalledState, PublisherLine } from './appStoreParts'
import { RECOVERY_STATES } from './InstallationActions'
import { appHref, installedDisplay, installedPublisher, useAppLabels, type AdminApp } from './appsAdminModel'
import { AppIcon, ServiceStatus } from './appsAdminParts'
import type { PickMode } from './platformLifecycle'
import { useAppsConsoleFlow } from './appsConsoleFlow'
import { useLatestOperations } from './useOperationFeed'
import { loadInstalledRelease } from './platformMarket'
import { notifyError } from '../../lib/notify'

type View = 'cards' | 'list'
const VIEW_KEY = 'rp.apps.view'
const readView = (): View => {
  try { return localStorage.getItem(VIEW_KEY) === 'list' ? 'list' : 'cards' } catch { return 'cards' }
}

/**
 * The installed apps: built-ins and platform installations, as compact cards (or a dense list), in the
 * administrator's order. Nothing is read from a catalogue here: Switch version opens a dialog that loads the
 * versions when it is pressed. Each card says what its newest operation is doing or how it ended. Installing
 * something new is a page of its own (/manage/apps/install).
 */
export default function AppsMarketplace({ apps, reload, active = true }: { apps: AdminApp[]; reload: (opts?: { quiet?: boolean }) => Promise<boolean>; active?: boolean }) {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const navigate = useNavigate()
  const labels = useAppLabels()
  // The catalogue stays unread (`active: false`): the version dialog turns it on when it opens.
  const flow = useAppsConsoleFlow({ apps, reload, active: false })
  const { lifecycle, busy, acting, user, live, generation } = flow
  // The newest operation of every installation, read again whenever the layout is.
  const operations = useLatestOperations(active, apps)
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

  // The first view needs only the layout (one local read, already behind the page's gate): it carries each app's verified
  // name, publisher, version and display.
  const matches = (text: string) => !search.trim() || text.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())
  const shown = apps.filter(a => matches(`${installedDisplay(a, lang, labels.title(a)).name} ${a.key} ${a.appId ?? ''} ${a.name}`))

  const refuse = () => notifyError(t(flow.busyReason === 'pending' ? 'appStore.busyPending' : 'appStore.busyWorking'))
  // Everything one app needs to show itself, in either view.
  const describe = (a: AdminApp) => {
    const copy = installedDisplay(a, lang, labels.title(a))
    return {
      app: a,
      name: copy.name,
      description: a.builtin ? labels.description(a) : copy.description,
      publisher: installedPublisher(a),
      uninstallable: !!a.installationId && uninstallable[a.installationId],
      busy: acting === a.key,
      blocked: flow.busyReason,
      onRefuse: refuse,
      operation: a.installationId ? operations[a.installationId] : undefined,
      onOpenOperation: (id: string, opener: HTMLElement) => lifecycle.showOperation(id, opener),
      // The controls stay reachable while blocked, so this is only a race (state changed between render and click):
      // it still answers instead of dropping the click.
      onPick: (mode: PickMode, opener?: HTMLElement | null) => {
        if (busy) { refuse(); return }
        flow.pickVersion(a, copy.name, mode, opener)
      },
      onLifecycle: (action: Parameters<typeof flow.act>[1], opener?: HTMLElement | null) => {
        if (busy) { refuse(); return }
        void flow.act(a, action, opener)
      },
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
            <Link to={appHref(a)}>{d.name}</Link>
            <div><PublisherLine publisherName={a.publisherName} publisherId={a.publisherId} className="rp-app-card-sub">{[d.publisher, a.version].filter(Boolean).join(' · ')}</PublisherLine></div>
          </div>
        </Space>
      },
    },
    { title: t('appStore.col.status'), render: (_: unknown, a: AdminApp) => { const { operation, onOpenOperation } = describe(a); return <Space orientation="vertical" size={2} align="start"><InstalledState app={a} /><InstalledOperation operation={operation} onOpenOperation={onOpenOperation} /></Space> } },
    { title: t('appStore.col.access'), render: (_: unknown, a: AdminApp) => <ServiceStatus app={a} /> },
    { title: '', align: 'right', render: (_: unknown, a: AdminApp) => { const { app, name: _name, description: _desc, publisher: _pub, ...controls } = describe(a); return <InstalledControls app={app} {...controls} /> } },
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

    <PlatformLifecycleModal lifecycle={lifecycle} />
    <AppReorderModal open={reordering} apps={apps} onClose={() => setReordering(false)} onSaved={() => void reload({ quiet: true })} />
  </Space>
}
