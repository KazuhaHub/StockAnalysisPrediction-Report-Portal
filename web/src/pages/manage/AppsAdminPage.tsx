import { Tabs } from 'antd'
import { useTranslation } from 'react-i18next'
import AppPlatformInstallPanel from './AppPlatformInstallPanel'
import LegacyAppsAdminPage from './LegacyAppsAdminPage'

export default function AppsAdminPage() {
  const { t } = useTranslation()
  return <Tabs items={[
    { key: 'platform', label: t('appPlatform.tab'), children: <AppPlatformInstallPanel /> },
    { key: 'legacy', label: t('appPlatform.legacy'), children: <LegacyAppsAdminPage /> },
  ]} />
}
