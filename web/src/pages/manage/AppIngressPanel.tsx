import { useState } from 'react'
import { Alert, Button, Modal, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'

// The administrator's side of authenticated inbound ingress (doc 180 section 4). The routes and the
// address pattern are shown before anything is enabled, and withdrawal is one click in any state
// because it only removes exposure. Creating an endpoint, and its secret, is a binding review on the
// credentials page; this panel never handles a secret.

type IngressRoute = { id: string; path: string; body_bytes: number; per_endpoint_per_minute: number }
type Preview = {
  policy_revision: string
  granted: boolean
  ready: boolean
  routes: IngressRoute[]
  url_pattern: string
  disclosure: string
  disclosure_sha256: string
}

export default function AppIngressPanel({ root, installation, granted, ready, onChanged }: {
  root: string
  installation: string
  granted: boolean
  ready: boolean
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const route = `${root}/installations/${installation}/ingress`
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [preview, setPreview] = useState<Preview | null>(null)

  const show = async () => {
    if (busy) return
    setBusy(true); setError('')
    try {
      setPreview(await api.get<Preview>(route))
      setOpen(true)
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const enable = async () => {
    if (!preview || busy) return
    setBusy(true); setError('')
    try {
      await api.post(route, { enabled: true, policyRevision: preview.policy_revision, disclosureSha256: preview.disclosure_sha256 })
      setOpen(false)
      onChanged()
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const withdraw = async () => {
    if (busy) return
    setBusy(true); setError('')
    try {
      // The revision comes from a fresh read, so a withdrawal is never refused for a stale page.
      const value = await api.get<Preview>(route)
      await api.post(route, { enabled: false, policyRevision: value.policy_revision, disclosureSha256: null })
      onChanged()
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }

  return <Space orientation="vertical" size={8} style={{ width: '100%', marginTop: 12 }}>
    <Typography.Text type="secondary">{t('appPlatform.capability.ingress.webhook')}</Typography.Text>
    {error && !open && <Alert type="error" showIcon title={error} />}
    <Space wrap>
      {!granted && <Button loading={busy && !open} disabled={!ready} onClick={() => void show()}>{t('appPlatform.settings.ingress.enable')}</Button>}
      {granted && <Button loading={busy && !open} onClick={() => void withdraw()}>{t('appPlatform.settings.ingress.withdraw')}</Button>}
    </Space>
    {!granted && !ready && <Typography.Text type="secondary">{t('appPlatform.settings.ingress.notReady')}</Typography.Text>}
    <Modal
      className="rp-platform-review-modal"
      open={open}
      title={t('appPlatform.settings.ingress.title')}
      okText={t('appPlatform.settings.ingress.confirm')}
      cancelText={t('common.cancel')}
      confirmLoading={busy}
      closable={!busy}
      mask={{ closable: false }}
      onOk={() => void enable()}
      onCancel={() => { if (!busy) { setOpen(false); setError('') } }}
    >
      {preview && <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Alert type="warning" showIcon title={t('appPlatform.capability.ingress.webhook')} />
        <Typography.Text>{t('appPlatform.settings.ingress.pattern')}</Typography.Text>
        <Typography.Text code copyable>{`${window.location.origin}${preview.url_pattern}`}</Typography.Text>
        <Space orientation="vertical" size={4}>
          {preview.routes.map((item) => <Typography.Text key={item.id}>
            {t('appPlatform.settings.ingress.route', { id: item.id, path: item.path, bytes: item.body_bytes, rate: item.per_endpoint_per_minute })}
          </Typography.Text>)}
        </Space>
        <Typography.Text type="secondary">{t('appPlatform.settings.ingress.hint')}</Typography.Text>
        {error && <Alert type="error" showIcon title={error} />}
      </Space>}
    </Modal>
  </Space>
}
