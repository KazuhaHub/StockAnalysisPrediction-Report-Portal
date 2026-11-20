import { useEffect, useMemo, useState } from 'react'
import {
  Alert,
  App,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import { DeleteOutlined, KeyOutlined, PlusOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import { useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import Confirm, { confirmAction } from '../../components/Confirm'
import LoadGate from '../../components/LoadGate'
import { notifyError } from '../../lib/notify'
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
  operation: 'http_bearer' | 'http_api_key' | 'raw_read' | 'acs3_hmac_sha256' | 'pg_password' | 'ingress_hmac_sha256'
  state: 'active' | 'revoked' | 'review_required'
  policy_revision: number
  revision: number
  principals?: Principal[]
  acs3_actions?: Acs3Action[]
  // The route an ingress endpoint authenticates (doc 180) and what it has heard in the open window.
  ingress_route?: string
  ingress?: { window_start: string; accepted: number; refused: number }
  network_target?: string
  network_target_sha256?: string
}

interface TargetChoice {
  id: string
  origin: string
}

// One API action an Alibaba Cloud signed-request binding may sign (doc 177).
interface Acs3Action {
  action: string
  version: string
}

const isHttpOperation = (operation?: string) => operation === 'http_bearer' || operation === 'http_api_key'

const acs3Label = (item: Acs3Action) => `${item.action} · ${item.version}`

// The reviewed list is sorted by action then version and has no duplicate; the console sends it that way.
const acs3Sorted = (items: Acs3Action[]): Acs3Action[] =>
  [...items].sort((a, b) =>
    a.action === b.action ? (a.version < b.version ? -1 : a.version > b.version ? 1 : 0) : a.action < b.action ? -1 : 1,
  )

type Principal = 'viewer' | 'service'
// Who may use a binding, as the review form offers it.
type Use = 'viewer' | 'both' | 'service'

const principalsForUse = (use: Use): Principal[] =>
  use === 'service' ? ['service'] : use === 'both' ? ['service', 'viewer'] : ['viewer']

const principalsToUse = (principals?: Principal[]): Use =>
  principals?.includes('service') ? (principals.includes('viewer') ? 'both' : 'service') : 'viewer'

interface BindingForm {
  name: string
  credential_id: string
  operation: string
  use: Use
  acs3_actions?: Acs3Action[]
  ingress_route?: string
  target?: string
}

// What a credential is made of: one secret value, or an Alibaba Cloud AccessKey (doc 177).
type CredentialKind = 'secret' | 'acs3' | 'postgres'

interface CredentialFormValues {
  label?: string
  kind?: CredentialKind
  value?: string
  access_key_id?: string
  access_key_secret?: string
  security_token?: string
  pg_username?: string
  pg_password?: string
}

// The AccessKey credential value: canonical JSON of its parts, sent once like any other value.
const acs3CredentialValue = (values: CredentialFormValues): string =>
  JSON.stringify({
    access_key_id: values.access_key_id ?? '',
    access_key_secret: values.access_key_secret ?? '',
    security_token: values.security_token ?? '',
  })

// The PostgreSQL login credential value (doc 178): canonical JSON of username and password.
const postgresCredentialValue = (values: CredentialFormValues): string =>
  JSON.stringify({
    username: values.pg_username ?? '',
    password: values.pg_password ?? '',
  })

const credentialValue = (values: CredentialFormValues): string =>
  values.kind === 'acs3'
    ? acs3CredentialValue(values)
    : values.kind === 'postgres'
      ? postgresCredentialValue(values)
      : (values.value ?? '')

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
    network_origin?: string
    network_target?: string
    principals?: Principal[]
    acs3_actions?: Acs3Action[]
    ingress_route?: string
    ingress_url?: string
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
  const [targets, setTargets] = useState<TargetChoice[]>([])
  const [postgresTargets, setPostgresTargets] = useState<TargetChoice[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [credentialOpen, setCredentialOpen] = useState(false)
  const [credentialTarget, setCredentialTarget] = useState<Credential | null>(null)
  const [bindingOpen, setBindingOpen] = useState(false)
  const [bindingSeed, setBindingSeed] = useState<Binding | null>(null)
  const [credentialForm] = Form.useForm<CredentialFormValues>()
  const [bindingForm] = Form.useForm<BindingForm>()
  const bindingOperation = Form.useWatch('operation', bindingForm)
  const credentialKind = Form.useWatch('kind', credentialForm) ?? 'secret'

  // A PostgreSQL binding with one reviewed target needs no choice: the form picks it.
  useEffect(() => {
    if (bindingOperation === 'pg_password' && postgresTargets.length === 1 && !bindingForm.getFieldValue('target')) {
      bindingForm.setFieldValue('target', postgresTargets[0].id)
    }
  }, [bindingOperation, postgresTargets, bindingForm])

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
      const current = await api.get<{ bindings: Binding[]; targets?: TargetChoice[]; postgres_targets?: TargetChoice[] }>(
        `/api/admin/app-platform/apps/${encodeURIComponent(appKey)}/secret-bindings`,
      )
      setCredentials(all)
      setBindings(current.bindings ?? [])
      setTargets(current.targets ?? [])
      setPostgresTargets(current.postgres_targets ?? [])
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

  const finishCredential = async (values: CredentialFormValues) => {
    try {
      const value = credentialValue(values)
      if (!credentialTarget) {
        await api.post('/api/admin/app-platform/credentials', {
          label: values.label ?? '',
          value_base64: encodeValue(value),
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
            value_base64: encodeValue(value),
            plan_id: review.plan_id ?? '',
            plan_sha256: review.plan_sha256 ?? '',
          })
        if (review.direct) {
          await commit()
        } else {
          await new Promise<void>((resolve, reject) => {
            confirmAction(modal, {
              title: t('appCredentials.rotationConfirm'),
              description: t('appCredentials.affected', { count: review.affected.length }),
              okText: t('appCredentials.rotate'),
              danger: false,
              cancelText: t('common.cancel'),
              onOk: () => commit().then(resolve, reject),
              onCancel: () => reject(cancelled),
            }, t)
          })
        }
      }
      message.success(t('appCredentials.saved'))
      setCredentialOpen(false)
      setCredentialTarget(null)
      credentialForm.resetFields()
      await load()
    } catch (cause) {
      if (cause !== cancelled) notifyError(errText(cause, t))
    }
  }

  const revokeCredential = (credential: Credential) => {
    confirmAction(modal, {
      title: t('appCredentials.revokeCredential'),
      description: credential.label,
      okText: t('appCredentials.revokeCredential'),
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
              confirmAction(modal, {
                title: t('appCredentials.credentialRevokeConfirm'),
                description: t('appCredentials.affected', { count: review.affected.length }),
                okText: t('appCredentials.revokeCredential'),
                cancelText: t('common.cancel'),
                onOk: () => resolve(),
                onCancel: () => reject(cancelled),
              }, t)
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
          if (cause !== cancelled) notifyError(errText(cause, t))
        }
      },
    }, t)
  }

  const finishBinding = async (values: BindingForm) => {
    try {
      // A setup-only credential is never offered to background work.
      const request: Record<string, unknown> = {
        name: values.name,
        credential_id: values.credential_id,
        operation: values.operation,
      }
      if (values.operation === 'pg_password') {
        // A PostgreSQL binding is for background work only, and always names its target.
        request.principals = ['service']
        request.target = values.target ?? (postgresTargets.length === 1 ? postgresTargets[0].id : '')
      } else if (values.operation !== 'raw_read' && values.operation !== 'ingress_hmac_sha256') {
        request.principals = principalsForUse(values.use ?? 'viewer')
      }
      if (values.operation === 'ingress_hmac_sha256') request.ingress_route = (values.ingress_route ?? '').trim()
      if (values.operation === 'acs3_hmac_sha256') {
        request.acs3_actions = acs3Sorted(
          (values.acs3_actions ?? []).map((item) => ({ action: item.action?.trim() ?? '', version: item.version?.trim() ?? '' })),
        )
      }
      // A grant with several targets needs the one the binding is for named (an HTTP binding only:
      // a signed-request binding covers the whole network policy).
      if (isHttpOperation(values.operation) && targets.length > 1 && values.target) request.target = values.target
      const plan = await api.post<BindingPlan>(
        `/api/admin/app-platform/apps/${encodeURIComponent(appKey)}/secret-bindings/plan`,
        request,
      )
      await new Promise<void>((resolve, reject) => {
        confirmAction(modal, {
          title: t('appCredentials.confirmBinding'),
          description: (
            <Space orientation="vertical" size={4}>
              <span>{t('appCredentials.confirmBindingBody')}</span>
              <Typography.Text code>{plan.review.credential_label}</Typography.Text>
              <Typography.Text type="secondary">
                {t(`appCredentials.operation.${plan.review.operation}`)}{plan.review.network_policy_sha256 ? ` · ${plan.review.network_policy_sha256.slice(0, 12)}` : ''}
              </Typography.Text>
              {plan.review.operation === 'ingress_hmac_sha256' && (
                <>
                  <Typography.Text>{t('appCredentials.ingressUrl')}</Typography.Text>
                  <Typography.Text code copyable>{`${window.location.origin}${plan.review.ingress_url ?? ''}`}</Typography.Text>
                  <Typography.Text type="secondary">{t('appCredentials.ingressRecipe')}</Typography.Text>
                  <Alert type="info" showIcon title={t('appCredentials.ingressDisclosure')} />
                </>
              )}
              {plan.review.network_target ? (
                <Typography.Text>
                  {t('appCredentials.bindingTarget', { origin: plan.review.network_target })}
                </Typography.Text>
              ) : (
                plan.review.network_origin && <Typography.Text code>{plan.review.network_origin}</Typography.Text>
              )}
              {plan.review.acs3_actions?.map((item) => (
                <Typography.Text key={acs3Label(item)}>{acs3Label(item)}</Typography.Text>
              ))}
              {plan.review.operation === 'acs3_hmac_sha256' && (
                <Alert type="info" showIcon title={t('appCredentials.acs3Disclosure')} />
              )}
              {plan.review.operation === 'pg_password' && (
                <Alert type="info" showIcon title={t('appCredentials.pgDisclosure')} />
              )}
              {plan.review.operation !== 'ingress_hmac_sha256' && (
                <Typography.Text>
                  {t(`appCredentials.principals.${principalsToUse(plan.review.principals)}`)}
                </Typography.Text>
              )}
              {plan.review.principals?.includes('service') && (
                <Alert type="warning" showIcon title={t('appCredentials.backgroundDisclosure')} />
              )}
            </Space>
          ),
          okText: t('appCredentials.approve'),
          danger: false,
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
        }, t)
      })
      message.success(t('appCredentials.saved'))
      setBindingOpen(false)
      setBindingSeed(null)
      bindingForm.resetFields()
      await load()
    } catch (cause) {
      if (cause !== cancelled) notifyError(errText(cause, t))
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
      notifyError(errText(cause, t))
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
            use: principalsToUse(binding.principals),
            target: (binding.operation === 'pg_password' ? postgresTargets : targets).find((choice) => choice.origin === binding.network_target)?.id,
            acs3_actions: binding.acs3_actions?.length ? binding.acs3_actions : undefined,
            ingress_route: binding.ingress_route,
          }
        : {
            name: appKey === 'scheduled-digest' ? 'digest-feed' : '',
            credential_id: '',
            operation: appKey === 'scheduled-digest' ? 'raw_read' : 'http_bearer',
            use: 'viewer',
            acs3_actions: undefined,
            ingress_route: undefined,
            target: undefined,
          },
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
      title: t('appCredentials.acs3Actions'),
      key: 'acs3_actions',
      render: (_: unknown, binding: Binding) =>
        binding.acs3_actions?.length ? (
          <Space orientation="vertical" size={0}>
            {binding.acs3_actions.map((item) => (
              <span key={acs3Label(item)}>{acs3Label(item)}</span>
            ))}
          </Space>
        ) : null,
    },
    {
      title: t('appCredentials.ingressRoute'),
      key: 'ingress_route',
      render: (_: unknown, binding: Binding) =>
        binding.ingress_route ? (
          <Space orientation="vertical" size={0}>
            <span>{binding.ingress_route}</span>
            {binding.ingress && (
              <Typography.Text type="secondary">
                {t('appCredentials.ingressTraffic', { accepted: binding.ingress.accepted, refused: binding.ingress.refused, since: new Date(binding.ingress.window_start).toLocaleString() })}
              </Typography.Text>
            )}
          </Space>
        ) : null,
    },
    {
      title: t('appCredentials.target'),
      key: 'target',
      render: (_: unknown, binding: Binding) => binding.network_target ?? null,
    },
    {
      title: t('appCredentials.usedBy'),
      key: 'principals',
      render: (_: unknown, binding: Binding) =>
        binding.operation === 'raw_read' || binding.operation === 'ingress_hmac_sha256' ? null : t(`appCredentials.principals.${principalsToUse(binding.principals)}`),
    },
    {
      title: t('appCredentials.state'),
      dataIndex: 'state',
      render: (state: string) => <Tag>{t(`appCredentials.state.${state}`)}</Tag>,
    },
    {
      title: t('appPlatform.action'),
      key: 'actions',
      width: 260,
      render: (_: unknown, binding: Binding) =>
        binding.state === 'revoked' ? null : binding.state === 'review_required' ? (
          <Button size="small" icon={<SafetyCertificateOutlined aria-hidden="true" />} onClick={() => openBinding(binding)}>
            {t('appCredentials.reapprove')}
          </Button>
        ) : (
          <Space size={4}>
            {binding.operation !== 'raw_read' && binding.operation !== 'ingress_hmac_sha256' && (
              <Button size="small" onClick={() => openBinding(binding)}>
                {t('appCredentials.changeUse')}
              </Button>
            )}
            <Confirm
              title={t('appCredentials.revokeBindingConfirm')}
              okText={t('appCredentials.revoke')}
              onConfirm={() => revokeBinding(binding)}
            >
              <Button size="small" danger>
                {t('appCredentials.revoke')}
              </Button>
            </Confirm>
          </Space>
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
          <Form.Item name="kind" label={t('appCredentials.kind')} initialValue="secret">
            <Select
              options={(['secret', 'acs3', 'postgres'] as CredentialKind[]).map((kind) => ({
                value: kind,
                label: t(`appCredentials.kind.${kind}`),
              }))}
            />
          </Form.Item>
          {credentialKind === 'acs3' ? (
            <>
              <Form.Item name="access_key_id" label={t('appCredentials.accessKeyId')} rules={[{ required: true, max: 128, pattern: /^[\x21-\x7e]+$/ }]}>
                <Input autoComplete="off" />
              </Form.Item>
              <Form.Item
                name="access_key_secret"
                label={t('appCredentials.accessKeySecret')}
                extra={t('appCredentials.valueHint')}
                rules={[{ required: true, max: 256, pattern: /^[\x20-\x7e]+$/ }]}
              >
                <Input.Password autoComplete="new-password" />
              </Form.Item>
              <Form.Item
                name="security_token"
                label={t('appCredentials.securityToken')}
                extra={t('appCredentials.securityTokenHint')}
                rules={[{ max: 4096, pattern: /^[\x20-\x7e]*$/ }]}
              >
                <Input.Password autoComplete="new-password" />
              </Form.Item>
            </>
          ) : credentialKind === 'postgres' ? (
            <>
              <Form.Item name="pg_username" label={t('appCredentials.pgUsername')} rules={[{ required: true, max: 63, pattern: /^[\x20-\x7e]+$/ }]}>
                <Input autoComplete="off" />
              </Form.Item>
              <Form.Item
                name="pg_password"
                label={t('appCredentials.pgPassword')}
                extra={t('appCredentials.valueHint')}
                rules={[{ required: true, max: 1024, pattern: /^[\x20-\x7e]+$/ }]}
              >
                <Input.Password autoComplete="off" />
              </Form.Item>
            </>
          ) : (
            <Form.Item name="value" label={t('appCredentials.value')} extra={t('appCredentials.valueHint')} rules={[{ required: true, min: 1, max: 16384 }]}>
              <Input.Password autoComplete="new-password" />
            </Form.Item>
          )}
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
                ...(appKey === 'scheduled-digest'
                  ? [{ value: 'raw_read', label: t('appCredentials.operation.raw_read') }]
                  : []),
                { value: 'http_bearer', label: t('appCredentials.operation.http_bearer') },
                { value: 'http_api_key', label: t('appCredentials.operation.http_api_key') },
                { value: 'acs3_hmac_sha256', label: t('appCredentials.operation.acs3_hmac_sha256') },
                { value: 'pg_password', label: t('appCredentials.operation.pg_password') },
                { value: 'ingress_hmac_sha256', label: t('appCredentials.operation.ingress_hmac_sha256') },
              ]}
            />
          </Form.Item>
          {bindingOperation === 'pg_password' && (
            <Form.Item name="target" label={t('appCredentials.pgTarget')} rules={[{ required: true }]}>
              <Select
                options={postgresTargets.map((choice) => ({ value: choice.id, label: `${choice.id} · ${choice.origin}` }))}
              />
            </Form.Item>
          )}
          {bindingOperation === 'acs3_hmac_sha256' && (
            <Form.Item label={t('appCredentials.acs3Actions')} extra={t('appCredentials.acs3ActionsHint')} required>
              <Form.List name="acs3_actions" initialValue={[{ action: '', version: '' }]}>
                {(fields, { add, remove }) => (
                  <Space orientation="vertical" size={8} style={{ width: '100%' }}>
                    {fields.map((field) => (
                      <Space key={field.key} align="start" wrap>
                        <Form.Item
                          name={[field.name, 'action']}
                          noStyle
                          rules={[{ required: true, pattern: /^[A-Za-z][A-Za-z0-9]{0,63}$/ }]}
                        >
                          <Input aria-label={t('appCredentials.acs3Action')} placeholder="QueryAccountBalance" autoComplete="off" />
                        </Form.Item>
                        <Form.Item
                          name={[field.name, 'version']}
                          noStyle
                          rules={[{ required: true, pattern: /^[0-9]{4}-[0-9]{2}-[0-9]{2}$/ }]}
                        >
                          <Input aria-label={t('appCredentials.acs3Version')} placeholder="2017-12-14" autoComplete="off" />
                        </Form.Item>
                        {fields.length > 1 && (
                          <Button
                            aria-label={t('appCredentials.acs3RemoveAction')}
                            icon={<DeleteOutlined aria-hidden="true" />}
                            onClick={() => remove(field.name)}
                          />
                        )}
                      </Space>
                    ))}
                    {fields.length < 16 && (
                      <Button icon={<PlusOutlined aria-hidden="true" />} onClick={() => add({ action: '', version: '' })}>
                        {t('appCredentials.acs3AddAction')}
                      </Button>
                    )}
                  </Space>
                )}
              </Form.List>
            </Form.Item>
          )}
          {bindingOperation === 'ingress_hmac_sha256' && (
            <Form.Item
              name="ingress_route"
              label={t('appCredentials.ingressRoute')}
              extra={t('appCredentials.ingressRouteHint')}
              rules={[{ required: true, pattern: /^[a-z][a-z0-9-]{0,31}$/ }]}
            >
              <Input autoComplete="off" />
            </Form.Item>
          )}
          {isHttpOperation(bindingOperation) && targets.length > 1 && (
            <Form.Item name="target" label={t('appCredentials.target')} rules={[{ required: true }]}>
              <Select
                options={targets.map((choice) => ({ value: choice.id, label: `${choice.id} · ${choice.origin}` }))}
              />
            </Form.Item>
          )}
          {bindingOperation !== 'raw_read' && bindingOperation !== 'pg_password' && bindingOperation !== 'ingress_hmac_sha256' && (
            <Form.Item
              name="use"
              label={t('appCredentials.principals')}
              extra={t('appCredentials.principalsHint')}
              rules={[{ required: true }]}
            >
              <Select
                options={(['viewer', 'both', 'service'] as Use[]).map((use) => ({
                  value: use,
                  label: t(`appCredentials.principals.${use}`),
                }))}
              />
            </Form.Item>
          )}
          <Button type="primary" htmlType="submit">
            {t('appCredentials.reviewBinding')}
          </Button>
        </Form>
      </Modal>
    </LoadGate>
  )
}
