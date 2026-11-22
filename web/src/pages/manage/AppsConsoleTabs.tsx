import type { ReactNode } from 'react'
import { Tabs } from 'antd'
import { Outlet, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'

/** The tabs of the apps console, in order. `apps` is the default page at /manage/apps. */
export const APPS_TABS = ['apps', 'publishers', 'tasks', 'subscriptions'] as const
export type AppsTab = (typeof APPS_TABS)[number]
export const isAppsTab = (value: string | null): value is AppsTab => APPS_TABS.some((tab) => tab === value)

const LABELS: Record<AppsTab, string> = {
  apps: 'nav.apps',
  publishers: 'appTrust.tab',
  tasks: 'appPlatform.tasks.tab',
  subscriptions: 'appPlatform.events.tab',
}

/** Where a tab lives: the list itself for Apps, `?tab=<key>` on it for the others. */
export const appsTabPath = (tab: AppsTab) => (tab === 'apps' ? '/manage/apps' : `/manage/apps?tab=${tab}`)

/**
 * The one tab bar of the apps console. The console page passes the panes and the choice; every page under it
 * (the install page, an app's page and its sub-pages) shows the bar with Apps active and no panes, and choosing a
 * tab there goes to that tab on the console page.
 */
export default function AppsConsoleTabs({ active, onChange, panes }: { active: AppsTab; onChange?: (tab: AppsTab) => void; panes?: Partial<Record<AppsTab, ReactNode>> }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  return <Tabs className="rp-apps-tabs" activeKey={active} onTabClick={(key) => { const tab = key as AppsTab; if (onChange) onChange(tab); else navigate(appsTabPath(tab)) }}
    items={APPS_TABS.map((key) => ({ key, label: t(LABELS[key]), children: panes?.[key] }))} />
}

/** The shell of every page under the console: the tab bar, then the page. */
export function AppsSubpageShell() {
  return <>
    <AppsConsoleTabs active="apps" />
    <Outlet />
  </>
}
