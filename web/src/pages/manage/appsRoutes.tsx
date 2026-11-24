import type { ReactNode } from 'react'
import { Navigate, Route, useParams } from 'react-router'
import { installationPath, type InstallationTab } from './installationPaths'
import { lazyRetry } from '../../lib/lazyRetry'
import { AppsSubpageShell } from './AppsConsoleTabs'
import AppInstallationTabs from './AppInstallationTabs'

const AppsAdminPage = lazyRetry(() => import('./AppsAdminPage'))
const AppsInstallPage = lazyRetry(() => import('./AppsInstallPage'))
const AppDetailPage = lazyRetry(() => import('./AppDetailPage'))
const AppAccessPage = lazyRetry(() => import('./AppAccessPage'))
const AppCredentialsPage = lazyRetry(() => import('./AppCredentialsPage'))
const AppPlatformSettingsPage = lazyRetry(() => import('./AppPlatformSettingsPage'))
const AppAdminTab = lazyRetry(() => import('./AppAdminTab'))
const InstallationOverview = lazyRetry(() => import('./InstallationOverview'))

/** A platform app's old page and access URLs (`/manage/apps/platform:<id>[/access]`) lead to its installation page. */
function PlatformRedirect({ tab, children }: { tab: InstallationTab; children: ReactNode }) {
  const key = useParams().key ?? ''
  return key.startsWith('platform:') ? <Navigate to={installationPath(key.slice('platform:'.length), tab)} replace /> : children
}

/**
 * Every route of the apps console, under /manage. The console page carries its own tabs; the rest sit in a shell
 * that shows the same tab bar above them. `apps/install` comes before `apps/:key` so "install" is never read as an
 * app key. An installation has one page of tabs (Overview, Permissions & network, Access, Data, and the app's own
 * administration page when it has one), selected by the URL; the bare URL and a platform app's old `platform:<id>`
 * URLs lead to its Overview or Access tab.
 */
export const appsRoutes = (
  <>
    <Route path="apps" element={<AppsAdminPage />} />
    <Route element={<AppsSubpageShell />}>
      <Route path="apps/install" element={<AppsInstallPage />} />
      <Route path="apps/:key" element={<PlatformRedirect tab="overview"><AppDetailPage /></PlatformRedirect>} />
      <Route path="apps/installations/:installation" element={<AppInstallationTabs />}>
        <Route path="overview" element={<InstallationOverview />} />
        <Route path="admin/*" element={<AppAdminTab />} />
        <Route path=":tab" element={<AppPlatformSettingsPage />} />
      </Route>
      <Route path="apps/:key/access" element={<PlatformRedirect tab="access"><AppAccessPage /></PlatformRedirect>} />
      <Route path="apps/:key/credentials" element={<AppCredentialsPage />} />
    </Route>
  </>
)
