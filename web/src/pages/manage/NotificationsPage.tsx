import { useEffect, useState } from 'react'
import { Alert, App, Button, Checkbox, Drawer, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tabs, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { notifications, type NotificationChannel, type NotificationDelivery, type NotificationDeliveryDetail, type NotificationOverview, type NotificationRoute, type NotificationChannelInput, type NotificationRouteInput, type NotificationState } from '../../api/notifications'
import { errText } from '../../api/client'
import LoadGate from '../../components/LoadGate'

export default function NotificationsPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [data, setData] = useState<NotificationOverview>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [channel, setChannel] = useState<NotificationChannel | null | undefined>()
  const [rule, setRule] = useState<NotificationRoute | null | undefined>()
  const [channelForm] = Form.useForm()
  const [ruleForm] = Form.useForm()
  const [history, setHistory] = useState<NotificationDelivery[]>([])
  const [historyLoaded, setHistoryLoaded] = useState(false)
  const [historyError, setHistoryError] = useState('')
  const [next, setNext] = useState('')
  const [state, setState] = useState<NotificationState>()
  const [detail, setDetail] = useState<NotificationDeliveryDetail>()
  const [testChannel, setTestChannel] = useState<NotificationChannel>()
  const [testTo, setTestTo] = useState('')
  const [action, setAction] = useState<'replay' | 'review'>()
  const [reason, setReason] = useState('')
  const [confirmed, setConfirmed] = useState(false)
  const [requestID, setRequestID] = useState('')
  const [resume, setResume] = useState(false)
  const [preview, setPreview] = useState<{ subject: string; text: string }>()
  const [sample, setSample] = useState('report.ingested')
  const [retentionOpen, setRetentionOpen] = useState(false)
  const [retentionForm] = Form.useForm()

  const load = async () => {
    setError('')
    try { setData(await notifications.overview()) } catch (e) { setError(errText(e, t)) }
  }
  const loadHistory = async (more = false) => {
    setHistoryError('')
    try {
      const result = await notifications.history({ state, cursor: more ? next : undefined })
      setHistory(old => more ? [...old, ...result.deliveries] : result.deliveries)
      setNext(result.next_cursor)
      setHistoryLoaded(true)
    } catch (e) { setHistoryError(errText(e, t)) }
  }
  useEffect(() => { void load() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (historyLoaded) void loadHistory() }, [state]) // eslint-disable-line react-hooks/exhaustive-deps
  const run = async (fn: () => Promise<unknown>, refreshHistory = true) => {
    setBusy(true)
    try { await fn(); await load(); if (historyLoaded && refreshHistory) await loadHistory() } catch (e) { message.error(errText(e, t)) } finally { setBusy(false) }
  }
  const eventLabel = (kind: string) => t(`webhook.event.${kind}`, kind)
  const stateLabel = (value: string) => t(`notify.state.${value}`, value)
  const openChannel = (value: NotificationChannel | null, adapter: NotificationChannel['adapter'] = 'email') => {
    channelForm.resetFields()
    channelForm.setFieldsValue(value ? { ...value, recipients: value.recipients.join(', '), url: '', signing_secret: '' } : { adapter, enabled: true, paused: false, recipients: '' })
    setChannel(value)
  }
  const openRule = (value: NotificationRoute | null) => {
    ruleForm.resetFields()
    ruleForm.setFieldsValue(value ? { ...value, events: value.events.split(',') } : { enabled: true, mandatory: false, grants: [], events: [], recipient_mode: 'grant' })
    setRule(value)
  }
  const saveChannel = async () => {
    const values = await channelForm.validateFields()
    const input: NotificationChannelInput = { ...values, revision: channel?.revision }
    if (!['slack', 'webhook'].includes(values.adapter)) { delete input.url; delete input.clear_destination }
    if (values.adapter !== 'webhook') { delete input.signing_secret; delete input.clear_signing_secret }
    if (values.adapter === 'email' && channel?.id !== 'legacy:smtp') input.recipients = String(values.recipients || '').split(',').map((v: string) => v.trim()).filter(Boolean)
    else delete input.recipients
    await run(async () => { await notifications.saveChannel(input, channel?.id); setChannel(undefined) })
  }
  const saveRule = async () => {
    const values = await ruleForm.validateFields()
    const input: NotificationRouteInput = { ...values, revision: rule?.revision, grants: values.recipient_mode === 'grant' ? values.grants || [] : [], mandatory: values.recipient_mode === 'destination' ? false : values.mandatory }
    await run(async () => { await notifications.saveRoute(input, rule?.id); setRule(undefined) })
  }
  const selectedAdapter = Form.useWatch('adapter', channelForm) ?? channel?.adapter ?? 'email'
  const recipientMode = Form.useWatch('recipient_mode', ruleForm) ?? rule?.recipient_mode ?? 'grant'
  const channels = data?.channels || []
  const rules = data?.routes || []
  const needsConfirmation = detail && (['accepted', 'uncertain'].includes(detail.delivery.state) || detail.delivery.error_code === 'reviewed_uncertain')
  const showDetail = async (id: string) => { await run(async () => setDetail(await notifications.delivery(id))) }
  const finishAction = async () => {
    if (!detail || !action || !reason.trim()) return
    const d = detail.delivery
    await run(async () => {
      if (action === 'review') await notifications.review(d.id, { decision: 'dismiss', reason, updated_at: d.updated_at })
      else {
        const c = channels.find(item => item.id === d.channel_id)
        const r = rules.find(item => item.id === d.route_id)
        if (!c) throw new Error(t('notify.missingChannel'))
        await notifications.replay(d.id, { request_id: requestID, channel_revision: c.revision, route_revision: r?.revision || 0, confirm_duplicate: confirmed, reason })
      }
      setAction(undefined)
      setDetail(await notifications.delivery(d.id))
    })
  }

  return <LoadGate loading={!data} error={error} onRetry={load}>
    <Space orientation="vertical" size="large" style={{ width: '100%' }}>
      <Space wrap><Typography.Title level={3} style={{ margin: 0 }}>{t('notify.title')}</Typography.Title><Button onClick={() => run(load)} loading={busy}>{t('common.refresh')}</Button></Space>
      <Alert type={data?.dispatch_paused ? 'warning' : 'info'} showIcon title={t(data?.dispatch_paused ? 'notify.paused' : 'notify.running')} description={<Space wrap>
        <span>{t('notify.queueCounts', data?.counts || {})}</span>
        <Button disabled={busy} onClick={() => data?.dispatch_paused ? setResume(true) : run(() => notifications.dispatch(true))}>{t(data?.dispatch_paused ? 'notify.resume' : 'notify.pause')}</Button>
      </Space>} />
      <Tabs onChange={key => { if (key === 'history' && !historyLoaded) void loadHistory() }} items={[
        { key: 'channels', label: t('notify.channels'), children: <Space orientation="vertical" style={{ width: '100%' }}>
          <Typography.Paragraph>{t('notify.channelHelp')}</Typography.Paragraph>
          <Button type="primary" onClick={() => openChannel(null)}>{t('notify.addChannel')}</Button>
          <Table<NotificationChannel> rowKey="id" dataSource={channels} scroll={{ x: 850 }} columns={[
            { title: t('notify.label'), dataIndex: 'label' },
            { title: t('notify.adapter'), render: (_, c) => t(`notify.adapter.${c.adapter}`) },
            { title: t('notify.destination'), render: (_, c) => c.adapter === 'email' ? c.recipients.join(', ') || t('notify.personalEmail') : c.url_host || '—' },
            { title: t('notify.status'), render: (_, c) => <Tag>{t(!c.enabled ? 'notify.disabled' : c.paused ? 'notify.paused' : 'notify.enabled')}</Tag> },
            { title: t('batch.col.actions'), render: (_, c) => <Space wrap>
              <Button size="small" onClick={() => openChannel(c)}>{t('common.edit')}</Button>
              {c.adapter !== 'inbox' && <Button size="small" disabled={!c.enabled || busy} onClick={() => { setTestTo(''); setTestChannel(c) }}>{t('notify.test')}</Button>}
              {(!c.id.startsWith('legacy:') || c.legacy_ref.startsWith('webhook:')) && <Popconfirm title={t('notify.deleteChannel')} onConfirm={() => run(() => notifications.deleteChannel(c.id, c.revision))}><Button size="small" danger disabled={busy}>{t('common.delete')}</Button></Popconfirm>}
            </Space> },
          ]} />
        </Space> },
        { key: 'rules', label: t('notify.rules'), children: <Space orientation="vertical" style={{ width: '100%' }}>
          <Alert type="info" showIcon title={t('notify.ruleHelp')} />
          <Button type="primary" onClick={() => openRule(null)}>{t('notify.addRule')}</Button>
          <Table<NotificationRoute> rowKey="id" dataSource={rules} scroll={{ x: 750 }} columns={[
            { title: t('notify.label'), dataIndex: 'label' },
            { title: t('notify.channels'), render: (_, r) => channels.find(c => c.id === r.channel_id)?.label || r.channel_id },
            { title: t('notify.events'), render: (_, r) => r.events.split(',').map(kind => <Tag key={kind}>{eventLabel(kind)}</Tag>) },
            { title: t('notify.audience'), render: (_, r) => t(`notify.mode.${r.recipient_mode}`) },
            { title: t('notify.status'), render: (_, r) => t(r.enabled ? 'notify.enabled' : 'notify.disabled') },
            { title: t('batch.col.actions'), render: (_, r) => <Space><Button size="small" onClick={() => openRule(r)}>{t('common.edit')}</Button><Popconfirm title={t('notify.deleteRule')} onConfirm={() => run(() => notifications.deleteRoute(r.id, r.revision))}><Button size="small" danger disabled={busy}>{t('common.delete')}</Button></Popconfirm></Space> },
          ]} />
        </Space> },
        { key: 'history', label: t('notify.history'), children: <Space orientation="vertical" style={{ width: '100%' }}>
          <Alert type="info" title={t('notify.acceptanceHelp')} />
          <Space><Select allowClear placeholder={t('notify.status')} value={state} onChange={setState} style={{ width: 180 }} options={['pending', 'leased', 'retry_wait', 'accepted', 'failed', 'uncertain', 'canceled', 'expired'].map(value => ({ value, label: stateLabel(value) }))} /><Button onClick={() => run(() => loadHistory())} disabled={busy}>{t('common.refresh')}</Button></Space>
          {historyError && <Alert type="error" title={historyError} />}
          <Table<NotificationDelivery> rowKey="id" dataSource={history} loading={!historyLoaded && !historyError} pagination={false} scroll={{ x: 850 }} columns={[
            { title: t('notify.occurredAt'), dataIndex: 'occurred_at' },
            { title: t('notify.events'), render: (_, d) => eventLabel(d.event_type) },
            { title: t('notify.channels'), render: (_, d) => channels.find(c => c.id === d.channel_id)?.label || d.channel_id },
            { title: t('notify.status'), render: (_, d) => <Tag>{stateLabel(d.state)}</Tag> },
            { title: t('notify.attempts'), dataIndex: 'attempts' },
            { title: t('batch.col.actions'), render: (_, d) => <Button size="small" disabled={busy} onClick={() => showDetail(d.id)}>{t('notify.details')}</Button> },
          ]} />
          {next && <Button onClick={() => run(() => loadHistory(true), false)} disabled={busy}>{t('notify.more')}</Button>}
        </Space> },
        { key: 'retention', label: t('notify.retention'), children: <Space orientation="vertical">
          <Alert type="warning" showIcon title={t('notify.retentionHelp')} />
          <Typography.Text>{t(data?.retention?.enabled ? 'notify.enabled' : 'notify.disabled')}</Typography.Text>
          <Button onClick={() => { retentionForm.setFieldsValue(data?.retention); setRetentionOpen(true) }}>{t('common.edit')}</Button>
        </Space> },
        { key: 'presets', label: t('notify.presets'), children: <Space orientation="vertical" size="large">
          <Alert type="info" title={t('notify.presetHelp')} />
          {(['email', 'slack', 'webhook', 'inbox'] as const).map(adapter => <Space key={adapter} orientation="vertical">
            <Typography.Title level={5}>{t(`notify.adapter.${adapter}`)}</Typography.Title>
            <Typography.Paragraph>{t(`notify.preset.${adapter}`)}</Typography.Paragraph>
            <Button onClick={() => openChannel(null, adapter)}>{t('notify.usePreset')}</Button>
          </Space>)}
          <Typography.Title level={5}>{t('notify.n8nPreset')}</Typography.Title>
          <Typography.Paragraph>{t('notify.n8nHelp')}</Typography.Paragraph>
          <Space wrap><Button href="/notification-presets/n8n-signed-receiver.json" download>{t('notify.downloadWorkflow')}</Button><Button href="/notification-presets/README.md" download>{t('notify.downloadRecipes')}</Button></Space>
        </Space> },
        { key: 'preview', label: t('notify.preview'), children: <Space orientation="vertical" style={{ width: '100%' }}>
          <Typography.Paragraph>{t('notify.previewHelp')}</Typography.Paragraph>
          <Space><Select value={sample} onChange={setSample} style={{ minWidth: 260 }} options={data?.events.map(value => ({ value, label: eventLabel(value) }))} /><Button onClick={() => run(async () => setPreview(await notifications.preview({ event_type: sample })))} disabled={busy}>{t('notify.preview')}</Button></Space>
          {preview && <><Typography.Title level={5}>{preview.subject}</Typography.Title><Typography.Paragraph style={{ whiteSpace: 'pre-wrap' }}>{preview.text}</Typography.Paragraph></>}
        </Space> },
      ]} />
    </Space>
    <Modal open={retentionOpen} title={t('notify.retention')} onCancel={() => setRetentionOpen(false)} confirmLoading={busy} onOk={async () => { const values = await retentionForm.validateFields(); await run(async () => { await notifications.retention(values); setRetentionOpen(false) }) }}>
      <Alert type="warning" title={t('notify.retentionHelp')} />
      <Form form={retentionForm} layout="vertical">
        <Form.Item name="enabled" valuePropName="checked" label={t('notify.enabled')}><Switch /></Form.Item>
        <Form.Item name="history_days" label={t('notify.historyDays')} rules={[{ required: true }]}><InputNumber min={1} max={3650} /></Form.Item>
        <Form.Item name="inbox_days" label={t('notify.inboxDays')} rules={[{ required: true }]}><InputNumber min={1} max={3650} /></Form.Item>
      </Form>
    </Modal>
    <Modal open={channel !== undefined} title={t(channel ? 'notify.editChannel' : 'notify.addChannel')} onCancel={() => setChannel(undefined)} onOk={saveChannel} confirmLoading={busy} destroyOnHidden>
      <Form form={channelForm} layout="vertical">
        <Form.Item name="label" label={t('notify.label')} rules={[{ required: true, whitespace: true, max: 120 }]}><Input /></Form.Item>
        <Form.Item name="adapter" label={t('notify.adapter')} rules={[{ required: true }]}><Select disabled={!!channel} options={['email', 'slack', 'webhook', 'inbox'].map(value => ({ value, label: t(`notify.adapter.${value}`) }))} /></Form.Item>
        {selectedAdapter === 'email' && channel?.id !== 'legacy:smtp' && <Form.Item name="recipients" label={t('notify.recipients')} extra={t('notify.recipientHelp')}><Input /></Form.Item>}
        {['slack', 'webhook'].includes(selectedAdapter) && <><Alert type="info" title={t('notify.secretHelp')} /><Form.Item name="url" label={t('notify.url')}><Input.Password autoComplete="new-password" /></Form.Item><Form.Item name="clear_destination" valuePropName="checked"><Checkbox>{t('notify.clearDestination')}</Checkbox></Form.Item></>}
        {selectedAdapter === 'webhook' && <><Form.Item name="signing_secret" label={t('notify.signingSecret')}><Input.Password autoComplete="new-password" /></Form.Item><Form.Item name="clear_signing_secret" valuePropName="checked"><Checkbox>{t('notify.clearSigning')}</Checkbox></Form.Item></>}
        <Form.Item name="enabled" label={t('notify.enabled')} valuePropName="checked"><Switch /></Form.Item><Form.Item name="paused" label={t('notify.paused')} valuePropName="checked"><Switch /></Form.Item>
      </Form>
    </Modal>
    <Modal open={rule !== undefined} title={t(rule ? 'notify.editRule' : 'notify.addRule')} onCancel={() => setRule(undefined)} onOk={saveRule} confirmLoading={busy} destroyOnHidden>
      <Form form={ruleForm} layout="vertical">
        <Form.Item name="label" label={t('notify.label')} rules={[{ required: true, whitespace: true, max: 120 }]}><Input /></Form.Item>
        <Form.Item name="channel_id" label={t('notify.channels')} rules={[{ required: true }]}><Select options={channels.filter(c => !c.id.startsWith('legacy:')).map(c => ({ value: c.id, label: c.label }))} /></Form.Item>
        <Form.Item name="events" label={t('notify.events')} rules={[{ required: true }]}><Select mode="multiple" options={data?.events.map(value => ({ value, label: eventLabel(value) }))} /></Form.Item>
        <Form.Item name="recipient_mode" label={t('notify.audience')} rules={[{ required: true }]}><Select options={['grant', 'creator', 'destination'].map(value => ({ value, label: t(`notify.mode.${value}`) }))} /></Form.Item>
        {recipientMode === 'grant' && <Form.Item name="grants" label={t('notify.grants')} extra={t('notify.emptyGrants')}><Select mode="multiple" optionFilterProp="label" options={data?.principals.map(p => ({ value: p.principal, label: `${t(`notify.principal.${p.kind}`)}: ${p.label}` }))} /></Form.Item>}
        {recipientMode !== 'destination' && <Form.Item name="mandatory" label={t('notify.mandatory')} valuePropName="checked"><Switch /></Form.Item>}
        <Form.Item name="enabled" label={t('notify.enabled')} valuePropName="checked"><Switch /></Form.Item>
      </Form>
    </Modal>
    <Modal open={!!testChannel} title={t('notify.test')} onCancel={() => setTestChannel(undefined)} confirmLoading={busy} onOk={() => testChannel && run(async () => { await notifications.test(testChannel.id, { revision: testChannel.revision, to: testTo || undefined }); message.success(t('notify.testAccepted')); setTestChannel(undefined) })}>
      <Typography.Paragraph>{t('notify.testHelp', { channel: testChannel?.label })}</Typography.Paragraph>
      {testChannel?.adapter === 'email' && <Input aria-label={t('notify.testTo')} placeholder={t('notify.testTo')} value={testTo} onChange={e => setTestTo(e.target.value)} />}
    </Modal>
    <Modal open={resume} title={t('notify.resume')} onCancel={() => setResume(false)} confirmLoading={busy} onOk={() => run(async () => { await notifications.dispatch(false, data?.review_token); setResume(false) })}>
      <Alert type="warning" showIcon title={t('notify.resumeHelp')} /><Typography.Paragraph>{t('notify.queueCounts', data?.counts || {})}</Typography.Paragraph>
    </Modal>
    <Drawer open={!!detail} size={640} title={t('notify.details')} onClose={() => setDetail(undefined)}>
      {detail && <Space orientation="vertical" style={{ width: '100%' }}>
        <Typography.Text copyable>{detail.delivery.id}</Typography.Text><Tag>{stateLabel(detail.delivery.state)}</Tag><Typography.Text>{detail.delivery.error_code}</Typography.Text>
        <Typography.Title level={5}>{detail.preview.subject}</Typography.Title><Typography.Paragraph style={{ whiteSpace: 'pre-wrap' }}>{detail.preview.text}</Typography.Paragraph>
        <Table rowKey="id" size="small" dataSource={detail.attempts} columns={[{ title: t('notify.occurredAt'), dataIndex: 'started_at' }, { title: t('notify.status'), dataIndex: 'outcome', render: stateLabel }, { title: t('notify.error'), dataIndex: 'error_code' }]} />
        <Space>{!['pending', 'leased', 'retry_wait'].includes(detail.delivery.state) && <Button onClick={() => { setAction('replay'); setReason(''); setConfirmed(false); setRequestID(crypto.randomUUID()) }}>{t('notify.replay')}</Button>}{detail.delivery.state === 'uncertain' && <Button onClick={() => { setAction('review'); setReason('') }}>{t('notify.dismissUncertain')}</Button>}</Space>
      </Space>}
    </Drawer>
    <Modal open={!!action} title={t(action === 'replay' ? 'notify.replay' : 'notify.dismissUncertain')} onCancel={() => setAction(undefined)} onOk={finishAction} confirmLoading={busy} okButtonProps={{ disabled: !reason.trim() || (action === 'replay' && !!needsConfirmation && !confirmed) }}>
      <Alert type="warning" title={t('notify.replayHelp')} /><Input.TextArea aria-label={t('notify.reason')} placeholder={t('notify.reason')} maxLength={500} value={reason} onChange={e => setReason(e.target.value)} style={{ marginTop: 16 }} />
      {action === 'replay' && needsConfirmation && <Checkbox checked={confirmed} onChange={e => setConfirmed(e.target.checked)}>{t('notify.confirmDuplicate')}</Checkbox>}
    </Modal>
  </LoadGate>
}
