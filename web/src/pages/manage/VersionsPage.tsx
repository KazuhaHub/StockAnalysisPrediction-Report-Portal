import { useEffect, useState } from 'react'
import { Alert, App, Button, ConfigProvider, Drawer, Form, Input, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import LoadGate from '../../components/LoadGate'
import SettingsSaveBar from './SettingsSaveBar'
import { usePendingSettings } from './AdminDraftBoundary'
import { PolicyPage } from './OUPolicyWorkspace'

interface VersionRow {
  name: string
  label: string
  ord: number
  visibility: 'owner' | 'group' | 'all'
  grants: string[]
  is_default: boolean
  reports: number
}
interface Principal { principal: string; name: string; restricted?: boolean; display?: string }
const VISIBILITY = { owner: 'versions.visOwner', group: 'versions.visGroup', all: 'versions.visAll' } as const

// Grants decide who may read an edition; visibility decides whose reports those readers see.
// Both are edited as one explicit draft so moving focus cannot publish an access change.
export default function VersionsPage() {
  return <PolicyPage section="access"><VersionRegistry /></PolicyPage>
}

function VersionRegistry() {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
  const [versions, setVersions] = useState<VersionRow[]>([])
  const [groups, setGroups] = useState<Principal[]>([])
  const [users, setUsers] = useState<Principal[]>([])
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [query, setQuery] = useState('')
  const [original, setOriginal] = useState<VersionRow | null>(null)
  const [draft, setDraft] = useState<VersionRow | null>(null)
  const [adding, setAdding] = useState(false)
  const [saving, setSaving] = useState(false)
  const [nameError, setNameError] = useState('')
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(original)
  usePendingSettings(dirty, saving)

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return api.get<{ versions: (Omit<VersionRow, 'grants'> & { grants: string[] | null })[]; groups: Principal[]; users: Principal[] }>('/api/admin/versions')
      .then((result) => {
        setVersions((result.versions ?? []).map((version) => ({ ...version, grants: version.grants ?? [] })))
        setGroups(result.groups ?? [])
        setUsers(result.users ?? [])
        setLoaded(true)
      })
      .catch((error) => setLoadErr(errText(error, t)))
      .finally(() => setLoading(false))
  }
  useEffect(() => { void load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const open = (version?: VersionRow) => {
    const value: VersionRow = version ? { ...version, grants: [...version.grants] } : {
      name: '', label: '', ord: versions.length, visibility: 'owner', grants: [], is_default: false, reports: 0,
    }
    setOriginal(value)
    setDraft(value)
    setAdding(!version)
    setNameError('')
  }
  const close = () => {
    if (saving) return
    if (!dirty) { setDraft(null); return }
    modal.confirm({ title: t('manage.leaveTitle'), content: t('manage.leaveDescription'),
      okText: t('manage.leave'), cancelText: t('manage.stay'), okButtonProps: { danger: true },
      onOk: () => setDraft(null) })
  }
  const save = async () => {
    if (!draft) return
    const name = draft.name.trim()
    if (!name) { setNameError(t('versions.nameRequired')); return }
    if (adding && versions.some((version) => version.name === name)) {
      setNameError(t('versions.nameExists'))
      return
    }
    setSaving(true)
    try {
      await api.post('/api/admin/versions', { name, label: draft.label, ord: draft.ord,
        visibility: draft.visibility, grants: draft.grants })
      message.success(t('versions.saved'))
      setDraft(null)
      await load()
    } catch (error) { message.error(errText(error, t)) }
    finally { setSaving(false) }
  }
  const remove = async (name: string) => {
    try { await api.del(`/api/admin/versions/${encodeURIComponent(name)}`); await load() }
    catch (error) { message.error(errText(error, t)) }
  }
  const patch = (changes: Partial<VersionRow>) => setDraft((value) => value ? { ...value, ...changes } : value)
  const principalOptions = [
    { label: t('versions.principalGroups'), options: groups.map((group) => ({ value: group.principal,
      label: group.restricted ? `${group.name} · ${t('versions.restrictedTag')}` : group.name })) },
    { label: t('versions.principalUsers'), options: users.map((user) => ({ value: user.principal,
      label: user.display && user.display !== user.name ? `${user.display} (${user.name})` : user.name })) },
  ]
  const names = new Map([...groups, ...users].map((principal) => [principal.principal, principal.display || principal.name]))
  const filtered = versions.filter((version) => [version.name, version.label].join(' ').toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()))

  return (
    <LoadGate loading={loading && !loaded} error={!loaded ? loadErr : undefined} onRetry={load}>
      <div className="rp-admin-list-page">
        {loaded && loadErr && <Alert type="error" title={loadErr} showIcon />}
        <Typography.Paragraph type="secondary">{t('versions.desc')}</Typography.Paragraph>
        <div className="rp-admin-list-toolbar">
          <Input allowClear prefix={<SearchOutlined />} aria-label={t('versions.search')} placeholder={t('versions.search')}
            value={query} onChange={(event) => setQuery(event.target.value)} style={{ maxWidth: 360 }} />
          <Button type="primary" icon={<PlusOutlined />} aria-label={t('versions.add')} onClick={() => open()}>{t('versions.add')}</Button>
        </div>
        <Table<VersionRow> rowKey="name" dataSource={filtered} loading={loading} scroll={{ x: 700 }}
          pagination={{ pageSize: 12, hideOnSinglePage: true, showSizeChanger: false }} columns={[
            { title: t('versions.name'), render: (_, version) => <Space orientation="vertical" size={2}>
              <Space wrap size={4}><Typography.Text strong>{version.label || version.name}</Typography.Text>
                {version.is_default && <Tag>{t('versions.default')}</Tag>}</Space>
              <Typography.Text type="secondary" code>{version.name}</Typography.Text>
            </Space> },
            { title: t('versions.visibility'), dataIndex: 'visibility', render: (value: VersionRow['visibility']) => t(VISIBILITY[value] ?? value) },
            { title: t('versions.grants'), render: (_, version) => <Typography.Text type={version.grants.length ? undefined : 'secondary'}>
              {version.grants.map((principal) => names.get(principal) ?? principal).join(', ') || t('versions.noGrants')}
            </Typography.Text> },
            { title: t('versions.reportCount'), dataIndex: 'reports', width: 90 },
            { title: '', width: 100, align: 'right', render: (_, version) => <Space size={4}>
              <Button size="small" icon={<EditOutlined />} aria-label={t('common.edit')} onClick={() => open(version)} />
              {!version.is_default && <Popconfirm title={t('versions.deleteConfirm')} onConfirm={() => remove(version.name)}>
                <Button size="small" danger icon={<DeleteOutlined />} aria-label={t('common.delete')} />
              </Popconfirm>}
            </Space> },
          ]} />
      </div>
      <Drawer open={draft !== null} title={t(adding ? 'versions.add' : 'versions.title')} onClose={close} size={560}
        className="rp-admin-editor-drawer" destroyOnHidden maskClosable={!saving} closable={!saving}
        footer={<SettingsSaveBar dirty={dirty} saving={saving} onSave={save}
          onDiscard={() => { setDraft(original); setNameError('') }} />}>
        {draft && <ConfigProvider componentDisabled={saving}>
          <Form layout="vertical">
            {draft.is_default && <Alert style={{ marginBottom: 16 }} type="info" showIcon title={t('versions.defaultHint')} />}
            <Form.Item label={t('versions.name')} validateStatus={nameError ? 'error' : undefined} help={nameError || undefined} required>
              <Input aria-label={t('versions.name')} value={draft.name} disabled={!adding || saving}
                onChange={(event) => { patch({ name: event.target.value }); setNameError('') }} />
            </Form.Item>
            <Form.Item label={t('versions.label')}>
              <Input aria-label={t('versions.label')} value={draft.label} onChange={(event) => patch({ label: event.target.value })} />
            </Form.Item>
            <Form.Item label={t('versions.visibility')}>
              <Select aria-label={t('versions.visibility')} value={draft.visibility} onChange={(value) => patch({ visibility: value })}
                options={Object.entries(VISIBILITY).map(([value, label]) => ({ value, label: t(label) }))} />
            </Form.Item>
            <Form.Item label={t('versions.grants')} extra={t('versions.grantsHint')}>
              <Select aria-label={t('versions.grants')} mode="multiple" allowClear value={draft.grants}
                onChange={(value) => patch({ grants: value })} options={principalOptions} optionFilterProp="label" />
            </Form.Item>
          </Form>
        </ConfigProvider>}
      </Drawer>
    </LoadGate>
  )
}
