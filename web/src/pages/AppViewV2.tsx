import { Button } from 'antd'
import { SettingOutlined } from '@ant-design/icons'
import { useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useIsAdmin } from '../auth'
import { manageHref, useManageable } from '../lib/appDirectoryV2'
import AppSurfaceView from '../components/apps/AppSurfaceView'

// The reader's page of an installed app. The document, launch and chrome are AppSurfaceView's; this adds the way
// to the administration page for a viewer the directory marks as an administrator of the app.
export default function AppViewV2() {
  const { installation = '' } = useParams()
  const navigate = useNavigate()
  const { t } = useTranslation()
  const portalAdmin = useIsAdmin()
  const manageable = useManageable(installation)
  return <AppSurfaceView installation={installation} surface="reader" base={`/apps/v2/${installation}`}
    onBack={() => { void navigate('/apps') }}
    headerExtra={manageable ? <Button icon={<SettingOutlined aria-hidden="true" />} onClick={() => { void navigate(manageHref(installation, portalAdmin)) }}>{t('appAdmin.manage')}</Button> : null} />
}
