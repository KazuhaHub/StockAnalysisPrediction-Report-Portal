import { useEffect, useState } from 'react'
import { Alert, Space } from 'antd'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'

type Unreadable = { release_sha256: string; app_id: string; version: string; reason: string }

// The global image storage status: every pinned release whose stored image cannot be used, so the operator knows
// which package to upload again. It is a notice, never an error: a failed read shows nothing.
export default function AppPlatformStorageStatus() {
  const { t } = useTranslation()
  const [items, setItems] = useState<Unreadable[]>([])
  useEffect(() => {
    let live = true
    api.get<{ unreadable?: Unreadable[] }>('/api/admin/app-platform/storage-status')
      .then((r) => { if (live && Array.isArray(r?.unreadable)) setItems(r.unreadable) })
      .catch(() => undefined)
    return () => { live = false }
  }, [])
  if (items.length === 0) return null
  return (
    <Space orientation="vertical" size={8} style={{ width: '100%', marginBottom: 16 }} data-testid="image-storage-status">
      {items.map((item) => (
        <Alert key={item.release_sha256} type="warning" showIcon
          title={t('appPlatform.storage.reuploadRequired')}
          description={t('appPlatform.storage.reuploadDetail', { app: item.app_id, version: item.version, release: item.release_sha256.slice(0, 12) })} />
      ))}
    </Space>
  )
}
