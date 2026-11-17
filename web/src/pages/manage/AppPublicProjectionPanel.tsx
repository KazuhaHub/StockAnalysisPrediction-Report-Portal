import { useState } from 'react'
import { Alert, Button, Checkbox, InputNumber, Modal, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'

// The administrator's side of the anonymous public projection (doc 179 section 4). The disclosure
// is shown before anything is enabled, the choice of routes and limits narrows what the release
// declared, and withdrawal is one click in any state because it only removes exposure.

type PublicRoute = { id: string; path: string; query?: string[] | null; cache_seconds: number; response_bytes: number }
type Preview = {
  policy_revision: string
  granted: boolean
  ready: boolean
  declared: { routes: PublicRoute[]; document: string }
  policy: { routes: string[]; document: boolean; client_per_minute: number; backend_per_second: number }
  url: string
  disclosure: string
  disclosure_sha256: string
}

export default function AppPublicProjectionPanel({ root, installation, granted, ready, onChanged }: {
  root: string
  installation: string
  granted: boolean
  ready: boolean
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const route = `${root}/installations/${installation}/public-projection`
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [preview, setPreview] = useState<Preview | null>(null)
  const [routes, setRoutes] = useState<string[]>([])
  const [document, setDocument] = useState(false)
  const [client, setClient] = useState(60)
  const [backend, setBackend] = useState(5)

  const show = async () => {
    if (busy) return
    setBusy(true); setError('')
    try {
      const value = await api.get<Preview>(route)
      setPreview(value)
      setRoutes(value.policy.routes)
      setDocument(value.policy.document)
      setClient(value.policy.client_per_minute)
      setBackend(value.policy.backend_per_second)
      setOpen(true)
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const send = async (body: Record<string, unknown>) => {
    if (busy) return
    setBusy(true); setError('')
    try {
      await api.post(route, body)
      setOpen(false)
      onChanged()
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const enable = () => {
    if (!preview) return
    // Routes go back in the descriptor's order: the server accepts only the sorted list.
    const chosen = preview.declared.routes.map((item) => item.id).filter((id) => routes.includes(id))
    void send({ enabled: true, policyRevision: preview.policy_revision, routes: chosen, document: !!preview.declared.document && document, clientPerMinute: client, backendPerSecond: backend, disclosureSha256: preview.disclosure_sha256 })
  }
  const withdraw = async () => {
    if (busy) return
    setBusy(true); setError('')
    try {
      // The revision comes from a fresh read, so a withdrawal is never refused for a stale page.
      const value = await api.get<Preview>(route)
      await api.post(route, { enabled: false, policyRevision: value.policy_revision, routes: null, document: null, clientPerMinute: null, backendPerSecond: null, disclosureSha256: null })
      onChanged()
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const url = preview ? `${window.location.origin}${preview.url}` : ''

  return <Space orientation="vertical" size={8} style={{ width: '100%', marginTop: 12 }}>
    <Typography.Text type="secondary">{t('appPlatform.capability.public.projection')}</Typography.Text>
    {error && !open && <Alert type="error" showIcon title={error} />}
    <Space wrap>
      <Button loading={busy && !open} disabled={!ready} onClick={() => void show()}>{granted ? t('appPlatform.settings.public.edit') : t('appPlatform.settings.public.enable')}</Button>
      {granted && <Button loading={busy && !open} onClick={() => void withdraw()}>{t('appPlatform.settings.public.withdraw')}</Button>}
    </Space>
    {!ready && <Typography.Text type="secondary">{t('appPlatform.settings.public.notReady')}</Typography.Text>}
    <Modal
      className="rp-platform-review-modal"
      open={open}
      title={t('appPlatform.settings.public.title')}
      okText={t('appPlatform.settings.public.confirm')}
      cancelText={t('common.cancel')}
      okButtonProps={{ disabled: routes.length === 0 }}
      confirmLoading={busy}
      closable={!busy}
      mask={{ closable: false }}
      onOk={enable}
      onCancel={() => { if (!busy) { setOpen(false); setError('') } }}
    >
      {preview && <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Alert type="warning" showIcon title={t('appPlatform.capability.public.projection')} />
        <Typography.Text>{t('appPlatform.settings.public.address')}</Typography.Text>
        <Typography.Text code copyable>{url}</Typography.Text>
        <Checkbox.Group value={routes} onChange={(value) => setRoutes(value as string[])}>
          <Space orientation="vertical">
            {preview.declared.routes.map((item) => <Checkbox key={item.id} value={item.id}>
              {t('appPlatform.settings.public.route', { id: item.id, path: item.path, seconds: item.cache_seconds })}
            </Checkbox>)}
          </Space>
        </Checkbox.Group>
        {preview.declared.document && <Checkbox checked={document} onChange={(event) => setDocument(event.target.checked)}>
          {t('appPlatform.settings.public.document', { path: preview.declared.document })}
        </Checkbox>}
        <Space wrap>
          <label>{t('appPlatform.settings.public.clientLimit')} <InputNumber min={1} max={600} value={client} onChange={(value) => setClient(Number(value ?? 60))} /></label>
          <label>{t('appPlatform.settings.public.backendLimit')} <InputNumber min={1} max={20} value={backend} onChange={(value) => setBackend(Number(value ?? 5))} /></label>
        </Space>
        <Typography.Text type="secondary">{t('appPlatform.settings.public.hint')}</Typography.Text>
        {error && <Alert type="error" showIcon title={error} />}
      </Space>}
    </Modal>
  </Space>
}
