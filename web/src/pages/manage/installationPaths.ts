// Where an installation's page and its tabs live. Kept apart from the app model so the routes can name them without
// loading the model's translations and preferences.

/** The tabs of an installation's page, in order. `admin` is the app's own administration page and exists only when its release declares one. */
export const INSTALLATION_TABS = ['overview', 'permissions', 'access', 'data', 'admin'] as const
export type InstallationTab = (typeof INSTALLATION_TABS)[number]
/** One installation's page, or one tab of it: `/manage/apps/installations/<id>/<tab>`. */
export const installationPath = (installation: string, tab: InstallationTab = 'overview') => `/manage/apps/installations/${encodeURIComponent(installation)}/${tab}`
