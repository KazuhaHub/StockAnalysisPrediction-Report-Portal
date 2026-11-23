import { Route } from 'react-router'
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

/**
 * Every route of the apps console, under /manage. The console page carries its own tabs; the rest sit in a shell
 * that shows the same tab bar above them. `apps/install` comes before `apps/:key` so "install" is never read as an
 * app key. An installation's page is two tabs (platform settings, and the app's own administration page when it has
 * one), selected by the URL.
 */
export const appsRoutes = (
  <>
    <Route path="apps" element={<AppsAdminPage />} />
    <Route element={<AppsSubpageShell />}>
      <Route path="apps/install" element={<AppsInstallPage />} />
      <Route path="apps/:key" element={<AppDetailPage />} />
      <Route path="apps/installations/:installation" element={<AppInstallationTabs />}>
        <Route index element={<AppPlatformSettingsPage />} />
        <Route path="admin/*" element={<AppAdminTab />} />
      </Route>
      <Route path="apps/:key/access" element={<AppAccessPage />} />
      <Route path="apps/:key/credentials" element={<AppCredentialsPage />} />
    </Route>
  </>
)
