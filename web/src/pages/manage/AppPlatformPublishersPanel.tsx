import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Checkbox, Descriptions, Empty, Form, Input, Modal, Select, Space, Spin, Switch, Table, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import { useAuth } from '../../auth'
import { publisherFingerprint, publisherPublicBytes, type PublisherDirectory, type PublisherKey, type PublisherKeyInput, type PublisherMutation, type PublisherNamespace, type TrustedPublisher } from '../../lib/publisherTrust'

const root = '/api/admin/app-platform'
type Editor = { kind: 'register' | 'policy' | 'add_key'; publisher?: TrustedPublisher }
type Draft = { publisher_id: string; enabled: boolean; namespaces: PublisherNamespace[]; key_id: string; public_key: string; not_before_ms: string; not_after_ms: string }
type Pending = { request: PublisherMutation; fingerprint?: string; existing?: TrustedPublisher }
const slug = /^[a-z][a-z0-9-]{0,63}$/
function counter(value: string, zero: boolean) {
  return /^(0|[1-9][0-9]*)$/.test(value) && value.length <= 19 && BigInt(value) <= 9223372036854775807n && (zero || value !== '0')
}

export default function AppPlatformPublishersPanel() {
  const { t } = useTranslation()
  const { user } = useAuth()
  const [directory, setDirectory] = useState<PublisherDirectory | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)
  const [editor, setEditor] = useState<Editor | null>(null)
  const [pending, setPending] = useState<Pending | null>(null)
  const [acknowledged, setAcknowledged] = useState(false)
  const [form] = Form.useForm<Draft>()
  const owner = useRef(user)
  owner.current = user
  const translate = useRef(t)
  translate.current = t
  const lifecycle = useRef<object>({})
  const active = useRef(false)
  const mounted = useRef(false)
  const live = (account: string | null, epoch: object) => mounted.current && owner.current === account && lifecycle.current === epoch

  useEffect(() => {
    mounted.current = true; lifecycle.current = {}; active.current = false
    setDirectory(null); setEditor(null); setPending(null); setBusy(false); setDirty(false); setError('')
    return () => { mounted.current = false; lifecycle.current = {} }
  }, [user])

  useEffect(() => {
    const abort = new AbortController()
    setLoading(true)
    api.get<PublisherDirectory>(`${root}/publishers`, { signal: abort.signal }).then(value => {
      if (abort.signal.aborted) return
      setDirectory(value); setDirty(false); setError('')
    }).catch(e => { if (!abort.signal.aborted) { setDirectory(null); setError(errText(e, translate.current)) } })
      .finally(() => { if (!abort.signal.aborted) setLoading(false) })
    return () => abort.abort()
  }, [user, reload])

  const writable = !!directory?.initialized && !directory.paused && !loading && !busy && !dirty
  const openEditor = (kind: Editor['kind'], publisher?: TrustedPublisher) => {
    if (!writable) return
    form.resetFields()
    form.setFieldsValue({ publisher_id: publisher?.id ?? '', enabled: kind === 'register' ? false : publisher?.enabled ?? false, namespaces: publisher?.namespaces.map(n => ({ ...n })) ?? [{ kind: 'prefix', value: '' }], key_id: '', public_key: '', not_before_ms: '0', not_after_ms: '' })
    setEditor({ kind, publisher }); setError('')
  }

  const review = (value: Pending) => { setAcknowledged(false); setPending(value) }
  const previewDraft = async (draft: Draft) => {
    if (!editor || !writable || active.current) return
    const account = user, epoch = lifecycle.current
    active.current = true; setBusy(true)
    try {
      let request: PublisherMutation
      let fingerprint: string | undefined
      if (editor.kind !== 'add_key') {
        if (!Array.isArray(draft.namespaces) || draft.namespaces.length > 64 || draft.namespaces.some(n => n.kind === 'exact' ? !slug.test(n.value) : n.kind !== 'prefix' || !n.value.endsWith('-') || n.value.length > 64 || !slug.test(n.value.slice(0, -1)))) throw new Error(t('appTrust.invalidNamespace'))
      }
      let key: PublisherKeyInput | undefined
      if (editor.kind !== 'policy') {
        if (!publisherPublicBytes(draft.public_key) || !/^[a-z0-9][a-z0-9._-]{0,127}$/.test(draft.key_id) || !counter(draft.not_before_ms, true) || (draft.not_after_ms !== '' && (!counter(draft.not_after_ms, false) || BigInt(draft.not_after_ms) <= BigInt(draft.not_before_ms)))) throw new Error(t('appTrust.invalidKey'))
        key = { id: draft.key_id, public_key: draft.public_key, not_before_ms: draft.not_before_ms, not_after_ms: draft.not_after_ms || null }
        fingerprint = await publisherFingerprint(draft.public_key)
      }
      if (editor.kind === 'register') {
        if (!slug.test(draft.publisher_id)) throw new Error(t('appTrust.invalidPublisher'))
        request = { action: 'register', publisher_id: draft.publisher_id, enabled: draft.enabled, namespaces: draft.namespaces.map(n => ({ ...n })), key: key! }
      } else if (editor.kind === 'policy') {
        request = { action: 'policy', publisher_id: editor.publisher!.id, expected_revision: editor.publisher!.revision, enabled: draft.enabled, namespaces: draft.namespaces.map(n => ({ ...n })) }
      } else {
        request = { action: 'add_key', publisher_id: editor.publisher!.id, expected_revision: editor.publisher!.revision, key: key! }
      }
      if (live(account, epoch)) { setError(''); review({ request, fingerprint, existing: editor.publisher }) }
    } catch (e) { if (live(account, epoch)) setError(errText(e, t)) }
    finally { if (live(account, epoch)) { active.current = false; setBusy(false) } }
  }

  const reviewRevoke = async (publisher: TrustedPublisher, key: PublisherKey) => {
    if (!writable || active.current) return
    const account = user, epoch = lifecycle.current
    active.current = true; setBusy(true)
    try {
      const fingerprint = await publisherFingerprint(key.public_key)
      if (live(account, epoch)) review({ request: { action: 'revoke_key', publisher_id: publisher.id, expected_revision: publisher.revision, key_id: key.id, expected_key_revision: key.revision }, fingerprint, existing: publisher })
    } catch (e) { if (live(account, epoch)) setError(errText(e, t)) }
    finally { if (live(account, epoch)) { active.current = false; setBusy(false) } }
  }

  const confirm = async () => {
    if (!pending || !acknowledged || active.current || busy || loading || dirty || !directory || directory.paused || (pending.request.action !== 'initialize' && !directory.initialized)) return
    const account = user, epoch = lifecycle.current, request = pending.request
    active.current = true; setBusy(true)
    try {
      await api.post(request.action === 'initialize' ? `${root}/control/initialize` : `${root}/publishers`, request)
      if (!live(account, epoch)) return
      setPending(null); setEditor(null); setDirectory(null); setReload(v => v + 1)
    } catch (e) {
      if (live(account, epoch)) { setError(errText(e, t)); setDirty(true); setPending(null); setEditor(null) }
    } finally { if (live(account, epoch)) { active.current = false; setBusy(false) } }
  }

  const namespaces = (values: PublisherNamespace[]) => values.map(n => `${t(`appTrust.namespace.${n.kind}`)}: ${n.value}`).join('、') || t('appTrust.noNamespace')
  const mutation = pending?.request
  const changedKey = mutation && 'key' in mutation ? mutation.key : undefined
  return <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
    <Alert type="info" showIcon title={t('appTrust.title')} description={t('appTrust.explanation')} />
    <Space><Button disabled={busy} onClick={() => { setPending(null); setEditor(null); setReload(v => v + 1) }}>{t('appTrust.refresh')}</Button>
      {directory && !directory.initialized && !directory.paused && <Button disabled={loading || busy || dirty} onClick={() => review({ request: { action: 'initialize' } })}>{t('appTrust.initialize')}</Button>}
      {directory?.initialized && <Button disabled={!writable} onClick={() => openEditor('register')}>{t('appTrust.register')}</Button>}
    </Space>
    {error && <Alert type="error" showIcon title={error} />}
    {dirty && <Alert type="warning" showIcon title={t('appTrust.reloadRequired')} />}
    {directory?.paused && <Alert type="warning" showIcon title={t('appTrust.paused')} />}
    {directory && !directory.initialized && !directory.paused && <Alert type="info" title={t('appTrust.fresh')} />}
    <Spin spinning={loading}><Table<TrustedPublisher> rowKey="id" dataSource={directory?.publishers ?? []} pagination={false} locale={{ emptyText: <Empty description={error ? t('appTrust.loadFailed') : t('appTrust.empty')} /> }} columns={[
      { title: t('appTrust.publisher'), render: (_, publisher) => <Space orientation="vertical"><Typography.Text strong>{publisher.id}</Typography.Text><Tag>{t(publisher.enabled ? 'appTrust.enabled' : 'appTrust.disabled')}</Tag><Typography.Text type="secondary">{t('appTrust.revision')}: {publisher.revision}</Typography.Text><Typography.Text>{namespaces(publisher.namespaces)}</Typography.Text></Space> },
      { title: t('appTrust.publicKeys'), render: (_, publisher) => <Space orientation="vertical" style={{ width: '100%' }}>{(publisher.keys ?? []).map(key => <div key={key.id}><Typography.Text strong>{key.id}</Typography.Text>{key.revoked_ms !== null && <Tag>{t('appTrust.revoked')}</Tag>}<Typography.Paragraph code style={{ overflowWrap: 'anywhere', marginBottom: 4 }}>{key.public_key}</Typography.Paragraph><Typography.Text type="secondary">{t('appTrust.validFrom')}: {key.not_before_ms} · {t('appTrust.validUntil')}: {key.not_after_ms ?? t('appTrust.noExpiry')}</Typography.Text><div><Button size="small" danger disabled={!writable || key.revoked_ms !== null} onClick={() => void reviewRevoke(publisher, key)}>{t('appTrust.revoke')}</Button></div></div>)}</Space> },
      { title: t('appPlatform.action'), render: (_, publisher) => <Space wrap><Button disabled={!writable} onClick={() => openEditor('policy', publisher)}>{t('appTrust.editPolicy')}</Button><Button disabled={!writable} onClick={() => openEditor('add_key', publisher)}>{t('appTrust.addKey')}</Button><Button disabled={!writable} onClick={() => review({ request: { action: 'policy', publisher_id: publisher.id, expected_revision: publisher.revision, enabled: !publisher.enabled, namespaces: publisher.namespaces.map(n => ({ ...n })) }, existing: publisher })}>{t(publisher.enabled ? 'appTrust.disable' : 'appTrust.enable')}</Button></Space> },
    ]} /></Spin>
    <Modal open={!!editor && !pending} title={t(editor?.kind === 'register' ? 'appTrust.register' : editor?.kind === 'policy' ? 'appTrust.editPolicy' : 'appTrust.addKey')} footer={null} closable={!busy} mask={{ closable: false }} onCancel={() => { if (!busy) setEditor(null) }}>
      {error && <Alert type="error" title={error} />}
      <Form form={form} layout="vertical" onFinish={values => void previewDraft(values)} disabled={busy}>
        <Form.Item name="publisher_id" label={t('appTrust.publisher')} rules={[{ required: true }]}><Input disabled={editor?.kind !== 'register'} maxLength={64} /></Form.Item>
        {editor?.kind !== 'add_key' && <><Form.Item name="enabled" label={t('appTrust.enabled')} valuePropName="checked"><Switch /></Form.Item><Typography.Paragraph>{t('appTrust.namespaceHint')}</Typography.Paragraph><Form.List name="namespaces">{(fields, { add, remove }) => <Space orientation="vertical" style={{ width: '100%' }}>{fields.map(field => <Space key={field.key} align="start"><Form.Item name={[field.name, 'kind']} rules={[{ required: true }]}><Select aria-label={t('appTrust.namespaceKind')} options={[{ value: 'exact', label: t('appTrust.namespace.exact') }, { value: 'prefix', label: t('appTrust.namespace.prefix') }]} style={{ width: 130 }} /></Form.Item><Form.Item name={[field.name, 'value']} rules={[{ required: true }]}><Input aria-label={t('appTrust.namespaceValue')} maxLength={64} /></Form.Item><Button onClick={() => remove(field.name)}>{t('appTrust.remove')}</Button></Space>)}<Button disabled={fields.length >= 64} onClick={() => add({ kind: 'exact', value: '' })}>{t('appTrust.addNamespace')}</Button></Space>}</Form.List></>}
        {editor?.kind !== 'policy' && <><Alert type="warning" showIcon title={t('appTrust.publicOnly')} /><Form.Item name="key_id" label={t('appTrust.keyID')} rules={[{ required: true }]}><Input maxLength={128} /></Form.Item><Form.Item name="public_key" label={t('appTrust.publicKey')} rules={[{ required: true }]}><Input.TextArea rows={2} maxLength={128} /></Form.Item><Form.Item name="not_before_ms" label={t('appTrust.validFrom')} rules={[{ required: true }]}><Input /></Form.Item><Form.Item name="not_after_ms" label={t('appTrust.validUntil')} extra={t('appTrust.timeHint')}><Input /></Form.Item></>}
        <Space style={{ marginTop: 16 }}><Button onClick={() => setEditor(null)}>{t('appTrust.cancel')}</Button><Button type="primary" htmlType="submit" loading={busy}>{t('appTrust.review')}</Button></Space>
      </Form>
    </Modal>
    <Modal open={!!pending} title={t('appTrust.reviewTitle')} okText={t('appTrust.confirm')} cancelText={t('appTrust.cancel')} confirmLoading={busy} okButtonProps={{ disabled: !acknowledged }} cancelButtonProps={{ disabled: busy }} closable={!busy} mask={{ closable: false }} onCancel={() => { if (!busy) setPending(null) }} onOk={() => void confirm()}>
      {mutation && <Space orientation="vertical" style={{ width: '100%' }}><Alert type="warning" title={t(mutation.action === 'initialize' ? 'appTrust.initializeHint' : 'appTrust.changeHint')} /><Descriptions column={1} items={[
        { key: 'action', label: t('appPlatform.action'), children: t(`appTrust.action.${mutation.action}`) },
        ...('publisher_id' in mutation ? [{ key: 'publisher', label: t('appTrust.publisher'), children: mutation.publisher_id }] : []),
        ...('expected_revision' in mutation ? [{ key: 'revision', label: t('appTrust.revision'), children: mutation.expected_revision }] : []),
        ...('enabled' in mutation ? [{ key: 'enabled', label: t('appTrust.enabled'), children: t(mutation.enabled ? 'appTrust.enabled' : 'appTrust.disabled') }] : []),
        ...('namespaces' in mutation ? [{ key: 'old', label: t('appTrust.previousNamespaces'), children: namespaces(pending?.existing?.namespaces ?? []) }, { key: 'new', label: t('appTrust.namespaces'), children: namespaces(mutation.namespaces) }] : []),
        ...(changedKey ? [{ key: 'key', label: t('appTrust.keyID'), children: changedKey.id }, { key: 'public', label: t('appTrust.publicKey'), children: <Typography.Text code style={{ overflowWrap: 'anywhere' }}>{changedKey.public_key}</Typography.Text> }, { key: 'from', label: t('appTrust.validFrom'), children: changedKey.not_before_ms }, { key: 'until', label: t('appTrust.validUntil'), children: changedKey.not_after_ms ?? t('appTrust.noExpiry') }] : []),
        ...(mutation.action === 'revoke_key' ? [{ key: 'key', label: t('appTrust.keyID'), children: `${mutation.key_id} · ${mutation.expected_key_revision}` }] : []),
        ...(pending.fingerprint ? [{ key: 'fingerprint', label: 'SHA-256', children: <Typography.Text code style={{ overflowWrap: 'anywhere' }}>{pending.fingerprint}</Typography.Text> }] : []),
      ]} /><Checkbox checked={acknowledged} disabled={busy} onChange={e => setAcknowledged(e.target.checked)}>{t('appTrust.acknowledge')}</Checkbox></Space>}
    </Modal>
  </Space>
}
