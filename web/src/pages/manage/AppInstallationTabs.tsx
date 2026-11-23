import { useEffect, useState } from 'react'
import { Tabs } from 'antd'
import { Navigate, Outlet, useLocation, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { LoadingPlaceholder } from '../../components/LoadGate'

type Answer = { installation: string; adminPage: boolean }

/**
 * The tabs of one installation in the console: Platform settings, and, when the app declares an administration page,
 * App management. The URL selects the tab (`…/installations/:id` and `…/installations/:id/admin`), so back and
 * forward move between them. An app without an admin page has one page and no tab bar.
 *
 * The page waits for the one small read that says whether the second tab exists, so the bar is whole from the first
 * paint rather than growing a tab later. A failed read shows the page without tabs; the page under it reports its
 * own load failure with a retry.
 */
export default function AppInstallationTabs() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const location = useLocation()
  const installation = useParams().installation ?? ''
  const base = `/manage/apps/installations/${installation}`
  const adminBase = `${base}/admin`
  const [answer, setAnswer] = useState<Answer | null>(null)
  useEffect(() => {
    const abort = new AbortController()
    api.get<{ admin_page?: boolean }>(`/api/admin/app-platform/installations/${installation}/settings`, { signal: abort.signal })
      .then(value => { if (!abort.signal.aborted) setAnswer({ installation, adminPage: value?.admin_page === true }) })
      .catch(() => { if (!abort.signal.aborted) setAnswer({ installation, adminPage: false }) })
    return () => abort.abort()
  }, [installation])
  const onAdmin = location.pathname === adminBase || location.pathname.startsWith(adminBase + '/')
  if (answer?.installation !== installation) return <LoadingPlaceholder minHeight={320} />
  // A direct visit to the admin URL of an app that has no admin page lands on its settings.
  if (!answer.adminPage && onAdmin) return <Navigate to={base} replace />
  return <div className="rp-installation-tabs" style={onAdmin ? { display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 } : undefined}>
    {answer.adminPage && <Tabs className="rp-apps-tabs" activeKey={onAdmin ? 'admin' : 'settings'}
      onTabClick={key => { void navigate(key === 'admin' ? adminBase : base) }}
      items={[{ key: 'settings', label: t('appPlatform.settings.tabSettings') }, { key: 'admin', label: t('appAdmin.tab') }]} />}
    <Outlet />
  </div>
}
