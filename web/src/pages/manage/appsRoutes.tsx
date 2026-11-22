import { Route } from 'react-router'
import { lazyRetry } from '../../lib/lazyRetry'
import { AppsSubpageShell } from './AppsConsoleTabs'

const AppsAdminPage = lazyRetry(() => import('./AppsAdminPage'))
const AppsInstallPage = lazyRetry(() => import('./AppsInstallPage'))
const AppDetailPage = lazyRetry(() => import('./AppDetailPage'))
const AppAccessPage = lazyRetry(() => import('./AppAccessPage'))
const AppCredentialsPage = lazyRetry(() => import('./AppCredentialsPage'))
const AppPlatformSettingsPage = lazyRetry(() => import('./AppPlatformSettingsPage'))

/**
 * Every route of the apps console, under /manage. The console page carries its own tabs; the rest sit in a shell
 * that shows the same tab bar above them. `apps/install` comes before `apps/:key` so "install" is never read as an
 * app key.
 */
export const appsRoutes = (
  <>
    <Route path="apps" element={<AppsAdminPage />} />
    <Route element={<AppsSubpageShell />}>
      <Route path="apps/install" element={<AppsInstallPage />} />
      <Route path="apps/:key" element={<AppDetailPage />} />
      <Route path="apps/installations/:installation" element={<AppPlatformSettingsPage />} />
      <Route path="apps/:key/access" element={<AppAccessPage />} />
      <Route path="apps/:key/credentials" element={<AppCredentialsPage />} />
    </Route>
  </>
)
