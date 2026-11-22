import { useState } from 'react'
import { Space, Tabs } from 'antd'
import { useTranslation } from 'react-i18next'
import LoadGate from '../../components/LoadGate'
import { useAppsCatalog } from './appsAdminModel'
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
  const { t } = useTranslation()
  const { apps, loading, error, reload } = useAppsCatalog()
  const [runtimeReload, setRuntimeReload] = useState(0)
  const [activeTab, setActiveTab] = useState('apps')

  return <Tabs activeKey={activeTab} onChange={setActiveTab} items={[
    {
      key: 'apps',
      label: t('nav.apps'),
      children: <LoadGate loading={loading} error={error} onRetry={reload}>
        <AppsMarketplace apps={apps} reload={reload} active={activeTab === 'apps'} onOpenTrust={() => setActiveTab('publishers')} />
      </LoadGate>,
    },
    { key: 'publishers', label: t('appTrust.tab'), children: <Space orientation="vertical" size={16} style={{ width: '100%' }}><OfficialCatalogueSwitch /><AppPlatformStorageStatus /><AppPlatformRuntimePanel reload={runtimeReload} /><AppPlatformPublishersPanel onInitialized={() => setRuntimeReload(v => v + 1)} /></Space> },
    { key: 'tasks', label: t('appPlatform.tasks.tab'), children: <AppPlatformTasksPanel /> },
    { key: 'subscriptions', label: t('appPlatform.events.tab'), children: <AppPlatformEventSubscriptionsPanel /> },
  ]} />
}
