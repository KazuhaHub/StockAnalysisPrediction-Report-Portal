import { Button, Typography } from 'antd'
import { SaveOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { usePendingSettings } from './AdminDraftBoundary'

// Renders nothing while there is nothing to save, so a clean page reserves no space. Once dirty it
// slides up as a bar pinned to the bottom of the page's scroll area (see .rp-settings-save-dock).
export default function SettingsSaveBar({ dirty, saving, onSave, onDiscard, scope }: {
  dirty: boolean
  saving: boolean
  onSave: () => void
  onDiscard: () => void
  scope?: string
}) {
  const { t } = useTranslation()
  usePendingSettings(dirty, saving)
  if (!dirty && !saving) return null
  return (
    <div className="rp-settings-save-dock">
      <div className="rp-settings-save-bar">
        <Typography.Text type="warning" role="status">
          {scope && `${scope} · `}
          {t('manage.unsavedChanges')}
        </Typography.Text>
        <div>
          <Button onClick={onDiscard} disabled={saving}>{t('manage.discard')}</Button>
          <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={onSave} aria-label={t('common.save')}>
            {t('common.save')}
          </Button>
        </div>
      </div>
    </div>
  )
}
