import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Result, Space, Tabs, Tag, Typography } from 'antd'
import { Link, Navigate, Outlet, useLocation, useNavigate, useParams } from 'react-router'
import { ArrowLeftOutlined, AppstoreOutlined, ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errText } from '../../api/client'
import { LoadingPlaceholder } from '../../components/LoadGate'
import type { Settings } from './AppPlatformSettingsPage'
import { AppsBreadcrumb } from './appsAdminParts'
import { installationPath, INSTALLATION_TABS, type InstallationTab } from './appsAdminModel'
import { useAppsConsoleFlow, type AppsConsoleFlow } from './appsConsoleFlow'
import PlatformLifecycleModal from './PlatformLifecycleModal'
import InstallationHeaderActions, { installationApp, useInstallationRepair } from './InstallationHeaderActions'
import { AppDisplayIcon, appDisplayText } from '../../lib/appDisplayV2'
import { usePrefs } from '../../prefs'

type Answer = { installation: string; settings: Settings | null; error: string; missing: boolean }

/**
 * What the page frame hands to the tab below it through the outlet: the settings it already loaded, a reload, the
 * app's name, the lifecycle flow every action of the page runs in, and a counter that says the operations changed.
 */
export type InstallationSettingsContext = {
  settings: Settings | null
  reload: () => void
  /** Swaps in settings the page already read, without a request or a loading state. */
  replace: (next: Settings) => void
  headed: boolean
  name: string
  flow: AppsConsoleFlow
  /** Changes whenever an operation of this installation started or ended, so lists of operations read again. */
  operationsKey: number
  /** Follows a running operation in the page's lifecycle dialog. */
  openOperation: (id: string) => void
  /** Opens the repair review: the flow the header's Repair runs. */
  repair: () => void
}

/** The tabs this installation has. Data only for an app that has a backend or data of its own; the app's own page only when its release declares one. */
export function installationTabs(settings: Settings): InstallationTab[] {
  const hasData = settings.backend.required || !!settings.data || settings.recovery.count > 0 || settings.recovery.points.length > 0 || !!settings.migration_recovery || !!settings.image_delivery
  return INSTALLATION_TABS.filter(tab => tab === 'data' ? hasData : tab === 'admin' ? settings.admin_page === true : true)
}

const LABEL: Record<InstallationTab, string> = {
  overview: 'appPlatform.tab.overview', permissions: 'appPlatform.tab.permissions', access: 'appPlatform.tab.access', data: 'appPlatform.tab.data', admin: 'appAdmin.tab',
}

/**
 * One page per installation (`/manage/apps/installations/:id/<tab>`): Overview, Permissions & network, Access, Data
 * and, when the app declares one, its own administration page. The URL selects the tab, so every tab can be linked to
 * and back and forward move between them; the bare URL, an unknown tab and a tab this app does not have all land on
 * the Overview. The frame carries the one breadcrumb, the Back link, the app's identity and the lifecycle dialog that
 * every action on the page runs in.
 *
 * The page waits for the one read that says which tabs exist (the installation's settings), so the bar is whole from
 * the first paint. The tabs below use that same read (outlet context) instead of asking again. A failed read shows
 * the failure with a retry instead of a page without tabs.
 */
export default function AppInstallationTabs() {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const navigate = useNavigate()
  const location = useLocation()
  const installation = useParams().installation ?? ''
  const base = `/manage/apps/installations/${installation}`
  const adminBase = `${base}/admin`
  const [answer, setAnswer] = useState<Answer | null>(null)
  const [attempt, setAttempt] = useState(0)
  const [operationsKey, setOperationsKey] = useState(0)
  const reload = useCallback(() => setAttempt(value => value + 1), [])
  useEffect(() => {
    const abort = new AbortController()
    api.get<Settings>(`/api/admin/app-platform/installations/${installation}/settings`, { signal: abort.signal })
      .then(value => { if (!abort.signal.aborted) setAnswer({ installation, settings: value ?? null, error: '', missing: false }) })
      .catch(e => { if (!abort.signal.aborted) setAnswer({ installation, settings: null, error: errText(e, t), missing: e instanceof ApiError && e.status === 404 }) })
    return () => abort.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [installation, attempt])
  const replace = useCallback((next: Settings) => setAnswer({ installation, settings: next, error: '', missing: false }), [installation])
  // An operation starting or ending changes the installation: read it and its history again.
  const changed = useCallback(async () => { reload(); setOperationsKey(value => value + 1); return true }, [reload])
  const flow = useAppsConsoleFlow({ apps: [], reload: changed, active: false })
  const settings = answer?.settings ?? null
  const name = settings ? appDisplayText(settings.app_id, settings.display, lang).name : ''
  const openOperation = useCallback((id: string) => flow.lifecycle.showOperation(id), [flow.lifecycle])
  const app = useMemo(() => installationApp(settings, name), [settings, name])
  const repair = useInstallationRepair(app, flow)
  const shared = useMemo<InstallationSettingsContext>(
    () => ({ settings, reload, replace, headed: true, name, flow, operationsKey, openOperation, repair }),
    // `flow` is a new object every render; what the tabs read from it is read at the time they act.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [settings, reload, replace, name, flow.lifecycle, flow.busyReason, flow.acting, operationsKey, openOperation, repair],
  )
  if (answer?.installation !== installation) return <LoadingPlaceholder minHeight={320} />
  if (!settings) {
    return <Result status={answer.missing ? '404' : 'error'} title={t(answer.missing ? 'appPlatform.settings.notFound' : 'common.loadFailed')} subTitle={answer.error || undefined}
      extra={<Space>
        {!answer.missing && <Button icon={<ReloadOutlined aria-hidden="true" />} onClick={reload}>{t('common.retry')}</Button>}
        <Link to="/manage/apps">{t('appPlatform.settings.back')}</Link>
      </Space>} />
  }
  const tabs = installationTabs(settings)
  const segment = location.pathname.slice(base.length).split('/').filter(Boolean)[0] ?? ''
  if (!(tabs as string[]).includes(segment)) return <Navigate to={installationPath(installation)} replace />
  const onAdmin = segment === 'admin'
  return <div className="rp-installation-tabs" style={onAdmin ? { display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 } : undefined}>
    <AppsBreadcrumb name={name} />
    <Link to="/manage/apps" style={{ display: 'inline-block', marginBottom: 8 }}><ArrowLeftOutlined aria-hidden="true" /> {t('appPlatform.settings.back')}</Link>
    <div className="rp-installation-header">
      <div className="rp-installation-identity">
        <span className="rp-installation-glyph">{settings.display ? <AppDisplayIcon display={settings.display} /> : <AppstoreOutlined aria-hidden="true" />}</span>
        <div className="rp-installation-identity-text">
          <Typography.Title level={4} style={{ margin: 0 }}>{name}</Typography.Title>
          <Space size={8} wrap className="rp-installation-meta">
            {settings.version && <Tag style={{ margin: 0 }}>{settings.version}</Tag>}
            <Typography.Text type="secondary">{settings.publisher_id} · {settings.app_id}</Typography.Text>
          </Space>
        </div>
      </div>
      {app && <InstallationHeaderActions settings={settings} app={app} name={name} flow={flow} />}
    </div>
    <Tabs className="rp-apps-tabs" activeKey={segment}
      onTabClick={key => { void navigate(key === 'admin' ? adminBase : installationPath(installation, key as InstallationTab)) }}
      items={tabs.map(tab => ({ key: tab, label: t(LABEL[tab]) }))} />
    <Outlet context={shared} />
    <PlatformLifecycleModal lifecycle={flow.lifecycle} />
  </div>
}
