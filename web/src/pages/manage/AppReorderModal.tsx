import { useEffect, useState } from 'react'
import { App, Modal, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { AppLayoutResp } from '../../api/types'
import { useAppLabels, type AdminApp } from './appsAdminModel'
import { AppIcon } from './appsAdminParts'
import { DragHandle, SortableItem, SortableWrapper } from './dnd'

// The one shared order of built-in and installed apps (ADR 0048). Dragging only rearranges a local copy:
// the order is written when Done is pressed, so Cancel really discards, and a mis-drag costs nothing.
export default function AppReorderModal({
  open,
  apps,
  onClose,
  onSaved,
}: {
  open: boolean
  apps: AdminApp[]
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const labels = useAppLabels()
  const [keys, setKeys] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  // Each opening starts from the saved order, never from an arrangement that was cancelled.
  useEffect(() => {
    if (open) setKeys(apps.map((a) => a.key))
  }, [open, apps])

  const byKey = new Map(apps.map((a) => [a.key, a]))
  const done = async () => {
    setSaving(true)
    try {
      await api.put<AppLayoutResp>('/api/admin/apps/order', { keys })
      onSaved()
      onClose()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('apps.order.title')}
      okText={t('common.done')}
      cancelText={t('common.cancel')}
      confirmLoading={saving}
      onOk={done}
      onCancel={onClose}
      destroyOnHidden
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Text type="secondary">{t('apps.order.hint')}</Typography.Text>
        <SortableWrapper ids={keys} onReorder={setKeys}>
          {keys.map((k) => {
            const a = byKey.get(k)
            if (!a) return null
            return (
              <SortableItem key={k} id={k}>
                <Space>
                  <DragHandle label={t('apps.order.drag', { name: labels.title(a) })} />
                  <AppIcon app={a} size={28} />
                  {labels.title(a)}
                </Space>
              </SortableItem>
            )
          })}
        </SortableWrapper>
      </Space>
    </Modal>
  )
}
