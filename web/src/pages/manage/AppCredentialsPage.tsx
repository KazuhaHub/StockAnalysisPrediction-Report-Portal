import { useEffect, useMemo, useState } from 'react'
import {
  Alert,
  App,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import { KeyOutlined, PlusOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import { useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import LoadGate from '../../components/LoadGate'
import { useAppsCatalog } from './appsAdminModel'
import { AppsBreadcrumb } from './appsAdminParts'

interface Credential {
  id: string
  label: string
  state: 'active' | 'revoked' | 'review_required'
  value_revision: number
  revision: number
  created_ms: number
  updated_ms: number
}

interface Binding {
  id: string
  name: string
  credential_id: string
  credential_label: string
  credential_value_revision: number
  operation: 'http_bearer' | 'http_api_key'
  state: 'active' | 'revoked' | 'review_required'
  policy_revision: number
  revision: number
}

interface CredentialReview {
  direct: boolean
  affected: { id: string; installation_id: string; revision: number; state: string }[]
  plan_id?: string
  plan_sha256?: string
}

interface BindingPlan {
  installation_id: string
  plan_id: string
  plan_sha256: string
  review: {
    name: string
    credential_id: string
    credential_label: string
    operation: string
    network_policy_sha256: string
  }
}

const cancelled = new Error('cancelled')

function encodeValue(value: string): string {
  const bytes = new TextEncoder().encode(value)
  let binary = ''
  for (let i = 0; i < bytes.length; i += 8192) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 8192))
  }
  return btoa(binary)
}

