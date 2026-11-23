import { useCallback, useEffect, useMemo, useState } from 'react'
import { Tabs } from 'antd'
import { Link, Navigate, Outlet, useLocation, useNavigate, useParams } from 'react-router'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { LoadingPlaceholder } from '../../components/LoadGate'
import type { Settings } from './AppPlatformSettingsPage'
import { AppsBreadcrumb } from './appsAdminParts'
import { appDisplayText } from '../../lib/appDisplayV2'
import { usePrefs } from '../../prefs'

type Answer = { installation: string; settings: Settings | null }

/**
 * What the tabs hand to the page below them through the outlet: the settings they already loaded, a reload, and
 * whether the tabs already show the breadcrumb and Back link (then the page leaves them out).
 */
export type InstallationSettingsContext = {
  settings: Settings | null
  reload: () => void
  /** Swaps in settings the page already read, without a request or a loading state. */
  replace: (next: Settings) => void
  headed: boolean
}

/**
 * The tabs of one installation in the console: Platform settings, and, when the app declares an administration page,
 * App management. The URL selects the tab (`…/installations/:id` and `…/installations/:id/admin`), so back and
 * forward move between them. An app without an admin page has one page and no tab bar.
 *
 * The page waits for the one read that says whether the second tab exists, so the bar is whole from the first paint
 * rather than growing a tab later. That read is the installation's settings, and the settings page below uses it
 * instead of asking again (outlet context), so one request serves both; the page asks for a reload through the
 * same context and the tab bar stays mounted meanwhile. A failed read shows the page without tabs and without
 * settings, so the page loads for itself and reports its own failure with a retry.
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
  const reload = useCallback(() => setAttempt(value => value + 1), [])
  useEffect(() => {
    const abort = new AbortController()
    api.get<Settings>(`/api/admin/app-platform/installations/${installation}/settings`, { signal: abort.signal })
      .then(value => { if (!abort.signal.aborted) setAnswer({ installation, settings: value ?? null }) })
      .catch(() => { if (!abort.signal.aborted) setAnswer({ installation, settings: null }) })
    return () => abort.abort()
  }, [installation, attempt])
  const replace = useCallback((next: Settings) => setAnswer({ installation, settings: next }), [installation])
  const settings = answer?.settings ?? null
  // With a tab bar, the breadcrumb and Back lead the page above it, for both tabs.
  const headed = settings?.admin_page === true
  const shared = useMemo<InstallationSettingsContext>(() => ({ settings, reload, replace, headed }), [settings, reload, replace, headed])
  const onAdmin = location.pathname === adminBase || location.pathname.startsWith(adminBase + '/')
  if (answer?.installation !== installation) return <LoadingPlaceholder minHeight={320} />
  // A direct visit to the admin URL of an app that has no admin page lands on its settings.
  const adminPage = answer.settings?.admin_page === true
  if (!adminPage && onAdmin) return <Navigate to={base} replace />
  return <div className="rp-installation-tabs" style={onAdmin ? { display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 } : undefined}>
    {headed && settings && <>
      <AppsBreadcrumb name={appDisplayText(settings.app_id, settings.display, lang).name} />
      <Link to="/manage/apps" style={{ display: 'inline-block', marginBottom: 8 }}><ArrowLeftOutlined aria-hidden="true" /> {t('appPlatform.settings.back')}</Link>
    </>}
    {adminPage && <Tabs className="rp-apps-tabs" activeKey={onAdmin ? 'admin' : 'settings'}
      onTabClick={key => { void navigate(key === 'admin' ? adminBase : base) }}
      items={[{ key: 'settings', label: t('appPlatform.settings.tabSettings') }, { key: 'admin', label: t('appAdmin.tab') }]} />}
    <Outlet context={shared} />
  </div>
}
