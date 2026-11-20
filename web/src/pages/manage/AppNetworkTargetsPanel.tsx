import { useState } from 'react'
import { Alert, Button, Descriptions, Input, InputNumber, Modal, Select, Space, Table, Tag, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { errText } from '../../api/client'
import NetworkChangeReview from '../../components/apps/NetworkChangeReview'
import {
  consumeNetworkPlan, planNetworkChange,
  type BoundsChange, type CapabilityPlan, type NetworkChange, type NetworkPolicyView, type PolicyTarget,
} from '../../lib/appNetworkTargetsV2'
import type { NetworkTargetRequest } from '../../lib/network-v2'

// The administrator's view of an installation's network.http destinations (doc 175 section 2.6).
// Every change is a plan the server drew up: the confirmation shows the server's diff, and only
// then is the plan consumed.

const methodChoices = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS']

export default function AppNetworkTargetsPanel({ installation, policyRevision, policy, ready, onChanged }: {
  installation: string
  policyRevision: string
  policy: NetworkPolicyView
  ready: boolean
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const [plan, setPlan] = useState<CapabilityPlan | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [dialogError, setDialogError] = useState('')
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState({ id: '', origin: '', paths: '', methods: ['GET'], network: 'public', requestBytes: null as number | null, responseBytes: null as number | null, deadlineMs: null as number | null })
  const fromPolicy = () => ({ requestBytes: policy.bounds.request_bytes, responseBytes: policy.bounds.response_bytes, deadlineMs: policy.bounds.deadline_ms, concurrency: policy.bounds.concurrency })
  const [bounds, setBounds] = useState(fromPolicy)

  const propose = async (change: NetworkChange) => {
    if (busy) return
    setBusy(true); setError(''); setDialogError('')
    try {
      setPlan(await planNetworkChange(installation, { policyRevision, policy }, change))
      setAdding(false); setEditing(false)
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const confirm = async () => {
    if (!plan || busy) return
    setBusy(true); setDialogError('')
    try {
      await consumeNetworkPlan(plan)
      setPlan(null)
      onChanged()
    } catch (failure) {
      setDialogError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const submitTarget = () => {
    const target: NetworkTargetRequest = {
      id: draft.id.trim(), origin: draft.origin.trim(),
      paths: draft.paths.split('\n').map(item => item.trim()).filter(Boolean),
      methods: draft.methods, network: draft.network,
      ...(draft.requestBytes ? { requestBytes: draft.requestBytes } : {}),
      ...(draft.responseBytes ? { responseBytes: draft.responseBytes } : {}),
      ...(draft.deadlineMs ? { deadlineMs: draft.deadlineMs } : {}),
    }
    void propose({ add: [target], remove: [] })
  }
  const submitBounds = () => {
    const change: BoundsChange = {}
    if (bounds.requestBytes !== policy.bounds.request_bytes) change.requestBytes = bounds.requestBytes
    if (bounds.responseBytes !== policy.bounds.response_bytes) change.responseBytes = bounds.responseBytes
    if (bounds.deadlineMs !== policy.bounds.deadline_ms) change.deadlineMs = bounds.deadlineMs
    if (bounds.concurrency !== policy.bounds.concurrency) change.concurrency = bounds.concurrency
    if (!Object.keys(change).length) { setEditing(false); return }
    void propose({ add: [], remove: [], bounds: change })
  }
  const limits = (item: PolicyTarget) => [
    item.request_bytes ? t('appPlatform.network.limit.requestBytes', { value: item.request_bytes }) : '',
    item.response_bytes ? t('appPlatform.network.limit.responseBytes', { value: item.response_bytes }) : '',
    item.deadline_ms ? t('appPlatform.network.limit.deadlineMs', { value: item.deadline_ms }) : '',
  ].filter(Boolean).join(' · ') || t('appPlatform.network.limit.shared')

  return <Space orientation="vertical" size={12} style={{ width: '100%' }} data-testid="network-targets-panel">
    {error && <Alert type="error" showIcon title={error} />}
    {!ready && <Typography.Text type="secondary">{t('appPlatform.network.notReady')}</Typography.Text>}
    <Table<PolicyTarget>
      size="small" pagination={false} rowKey="id" dataSource={policy.targets} scroll={{ x: 'max-content' }}
      locale={{ emptyText: t('appPlatform.network.none') }}
      columns={[
        { key: 'id', title: t('appPlatform.network.id'), dataIndex: 'id' },
        { key: 'origin', title: t('appPlatform.network.origin'), dataIndex: 'origin', render: (value: string) => <Typography.Text code>{value}</Typography.Text> },
        { key: 'network', title: t('appPlatform.network.class.title'), dataIndex: 'network', render: (value: string) => <Tag color={value === 'private' ? 'error' : 'default'}>{t(`appPlatform.network.class.${value}`, { defaultValue: value })}</Tag> },
        { key: 'paths', title: t('appPlatform.network.paths'), dataIndex: 'paths', render: (value: string[]) => value.join(', ') },
        { key: 'methods', title: t('appPlatform.network.methods'), dataIndex: 'methods', render: (value: string[]) => value.join(', ') },
        { key: 'limits', title: t('appPlatform.network.limits'), render: (_: unknown, item) => limits(item) },
        { key: 'actions', title: '', render: (_: unknown, item) => <Button size="small" danger disabled={!ready || busy} icon={<DeleteOutlined aria-hidden="true" />} aria-label={t('appPlatform.network.removeTarget', { id: item.id })} onClick={() => void propose({ add: [], remove: [item.id] })}>{t('appPlatform.network.remove')}</Button> },
      ]}
    />
    <Descriptions column={1} size="small" title={t('appPlatform.network.sharedLimits')} extra={<Button size="small" disabled={!ready || busy} icon={<EditOutlined aria-hidden="true" />} onClick={() => { setError(''); setBounds(fromPolicy()); setEditing(true) }}>{t('appPlatform.network.editLimits')}</Button>} items={[
      { key: 'request', label: t('appPlatform.network.limit.requestBytesName'), children: policy.bounds.request_bytes },
      { key: 'response', label: t('appPlatform.network.limit.responseBytesName'), children: policy.bounds.response_bytes },
      { key: 'deadline', label: t('appPlatform.network.limit.deadlineMsName'), children: policy.bounds.deadline_ms },
      { key: 'concurrency', label: t('appPlatform.network.limit.concurrencyName'), children: policy.bounds.concurrency },
    ]} />
    <Typography.Paragraph type="secondary" style={{ margin: 0 }}>{t('appPlatform.network.allowlistHint')}</Typography.Paragraph>
    <Button disabled={!ready || busy} icon={<PlusOutlined aria-hidden="true" />} onClick={() => { setError(''); setAdding(true) }}>{t('appPlatform.network.add')}</Button>

    <Modal className="rp-platform-review-modal" open={adding} title={t('appPlatform.network.add')} okText={t('appPlatform.network.review.plan')} cancelText={t('common.cancel')}
      okButtonProps={{ disabled: !draft.id.trim() || !draft.origin.trim() || !draft.paths.trim() || !draft.methods.length }} confirmLoading={busy} mask={{ closable: false }}
      onOk={submitTarget} onCancel={() => { if (!busy) setAdding(false) }}>
      <Space orientation="vertical" size={8} style={{ width: '100%' }}>
        {error && <Alert type="error" showIcon title={error} />}
        <label>{t('appPlatform.network.id')}<Input value={draft.id} onChange={event => setDraft({ ...draft, id: event.target.value })} /></label>
        <label>{t('appPlatform.network.origin')}<Input value={draft.origin} placeholder="https://api.example.com" onChange={event => setDraft({ ...draft, origin: event.target.value })} /></label>
        <label>{t('appPlatform.network.pathsHint')}<Input.TextArea rows={3} value={draft.paths} onChange={event => setDraft({ ...draft, paths: event.target.value })} /></label>
        <label>{t('appPlatform.network.methods')}<Select mode="multiple" style={{ width: '100%' }} value={draft.methods} options={methodChoices.map(value => ({ value, label: value }))} onChange={value => setDraft({ ...draft, methods: value })} /></label>
        <label>{t('appPlatform.network.class.title')}<Select style={{ width: '100%' }} value={draft.network} options={['public', 'private'].map(value => ({ value, label: t(`appPlatform.network.class.${value}`) }))} onChange={value => setDraft({ ...draft, network: value })} /></label>
        <Space wrap>
          <label>{t('appPlatform.network.limit.requestBytesName')}<InputNumber min={1} value={draft.requestBytes} onChange={value => setDraft({ ...draft, requestBytes: value })} /></label>
          <label>{t('appPlatform.network.limit.responseBytesName')}<InputNumber min={1} value={draft.responseBytes} onChange={value => setDraft({ ...draft, responseBytes: value })} /></label>
          <label>{t('appPlatform.network.limit.deadlineMsName')}<InputNumber min={1} value={draft.deadlineMs} onChange={value => setDraft({ ...draft, deadlineMs: value })} /></label>
        </Space>
        <Typography.Text type="secondary">{t('appPlatform.network.targetLimitHint')}</Typography.Text>
      </Space>
    </Modal>

    <Modal className="rp-platform-review-modal" open={editing} title={t('appPlatform.network.editLimits')} okText={t('appPlatform.network.review.plan')} cancelText={t('common.cancel')}
      confirmLoading={busy} mask={{ closable: false }} onOk={submitBounds} onCancel={() => { if (!busy) setEditing(false) }}>
      <Space orientation="vertical" size={8} style={{ width: '100%' }}>
        {error && <Alert type="error" showIcon title={error} />}
        {(['requestBytes', 'responseBytes', 'deadlineMs', 'concurrency'] as const).map(key => <label key={key}>{t(`appPlatform.network.limit.${key}Name`)}
          <InputNumber min={1} style={{ width: '100%' }} value={bounds[key]} onChange={value => { if (value) setBounds({ ...bounds, [key]: value }) }} /></label>)}
      </Space>
    </Modal>

    <NetworkChangeReview plan={plan} busy={busy} error={dialogError} onConfirm={() => void confirm()} onCancel={() => { setPlan(null); setDialogError(''); setBounds(fromPolicy()) }} />
  </Space>
}
