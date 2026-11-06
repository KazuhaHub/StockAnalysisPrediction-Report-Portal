import { useEffect, useState } from 'react'
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Space, Table, Tag, Typography } from 'antd'
import { PauseOutlined, PlusOutlined, ReloadOutlined, StopOutlined, SyncOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import Confirm from '../../components/Confirm'

type Subscription = {
  id: string
  installation_id: string
  app_id: string
  event_type: string
  resource_type: string
  resource_id: string
  state: string
  generation: number
  policy_revision: number
  max_inflight: number
  revision: number
  created_ms: number
  updated_ms: number
}
type Draft = {
  installationId: string
  eventType: 'report.ingested' | 'batch.job.finished'
  resourceId: string
  maxInflight: number
}

const root = '/api/admin/app-platform/event-subscriptions'
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const reportResource = /^[1-9][0-9]*$/
const jobResource = /^[1-9][0-9]*@[1-9][0-9]*$/

export default function AppPlatformEventSubscriptionsPanel() {
  const { t } = useTranslation()
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(false)
  const [reload, setReload] = useState(0)
  const [form] = Form.useForm<Draft>()

  useEffect(() => {
    const abort = new AbortController()
    setLoading(true)
    api.get<{ subscriptions: Subscription[] }>(`${root}?limit=200`, { signal: abort.signal })
      .then((value) => {
        if (!abort.signal.aborted) {
          setSubscriptions(Array.isArray(value.subscriptions) ? value.subscriptions : [])
          setError('')
        }
      })
      .catch((failure) => { if (!abort.signal.aborted) setError(errText(failure, t)) })
      .finally(() => { if (!abort.signal.aborted) setLoading(false) })
    return () => abort.abort()
  }, [reload, t])

  const eventLabel = (eventType: string) => {
    if (eventType === 'report.ingested') return t('appPlatform.events.reportIngested')
    if (eventType === 'batch.job.finished') return t('appPlatform.events.batchFinished')
    return eventType
  }
  const stateColor = (state: string) => state === 'active' ? 'success'
    : state === 'review_required' ? 'error' : 'default'
  const changeState = async (sub: Subscription, state: 'paused' | 'revoked') => {
    setBusy(`${sub.id}:${state}`); setError('')
    try {
      await api.post(`${root}/${sub.id}/state?installation=${sub.installation_id}`, {
        state, expectedRevision: sub.revision,
      })
      setReload((value) => value + 1)
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy('')
    }
  }
  const rebind = async (sub: Subscription) => {
    setBusy(`${sub.id}:rebind`); setError('')
    try {
      await api.post(`${root}/${sub.id}/rebind?installation=${sub.installation_id}`, {
        expectedRevision: sub.revision,
      })
      setReload((value) => value + 1)
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy('')
    }
  }
  const create = async () => {
    let values: Draft
    try {
      values = await form.validateFields()
    } catch {
      return
    }
    const resource = values.eventType === 'batch.job.finished' ? jobResource : reportResource
    if (!uuid.test(values.installationId) || !resource.test(values.resourceId)) return
    setBusy('create'); setError('')
    try {
      await api.post(root, {
        installationId: values.installationId,
        eventType: values.eventType,
        resourceType: values.eventType === 'report.ingested' ? 'report' : 'job',
        resourceId: values.resourceId,
        maxInflight: values.maxInflight,
      })
      setEditing(false)
      setReload((value) => value + 1)
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy('')
    }
  }

  return <Space orientation="vertical" size={16} style={{ width: '100%' }}>
    <Alert type="info" showIcon title={t('appPlatform.events.explanation')} />
    <Space wrap>
      <Button icon={<PlusOutlined aria-hidden="true" />} onClick={() => {
        form.setFieldsValue({ installationId: '', eventType: 'report.ingested', resourceId: '', maxInflight: 4 })
        setEditing(true)
      }}>{t('appPlatform.events.create')}</Button>
      <Button icon={<ReloadOutlined aria-hidden="true" />} loading={loading}
        disabled={!!busy} onClick={() => setReload((value) => value + 1)}>
        {t('appPlatform.events.refresh')}
      </Button>
    </Space>
    {error && <Alert type="error" showIcon title={error} closable onClose={() => setError('')} />}
    <Table<Subscription>
      rowKey="id"
      loading={loading}
      dataSource={subscriptions}
      pagination={false}
      scroll={{ x: 920 }}
      locale={{ emptyText: t('appPlatform.events.empty') }}
      columns={[
        {
          title: t('appPlatform.events.application'),
          render: (_, sub) => <Space orientation="vertical" size={0}>
            <Typography.Text strong>{sub.app_id}</Typography.Text>
            <Typography.Text type="secondary">{sub.installation_id}</Typography.Text>
          </Space>,
        },
        {
          title: t('appPlatform.events.event'),
          render: (_, sub) => <Space orientation="vertical" size={0}>
            <Typography.Text>{eventLabel(sub.event_type)}</Typography.Text>
            <Typography.Text code>{sub.resource_type}:{sub.resource_id}</Typography.Text>
          </Space>,
        },
        {
          title: t('appPlatform.tasks.state'),
          dataIndex: 'state',
          render: (state: string) => <Tag color={stateColor(state)}>
            {t(`appPlatform.events.state.${state}`, { defaultValue: state })}
          </Tag>,
        },
        { title: t('appPlatform.events.maxInflight'), dataIndex: 'max_inflight' },
        { title: t('appPlatform.events.revision'), dataIndex: 'revision' },
        {
          title: t('appPlatform.action'),
          render: (_, sub) => <Space wrap>
            {sub.state === 'active' && <Button size="small" icon={<PauseOutlined aria-hidden="true" />}
              loading={busy === `${sub.id}:paused`} disabled={!!busy}
              onClick={() => void changeState(sub, 'paused')}>{t('appPlatform.events.pause')}</Button>}
            {(sub.state === 'paused' || sub.state === 'review_required') && <Button size="small"
              icon={<SyncOutlined aria-hidden="true" />}
              loading={busy === `${sub.id}:rebind`} disabled={!!busy}
              onClick={() => void rebind(sub)}>{t('appPlatform.events.rebind')}</Button>}
            {sub.state !== 'revoked' && <Confirm
              title={t('appPlatform.events.revokeConfirm')}
              description={t('appPlatform.events.revokeConfirmBody')}
              okText={t('appPlatform.events.revoke')}
              irreversible
              onConfirm={() => void changeState(sub, 'revoked')}>
              <Button size="small" danger
                icon={<StopOutlined aria-hidden="true" />}
                loading={busy === `${sub.id}:revoked`} disabled={!!busy}>
                {t('appPlatform.events.revoke')}
              </Button>
            </Confirm>}
          </Space>,
        },
      ]}
    />
    <Modal open={editing} title={t('appPlatform.events.create')}
      okText={t('appPlatform.events.create')}
      cancelText={t('appPlatform.cancel')}
      confirmLoading={busy === 'create'}
      onCancel={() => { if (!busy) setEditing(false) }}
      onOk={() => void create()}>
      <Form form={form} layout="vertical">
        <Form.Item name="installationId" label={t('appPlatform.tasks.installation')}
          rules={[{ required: true, pattern: uuid, message: t('appPlatform.events.invalidInstallation') }]}>
          <Input maxLength={36} />
        </Form.Item>
        <Form.Item name="eventType" label={t('appPlatform.events.event')} rules={[{ required: true }]}>
          <Select options={[
            { value: 'report.ingested', label: t('appPlatform.events.reportIngested') },
            { value: 'batch.job.finished', label: t('appPlatform.events.batchFinished') },
          ]} />
        </Form.Item>
        <Form.Item name="resourceId" label={t('appPlatform.events.resourceId')}
          rules={[{
            required: true,
            validator: async (_, value: string | undefined) => {
              const pattern = form.getFieldValue('eventType') === 'batch.job.finished'
                ? jobResource : reportResource
              if (!value || !pattern.test(value)) {
                throw new Error(t('appPlatform.events.invalidResource'))
              }
            },
          }]}>
          <Input maxLength={41} />
        </Form.Item>
        <Form.Item name="maxInflight" label={t('appPlatform.events.maxInflight')}
          rules={[{ required: true }]}>
          <InputNumber min={1} max={16} precision={0} style={{ width: '100%' }} />
        </Form.Item>
      </Form>
    </Modal>
  </Space>
}
