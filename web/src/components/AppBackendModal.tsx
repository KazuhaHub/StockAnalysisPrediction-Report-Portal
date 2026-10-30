import { useEffect, useState } from 'react'
import { Alert, App, Button, Form, Input, Modal, Popconfirm, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errText } from '../api/client'
import type { AppSummary } from '../api/types'
import WaitingForConnection from './WaitingForConnection'
import { useLoadFailure } from '../lib/useOnReconnect'

interface Connection {
  revision: number
  instance_id: string
  issuer: string
  label: string
  base_url: string
  audience: string
  enabled: boolean
  has_key: boolean
  policy: unknown
}
interface Fields { label: string; base_url: string; audience: string; enabled: boolean; key_id: string; signing_key: string; policy: string }
const initialPolicy = { version: 1, routes: [{ method: 'GET', prefix: '/v1/components' }], ports: [443], networks: [], plain_http: false }

export default function AppBackendModal({ app, onClose }: { app: AppSummary | null; onClose: () => void }) {
  const { t } = useTranslation()
  const { failure, retryKey } = useLoadFailure()
  const { message } = App.useApp()
  const [form] = Form.useForm<Fields>()
  const [connection, setConnection] = useState<Connection | null>(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const endpoint = app ? `/api/admin/apps/${app.id}/backend` : ''
  useEffect(() => {
    if (!app) return
    let active = true
    setConnection(null); setError(''); setLoading(true)
    form.resetFields()
    form.setFieldsValue({ label: app.name, audience: app.id, enabled: false, key_id: '', signing_key: '', policy: JSON.stringify(initialPolicy, null, 2) })
    api.get<Connection>(endpoint).then(c => {
      if (!active) return
      setConnection(c)
      setLoading(false)
      form.setFieldsValue({ ...c, key_id: '', signing_key: '', policy: JSON.stringify(c.policy, null, 2) })
    }).catch(e => {
      if (!active) return
      // A lost connection keeps the form loading and reloads it when the connection returns.
      const text = e instanceof ApiError && e.status === 404 ? '' : failure(e)
      if (text === null) return
      if (text) setError(text)
      setLoading(false)
    })
    return () => { active = false }
  }, [app, endpoint, form, t, retryKey])
  const save = async () => {
    const values = await form.validateFields()
    let policy: unknown
    try { policy = JSON.parse(values.policy) } catch { setError(t('apps.backendPolicyInvalid')); return }
    setSaving(true); setError('')
    try {
      const saved = await api.put<Connection>(endpoint, { ...values, policy, revision: connection?.revision ?? 0 })
      setConnection(saved); form.setFieldsValue({ key_id: '', signing_key: '' }); message.success(t('common.saved'))
    } catch (e) { setError(errText(e, t)) } finally { setSaving(false) }
  }
  const remove = async () => {
    if (!connection) return
    setSaving(true)
    try { await api.del(endpoint, { revision: connection.revision }); onClose() } catch (e) { setError(errText(e, t)) } finally { setSaving(false) }
  }
  return <Modal open={!!app} title={<>{t('apps.backendTitle', { name: app?.name })}{loading && <WaitingForConnection />}</>} onCancel={onClose} onOk={save} confirmLoading={saving} loading={loading} width={720} destroyOnHidden>
    <Space orientation="vertical" style={{ width: '100%' }}>
      <Alert type="info" showIcon title={t('apps.backendHint')} />
      {error && <Alert type="error" showIcon title={error} />}
      {connection && <Typography.Paragraph copyable>{JSON.stringify({ issuer: connection.issuer, app_id: app?.id, instance_id: connection.instance_id, audience: connection.audience }, null, 2)}</Typography.Paragraph>}
      {connection && !connection.has_key && <Alert type="warning" showIcon title={t('apps.backendPairAgain')} />}
    </Space>
    <Form form={form} layout="vertical" disabled={loading || saving} style={{ marginTop: 16 }}>
      <Form.Item name="label" label={t('common.name')} rules={[{ required: true, max: 120 }]}><Input /></Form.Item>
      <Form.Item name="base_url" label={t('apps.backendURL')} rules={[{ required: true }]}><Input placeholder="https://status.example.com" autoComplete="off" /></Form.Item>
      <Form.Item name="audience" label={t('apps.backendAudience')} rules={[{ required: true, max: 256 }]}><Input /></Form.Item>
      <Form.Item name="key_id" label={t('apps.backendKeyID')}><Input autoComplete="off" /></Form.Item>
      <Form.Item name="signing_key" label={t('apps.backendKey')} extra={t('apps.backendKeyHint')}><Input.Password autoComplete="new-password" /></Form.Item>
      <Form.Item name="policy" label={t('apps.backendPolicy')} rules={[{ required: true }]}><Input.TextArea rows={10} spellCheck={false} /></Form.Item>
      <Form.Item name="enabled" label={t('apps.backendEnabled')} valuePropName="checked"><Switch /></Form.Item>
    </Form>
    {connection && <Popconfirm title={t('apps.backendDeleteConfirm')} onConfirm={remove}><Button danger loading={saving}>{t('common.delete')}</Button></Popconfirm>}
  </Modal>
}