export default function AppCredentialsPage() {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
  const appKey = useParams().key ?? ''
  const { apps, loading: appsLoading, error: appsError, reload: reloadApps } = useAppsCatalog()
  const app = apps.find((value) => value.key === appKey)
  const [credentials, setCredentials] = useState<Credential[]>([])
  const [bindings, setBindings] = useState<Binding[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [credentialOpen, setCredentialOpen] = useState(false)
  const [credentialTarget, setCredentialTarget] = useState<Credential | null>(null)
  const [bindingOpen, setBindingOpen] = useState(false)
  const [bindingSeed, setBindingSeed] = useState<Binding | null>(null)
  const [credentialForm] = Form.useForm<{ label: string; value: string }>()
  const [bindingForm] = Form.useForm<{ name: string; credential_id: string; operation: string }>()

  const load = async () => {
    if (!appKey) return
    setLoading(true)
    try {
      let after = ''
      const all: Credential[] = []
      for (;;) {
        const query = new URLSearchParams({ limit: '100' })
        if (after) query.set('after', after)
        const page = await api.get<{ credentials: Credential[]; next_after: string }>(
          `/api/admin/app-platform/credentials?${query}`,
        )
        all.push(...(page.credentials ?? []))
        if (!page.next_after || page.next_after === after || all.length >= 256) break
        after = page.next_after
      }
      const current = await api.get<{ bindings: Binding[] }>(
        `/api/admin/app-platform/apps/${encodeURIComponent(appKey)}/secret-bindings`,
      )
      setCredentials(all)
      setBindings(current.bindings ?? [])
      setError('')
    } catch (cause) {
      setError(errText(cause, t))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // appKey is the repository identity for this page.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appKey])

  const credentialLabel = useMemo(
    () => new Map(credentials.map((value) => [value.id, value.label])),
    [credentials],
  )

  const finishCredential = async (values: { label?: string; value: string }) => {
    try {
      if (!credentialTarget) {
        await api.post('/api/admin/app-platform/credentials', {
          label: values.label ?? '',
          value_base64: encodeValue(values.value),
        })
      } else {
        const review = await api.post<CredentialReview>(
          `/api/admin/app-platform/credentials/${credentialTarget.id}/preview`,
          {
            action: 'replace',
            revision: String(credentialTarget.revision),
            value_revision: String(credentialTarget.value_revision),
          },
        )
        const commit = () =>
          api.post(`/api/admin/app-platform/credentials/${credentialTarget.id}/commit`, {
            action: 'replace',
            revision: String(credentialTarget.revision),
            value_revision: String(credentialTarget.value_revision),
            value_base64: encodeValue(values.value),
            plan_id: review.plan_id ?? '',
            plan_sha256: review.plan_sha256 ?? '',
          })
        if (review.direct) {
          await commit()
        } else {
          await new Promise<void>((resolve, reject) => {
            modal.confirm({
              title: t('appCredentials.rotationConfirm'),
              content: t('appCredentials.affected', { count: review.affected.length }),
              okText: t('appCredentials.rotate'),
              cancelText: t('common.cancel'),
              onOk: () => commit().then(resolve, reject),
              onCancel: () => reject(cancelled),
            })
          })
        }
      }
      message.success(t('appCredentials.saved'))
      setCredentialOpen(false)
      setCredentialTarget(null)
      credentialForm.resetFields()
      await load()
    } catch (cause) {
      if (cause !== cancelled) message.error(errText(cause, t))
    }
  }

  const revokeCredential = (credential: Credential) => {
    modal.confirm({
      title: t('appCredentials.revokeCredential'),
      content: credential.label,
      okText: t('appCredentials.revokeCredential'),
      okButtonProps: { danger: true },
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          const review = await api.post<CredentialReview>(
            `/api/admin/app-platform/credentials/${credential.id}/preview`,
            {
              action: 'revoke',
              revision: String(credential.revision),
              value_revision: String(credential.value_revision),
            },
          )
          if (!review.direct && review.affected.length) {
            await new Promise<void>((resolve, reject) => {
              modal.confirm({
                title: t('appCredentials.credentialRevokeConfirm'),
                content: t('appCredentials.affected', { count: review.affected.length }),
                okText: t('appCredentials.revokeCredential'),
                okButtonProps: { danger: true },
                cancelText: t('common.cancel'),
                onOk: () => resolve(),
                onCancel: () => reject(cancelled),
              })
            })
          }
          await api.post(`/api/admin/app-platform/credentials/${credential.id}/commit`, {
            action: 'revoke',
            revision: String(credential.revision),
            value_revision: String(credential.value_revision),
            value_base64: '',
            plan_id: review.plan_id ?? '',
            plan_sha256: review.plan_sha256 ?? '',
          })
          message.success(t('appCredentials.saved'))
          await load()
        } catch (cause) {
          if (cause !== cancelled) message.error(errText(cause, t))
        }
      },
    })
  }

  const finishBinding = async (values: { name: string; credential_id: string; operation: string }) => {
    try {
      const plan = await api.post<BindingPlan>(
        `/api/admin/app-platform/apps/${encodeURIComponent(appKey)}/secret-bindings/plan`,
        values,
      )
      await new Promise<void>((resolve, reject) => {
        modal.confirm({
          title: t('appCredentials.confirmBinding'),
          content: (
            <Space orientation="vertical" size={4}>
              <span>{t('appCredentials.confirmBindingBody')}</span>
              <Typography.Text code>{plan.review.credential_label}</Typography.Text>
              <Typography.Text type="secondary">
                {plan.review.operation} · {plan.review.network_policy_sha256.slice(0, 12)}
              </Typography.Text>
            </Space>
          ),
          okText: t('appCredentials.approve'),
          cancelText: t('common.cancel'),
          onOk: async () => {
            try {
              await api.post(`/api/admin/app-platform/secret-binding-reviews/${plan.plan_id}`, {
                plan_id: plan.plan_id,
                plan_sha256: plan.plan_sha256,
              })
              resolve()
            } catch (cause) {
              reject(cause)
            }
          },
          onCancel: () => reject(cancelled),
        })
      })
      message.success(t('appCredentials.saved'))
      setBindingOpen(false)
      setBindingSeed(null)
      bindingForm.resetFields()
      await load()
    } catch (cause) {
      if (cause !== cancelled) message.error(errText(cause, t))
    }
  }

  const revokeBinding = async (binding: Binding) => {
    try {
      await api.post(`/api/admin/app-platform/secret-bindings/${binding.id}/revoke`, {
        revision: String(binding.revision),
      })
      message.success(t('appCredentials.saved'))
      await load()
    } catch (cause) {
      message.error(errText(cause, t))
    }
  }

  const openBinding = (binding?: Binding) => {
    setBindingSeed(binding ?? null)
    bindingForm.setFieldsValue(
      binding
        ? {
            name: binding.name,
            credential_id: binding.credential_id,
            operation: binding.operation,
          }
        : { name: '', credential_id: '', operation: 'http_bearer' },
    )
    setBindingOpen(true)
  }

  const credentialColumns: ColumnsType<Credential> = [
    { title: t('appCredentials.label'), dataIndex: 'label' },
    {
      title: t('appCredentials.state'),
      dataIndex: 'state',
      render: (state: string) => <Tag>{t(`appCredentials.state.${state}`)}</Tag>,
    },
    { title: t('appCredentials.revision'), dataIndex: 'revision', width: 96 },
    {
      title: t('appPlatform.action'),
      key: 'actions',
      width: 220,
      render: (_: unknown, credential: Credential) =>
        credential.state === 'active' ? (
          <Space>
            <Button
              size="small"
              icon={<KeyOutlined aria-hidden="true" />}
              onClick={() => {
                setCredentialTarget(credential)
                credentialForm.resetFields()
                setCredentialOpen(true)
              }}
            >
              {t('appCredentials.rotate')}
            </Button>
            <Button size="small" danger onClick={() => revokeCredential(credential)}>
              {t('appCredentials.revokeCredential')}
            </Button>
          </Space>
        ) : null,
    },
  ]

  const bindingColumns: ColumnsType<Binding> = [
    { title: t('appCredentials.name'), dataIndex: 'name' },
    {
      title: t('appCredentials.credential'),
      render: (_: unknown, binding: Binding) =>
        binding.credential_label || credentialLabel.get(binding.credential_id) || binding.credential_id,
    },
    {
      title: t('appCredentials.operation'),
      dataIndex: 'operation',
      render: (operation: string) => t(`appCredentials.operation.${operation}`),
    },
    {
      title: t('appCredentials.state'),
      dataIndex: 'state',
      render: (state: string) => <Tag>{t(`appCredentials.state.${state}`)}</Tag>,
    },
    {
      title: t('appPlatform.action'),
      key: 'actions',
      width: 180,
      render: (_: unknown, binding: Binding) =>
        binding.state === 'revoked' ? null : binding.state === 'review_required' ? (
          <Button size="small" icon={<SafetyCertificateOutlined aria-hidden="true" />} onClick={() => openBinding(binding)}>
            {t('appCredentials.reapprove')}
          </Button>
        ) : (
          <Popconfirm
            title={t('appCredentials.revokeBindingConfirm')}
            okText={t('appCredentials.revoke')}
            cancelText={t('common.cancel')}
            onConfirm={() => revokeBinding(binding)}
          >
            <Button size="small" danger>
              {t('appCredentials.revoke')}
            </Button>
          </Popconfirm>
        ),
    },
  ]

  return (
    <LoadGate loading={appsLoading || loading} error={appsError || error} onRetry={async () => {
      await reloadApps()
      await load()
    }}>
      <AppsBreadcrumb app={app} name={app?.name ?? appKey} page={t('appCredentials.title')} />
      <Space orientation="vertical" size={16} style={{ width: '100%' }}>
        <Alert type="info" showIcon title={t('appCredentials.hint')} />
        <Card
          title={t('appCredentials.credentials')}
          extra={
            <Button
              type="primary"
              icon={<PlusOutlined aria-hidden="true" />}
              onClick={() => {
                setCredentialTarget(null)
                credentialForm.resetFields()
                setCredentialOpen(true)
              }}
            >
              {t('appCredentials.createCredential')}
            </Button>
          }
        >
          <Table rowKey="id" size="small" dataSource={credentials} columns={credentialColumns} pagination={false} scroll={{ x: 'max-content' }} />
        </Card>
        <Card
          title={t('appCredentials.bindings')}
          extra={
            <Button icon={<PlusOutlined aria-hidden="true" />} onClick={() => openBinding()} disabled={!credentials.some((value) => value.state === 'active')}>
              {t('appCredentials.addBinding')}
            </Button>
          }
        >
          <Table rowKey="id" size="small" dataSource={bindings} columns={bindingColumns} pagination={false} scroll={{ x: 'max-content' }} />
        </Card>
      </Space>
      <Modal
        open={credentialOpen}
        title={credentialTarget ? t('appCredentials.rotate') : t('appCredentials.createCredential')}
        footer={null}
        onCancel={() => {
          setCredentialOpen(false)
          setCredentialTarget(null)
        }}
        destroyOnHidden
      >
        <Form form={credentialForm} layout="vertical" onFinish={finishCredential}>
          {!credentialTarget && (
            <Form.Item name="label" label={t('appCredentials.label')} rules={[{ required: true }]}>
              <Input maxLength={160} autoComplete="off" />
            </Form.Item>
          )}
          <Form.Item name="value" label={t('appCredentials.value')} extra={t('appCredentials.valueHint')} rules={[{ required: true, min: 1, max: 16384 }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit">
            {t('common.save')}
          </Button>
        </Form>
      </Modal>
      <Modal
        open={bindingOpen}
        title={bindingSeed ? t('appCredentials.reapprove') : t('appCredentials.addBinding')}
        footer={null}
        onCancel={() => {
          setBindingOpen(false)
          setBindingSeed(null)
        }}
        destroyOnHidden
      >
        <Form form={bindingForm} layout="vertical" onFinish={finishBinding}>
          <Form.Item name="name" label={t('appCredentials.name')} rules={[{ required: true, pattern: /^[a-z][a-z0-9-]{0,63}$/ }]}>
            <Input disabled={!!bindingSeed} autoComplete="off" />
          </Form.Item>
          <Form.Item name="credential_id" label={t('appCredentials.credential')} rules={[{ required: true }]}>
            <Select
              disabled={!!bindingSeed}
              options={credentials.filter((value) => value.state === 'active').map((value) => ({ value: value.id, label: value.label }))}
            />
          </Form.Item>
          <Form.Item name="operation" label={t('appCredentials.operation')} rules={[{ required: true }]}>
            <Select
              disabled={!!bindingSeed}
              options={[
                { value: 'http_bearer', label: t('appCredentials.operation.http_bearer') },
                { value: 'http_api_key', label: t('appCredentials.operation.http_api_key') },
              ]}
            />
          </Form.Item>
          <Button type="primary" htmlType="submit">
            {t('appCredentials.reviewBinding')}
          </Button>
        </Form>
      </Modal>
    </LoadGate>
  )
}
