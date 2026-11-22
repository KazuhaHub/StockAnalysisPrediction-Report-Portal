import { useState } from 'react'
import { useSearchParams } from 'react-router'
import { Space } from 'antd'
import LoadGate from '../../components/LoadGate'
import { useAppsCatalog } from './appsAdminModel'
import AppsConsoleTabs, { isAppsTab, type AppsTab } from './AppsConsoleTabs'
import AppsMarketplace from './AppsMarketplace'
import OfficialCatalogueSwitch from './OfficialCatalogueSwitch'
import AppPlatformStorageStatus from './AppPlatformStorageStatus'
import AppPlatformPublishersPanel from './AppPlatformPublishersPanel'
import AppPlatformRuntimePanel from './AppPlatformRuntimePanel'
import AppPlatformTasksPanel from './AppPlatformTasksPanel'
import AppPlatformEventSubscriptionsPanel from './AppPlatformEventSubscriptionsPanel'

// The Apps console: one marketplace page (what is installed, and what the catalogue offers), the sources
// and trust behind it, and the tasks and event subscriptions of installed apps. Installing, updating and
// removing all run in one modal on the marketplace page (doc 190, section 4). `?operation=` and
// `?installation=` links from an app's own page land here, on the marketplace tab, where the modal
// resumes the operation.
export default function AppsAdminPage() {
  const { apps, loading, error, reload } = useAppsCatalog()
  const [runtimeReload, setRuntimeReload] = useState(0)
  // `?tab=<key>` opens that tab, the way another page asks to be sent to Sources and trust or the tasks.
  const [params] = useSearchParams()
  const [activeTab, setActiveTab] = useState<AppsTab>(() => { const tab = params.get('tab'); return isAppsTab(tab) ? tab : 'apps' })

  return <AppsConsoleTabs active={activeTab} onChange={setActiveTab} panes={{
    apps: <LoadGate loading={loading} error={error} onRetry={reload}>
      <AppsMarketplace apps={apps} reload={reload} active={activeTab === 'apps'} onOpenTrust={() => setActiveTab('publishers')} />
    </LoadGate>,
    publishers: <Space orientation="vertical" size={16} style={{ width: '100%' }}><OfficialCatalogueSwitch /><AppPlatformStorageStatus /><AppPlatformRuntimePanel reload={runtimeReload} /><AppPlatformPublishersPanel onInitialized={() => setRuntimeReload(v => v + 1)} /></Space>,
    tasks: <AppPlatformTasksPanel />,
    subscriptions: <AppPlatformEventSubscriptionsPanel />,
  }} />
}
