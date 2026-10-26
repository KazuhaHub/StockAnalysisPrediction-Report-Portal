import { Tabs } from 'antd'
import { useTranslation } from 'react-i18next'
import AppPlatformInstallPanel from './AppPlatformInstallPanel'
import LegacyAppsAdminPage from './LegacyAppsAdminPage'
import AppPlatformPublishersPanel from './AppPlatformPublishersPanel'

export default function AppsAdminPage() {
  const { t } = useTranslation()
  return <Tabs items={[
    { key: 'platform', label: t('appPlatform.tab'), children: <AppPlatformInstallPanel /> },
    { key: 'publishers', label: t('appTrust.tab'), children: <AppPlatformPublishersPanel /> },
    { key: 'legacy', label: t('appPlatform.legacy'), children: <LegacyAppsAdminPage /> },
  ]} />
}
