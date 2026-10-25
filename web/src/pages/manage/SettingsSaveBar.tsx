import { Button, Typography } from 'antd'
import { SaveOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import StickyActionBar from '../../components/StickyActionBar'
import { usePendingSettings } from './AdminDraftBoundary'

export default function SettingsSaveBar({ dirty, saving, onSave, onDiscard, scope }: {
  dirty: boolean
  saving: boolean
  onSave: () => void
  onDiscard: () => void
  scope?: string
}) {
  const { t } = useTranslation()
  usePendingSettings(dirty, saving)
  return (
    <StickyActionBar>
      <div className="rp-settings-save-bar">
        <Typography.Text type={dirty ? 'warning' : 'secondary'} role="status">
          {scope && `${scope} · `}
          {t(dirty ? 'manage.unsavedChanges' : 'manage.noChanges')}
        </Typography.Text>
        <div>
          <Button onClick={onDiscard} disabled={!dirty || saving}>{t('manage.discard')}</Button>
          <Button type="primary" icon={<SaveOutlined />} loading={saving} disabled={!dirty} onClick={onSave}>
            {t('common.save')}
          </Button>
        </div>
      </div>
    </StickyActionBar>
  )
}
