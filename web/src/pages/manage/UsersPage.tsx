import { useEffect, useMemo, useState } from 'react'
import {
  App,
  Button,
  Card,
  DatePicker,
  Dropdown,
  Empty,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import dayjs from 'dayjs'
import type { ColumnsType } from 'antd/es/table'
import { useTableView } from '../../components/TableView'
import {
  DeleteOutlined,
  EditOutlined,
  KeyOutlined,
  PlusOutlined,
  SearchOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { api, errText } from '../../api/client'
import OrgUnitPicker, { subtreeOf } from './OrgUnitPicker'
import ScopeFold from './ScopeFold'
import UserAvatar from '../../components/UserAvatar'
import OrgUnitDetail from './OrgUnitDetail'
import { usePendingSettings } from './AdminDraftBoundary'
import type { Role, UserGroupRow, UserRow, UsersResp } from '../../api/types'

const ROLE_COLOR: Record<string, string> = { admin: 'gold', operator: 'blue', user: 'default' }

export default function UsersPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [data, setData] = useState<UsersResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [settled, setSettled] = useState(false) // the first /api/admin/users call has come back, one way or another
  const [search, setSearch] = useState('')
  const [roleFilter, setRoleFilter] = useState<string>()
  // OU scoping (the tree picker). `ouScoped` is the all-vs-selected radio; `ouSelected` is what the
  // tree has highlighted. Kept apart so clearing the selection does not silently widen the scope
  // back to every account, and so the radio survives a search that hides the selected node.
  const [ouScoped, setOuScoped] = useState(false)
  const [ouSelected, setOuSelected] = useState<number[]>([])
  // A link can land on the groups tab (?tab=groups) and on one unit in it (?ou=<id>), which is how
  // the forecast access summary points at the OU editor.
  const [linked] = useSearchParams()
  const [tab, setTab] = useState(linked.get('tab') === 'groups' ? 'groups' : 'accounts')
  const [selected, setSelected] = useState<string[]>([])
  const [editUser, setEditUser] = useState<UserRow | 'new' | null>(null)
  const [pwUser, setPwUser] = useState<string | null>(null)
  const [form] = Form.useForm()
  const [pwForm] = Form.useForm()

  const load = () =>
    api
      .get<UsersResp>('/api/admin/users')
      .then((d) => {
        setData(d)
        setSelected((sel) => sel.filter((u) => d.users.some((x) => x.username === u)))
      })
      // A failed load leaves `data` null for good, and the OU tree beside the table reads that as
      // "still loading". Settling it here means the tree falls back to its empty text next to a
      // table that is also visibly empty — one page in one state, rather than half of it spinning
      // for ever on an answer that is not coming.
      .catch(() => {})
      .finally(() => {
        setLoading(false)
        setSettled(true)
      })
  useEffect(() => {
    load()
  }, [])

  const roles: Role[] = data?.roles || []
  const groups: UserGroupRow[] = data?.groups || []
  const roleName = (code: string) => roles.find((r) => r.code === code)?.name || code
  const groupById = useMemo(() => new Map(groups.map((g) => [g.id, g])), [groups])
  const defaultGroup = useMemo(() => groups.find((g) => g.is_default), [groups])
  const adminCount = useMemo(() => (data?.users || []).filter((u) => u.role === 'admin').length, [data])

  // A user's effective group is their primary group, or the Default when unassigned.
  const primaryOf = (u: UserRow) => (u.primary_group ? groupById.get(u.primary_group) : undefined)

  // null = no OU filter at all. Selecting nothing while scoped means nothing matches, which is the
  // honest reading of "only the selected units" — silently showing everything would hide the state.
  const ouScope = useMemo(
    () => (ouScoped ? subtreeOf(groups, ouSelected) : null),
    [ouScoped, ouSelected, groups],
  )

  // Delete the OU you are scoped to and the list would go permanently empty with nothing
  // highlighted and nothing on screen explaining why. Prune the selection when the tree changes,
  // and fall back to unscoped once it is empty rather than leaving a filter that matches nobody.
  useEffect(() => {
    if (groups.length === 0) return // not loaded yet; pruning here would clear a valid selection
    setOuSelected((sel) => {
      const kept = sel.filter((id) => groups.some((g) => g.id === id))
      if (kept.length === sel.length) return sel
      if (kept.length === 0) setOuScoped(false)
      return kept
    })
  }, [groups])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (data?.users || []).filter((u) => {
      if (roleFilter && u.role !== roleFilter) return false
      if (ouScope) {
        // An account with no primary group belongs to the Default OU by inheritance, so the tree
        // has to match it the same way the rest of the product resolves it.
        const own = u.primary_group || defaultGroup?.id || 0
        if (!ouScope.has(own)) return false
      }
      if (q && ![u.username, u.display_name, u.email].some((v) => (v || '').toLowerCase().includes(q))) return false
      return true
    })
  }, [data, search, roleFilter, ouScope, defaultGroup])

  // Cut an account's identity-provider binding without deleting the account. A link outlives the
  // person's IdP account, and while it stands whoever the IdP later issues that subject to would
  // sign in as this one. The account returns to local and its sessions end.
  const unlink = async (name: string) => {
    try {
      await api.del(`/api/admin/users/${encodeURIComponent(name)}/identity`)
      message.success(t('users.unlinked'))
      load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const patch = async (name: string, body: Record<string, unknown>) => {
    await api.put(`/api/admin/users/${encodeURIComponent(name)}`, body)
    load()
  }
  const bulk = async (action: string, extra: Record<string, unknown> = {}) => {
    const r = await api.post<{ n: number }>('/api/admin/users/bulk', { action, usernames: selected, ...extra })
    message.success(t('users.bulkDone', { n: r.n }))
    setSelected([])
    load()
  }

  const openEdit = (u: UserRow | 'new') => {
    setEditUser(u)
    if (u === 'new') form.setFieldsValue({ username: '', display_name: '', email: '', role: 'user', primary_group: undefined, password: '', expires_at: null })
    else
      form.setFieldsValue({
        username: u.username,
        display_name: u.display_name,
        email: u.email,
        role: u.role,
        primary_group: u.primary_group || undefined,
        password: '',
        expires_at: u.expires_at ? dayjs(u.expires_at) : null,
      })
  }
  const saveEdit = async () => {
    const v = await form.validateFields()
    const primaryGroup = v.primary_group ?? 0
    const expiresAt = v.expires_at ? v.expires_at.format('YYYY-MM-DD') : ''
    try {
      if (editUser === 'new') {
        await api.post('/api/admin/users', {
          username: v.username,
          password: v.password,
          role: v.role,
          display_name: v.display_name || '',
          email: v.email || '',
          primary_group: primaryGroup,
          expires_at: expiresAt,
        })
      } else {
        await patch((editUser as UserRow).username, {
          role: v.role,
          display_name: v.display_name || '',
          email: v.email || '',
          primary_group: primaryGroup,
          expires_at: expiresAt,
          ...(v.password ? { password: v.password } : {}),
        })
      }
      setEditUser(null)
      message.success(t('common.saved'))
      load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }
  const resetPw = async () => {
    const v = await pwForm.validateFields()
    await patch(pwUser!, { password: v.password })
    setPwUser(null)
    pwForm.resetFields()
    message.success(t('common.saved'))
  }

  const cols: ColumnsType<UserRow> = [
    {
      title: t('users.user'),
      minWidth: 230,
      width: 300,
      dataIndex: 'username',
      render: (_, u) => (
        <div className="rp-user-identity">
          <UserAvatar name={u.display_name || u.username} seed={u.username} />
          <div className="rp-user-identity-text">
            <div className="rp-user-name">
              <Typography.Text strong>{u.display_name || u.username}</Typography.Text>
              {u.username === data?.me && <Tag color="green">{t('users.me')}</Tag>}
              {/* A federated account has no local password, so knowing which rows those are is the
                  difference between offering a password reset and offering to revoke a binding. */}
              {u.federated && (
                <Popconfirm
                  title={t('users.unlinkConfirm', { slug: u.sso_slug || 'SSO' })}
                  okText={t('users.unlink')}
                  cancelText={t('common.cancel')}
                  onConfirm={() => unlink(u.username)}
                >
                  <Tag color="geekblue" style={{ cursor: 'pointer' }}>
                    {t('users.federatedTag', { slug: u.sso_slug || 'SSO' })}
                  </Tag>
                </Popconfirm>
              )}
            </div>
            <div className="rp-user-secondary" title={`@${u.username}${u.email ? ` · ${u.email}` : ''}`}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                @{u.username}
                {u.email ? ` · ${u.email}` : ''}
              </Typography.Text>
            </div>
          </div>
        </div>
      ),
    },
    {
      title: t('users.role'),
      dataIndex: 'role',
      width: 90, minWidth: 80,
      render: (role: string) => <Tag color={ROLE_COLOR[role] ?? 'default'}>{roleName(role)}</Tag>,
    },
    {
      title: t('users.group'),
      dataIndex: 'primary_group',
      width: 150, minWidth: 120,
      render: (_, u) => {
        const g = primaryOf(u)
        if (g) return <Tag color="blue" title={g.name}>{g.name}</Tag>
        // No primary group → inherits the Default group.
        return (
          <Tag>
            {defaultGroup?.name || t('users.defaultGroupTag')} <span style={{ opacity: 0.6 }}>· {t('users.inheritedTag')}</span>
          </Tag>
        )
      },
    },
    {
      title: t('users.status'),
      dataIndex: 'active',
      width: 76, minWidth: 70,
      render: (active: boolean, u) => {
        const isLastAdmin = u.role === 'admin' && adminCount <= 1
        return (
          <Tooltip title={active ? t('users.active') : t('users.disabled')}>
            <Switch
              size="small"
              checked={active}
              disabled={u.username === data?.me || isLastAdmin}
              onChange={(checked) => patch(u.username, { active: checked })}
            />
          </Tooltip>
        )
      },
    },
    {
      title: t('users.expires'),
      dataIndex: 'expires_at',
      width: 110, minWidth: 100,
      render: (v: string) => {
        if (!v) return <Typography.Text type="secondary">{t('users.expiresNever')}</Typography.Text>
        const expired = dayjs(v).endOf('day').isBefore(dayjs())
        return (
          <Space size={4}>
            <Typography.Text style={{ fontSize: 12 }} type={expired ? 'danger' : undefined}>
              {v}
            </Typography.Text>
            {expired && <Tag color="red">{t('users.expired')}</Tag>}
          </Space>
        )
      },
    },
    {
      // Activity, not sign-in. "Signed in Monday and used it all week" and "signed in Monday and
      // never came back" are the two answers this column is consulted to tell apart, and showing
      // the login time made them identical. The login time is still a fact worth having, so it is
      // the tooltip rather than gone.
      title: t('users.lastSeen'),
      dataIndex: 'last_seen',
      width: 150, minWidth: 140,
      render: (v: string, u) =>
        v ? (
          <Tooltip title={t('users.lastLoginWas', { at: u.last_login || t('users.never') })}>
            <Typography.Text style={{ fontSize: 12 }}>{v}</Typography.Text>
          </Tooltip>
        ) : (
          <Typography.Text type="secondary">{t('users.never')}</Typography.Text>
        ),
    },
    {
      title: '',
      key: 'actions', minWidth: 96,
      width: 120,
      align: 'right',
      render: (_, u) => (
        <Space size={4}>
          <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(u)} aria-label={t('common.edit')} />
          <Button size="small" icon={<KeyOutlined />} onClick={() => setPwUser(u.username)} aria-label={t('users.newPassword')} />
          <Popconfirm title={t('common.deleteConfirm')} onConfirm={() => remove(u.username)} disabled={u.username === data?.me}>
            <Button size="small" danger icon={<DeleteOutlined />} disabled={u.username === data?.me} aria-label={t('common.delete')} />
          </Popconfirm>
        </Space>
      ),
    },
  ]
  const tableView = useTableView('users', cols, ['username', 'actions'])

  const remove = async (name: string) => {
    await api.del(`/api/admin/users/${encodeURIComponent(name)}`)
    load()
  }

  const accountList = (
    <Space orientation="vertical" size={16} style={{ width: '100%', minWidth: 0 }}>
      <Space wrap style={{ width: '100%', justifyContent: 'space-between' }}>
        <Space wrap>
          <Input
            allowClear
            prefix={<SearchOutlined />}
            placeholder={t('users.search')}
            style={{ width: 240 }}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <Select
            allowClear
            placeholder={t('users.role')}
            style={{ width: 130 }}
            value={roleFilter}
            onChange={setRoleFilter}
            options={roles.map((r) => ({ value: r.code, label: r.name }))}
          />
        </Space>
        <Space wrap>{tableView.controls}<Button type="primary" icon={<PlusOutlined />} onClick={() => openEdit('new')}>
          {t('users.add')}
        </Button></Space>
      </Space>

      {selected.length > 0 && (
        <Card size="small" style={{ background: 'var(--ant-color-fill-quaternary)' }}>
          <Space wrap>
            <Typography.Text strong>{t('users.selectedN', { n: selected.length })}</Typography.Text>
            <Button size="small" onClick={() => bulk('enable')}>
              {t('users.active')}
            </Button>
            <Button size="small" onClick={() => bulk('disable')}>
              {t('users.disabled')}
            </Button>
            <Dropdown
              menu={{ items: roles.map((r) => ({ key: r.code, label: r.name, onClick: () => bulk('set_role', { role: r.code }) })) }}
            >
              <Button size="small">{t('users.bulkSetRole')}</Button>
            </Dropdown>
            <Dropdown
              menu={{ items: groups.map((g) => ({ key: g.id, label: g.name, onClick: () => bulk('set_group', { group_id: g.id }) })) }}
              disabled={groups.length === 0}
            >
              <Button size="small">{t('users.bulkSetGroup')}</Button>
            </Dropdown>
            <Button size="small" onClick={() => bulk('clear_group')}>
              {t('users.bulkClearGroup')}
            </Button>
            <Popconfirm title={t('users.bulkDeleteConfirm', { n: selected.length })} onConfirm={() => bulk('delete')}>
              <Button size="small" danger>
                {t('common.delete')}
              </Button>
            </Popconfirm>
          </Space>
        </Card>
      )}

      <Table<UserRow>
        rowKey="username"
        loading={loading}
        dataSource={filtered}
        columns={tableView.columns}
        size="small"
        className={`${tableView.className} rp-users-table`}
        tableLayout="fixed"
        pagination={false}
        scroll={{ x: tableView.scroll.x + 32 }}
        rowSelection={{ selectedRowKeys: selected, onChange: (keys) => setSelected(keys as string[]) }}
      />

      {/* add / edit user */}
      <Modal
        open={editUser != null}
        title={editUser === 'new' ? t('users.add') : t('users.edit')}
        onOk={saveEdit}
        onCancel={() => setEditUser(null)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        destroyOnHidden
      >
        <Form form={form} layout="vertical">
          <Form.Item name="username" label={t('users.username')} rules={[{ required: true }]}>
            <Input autoComplete="off" disabled={editUser !== 'new'} />
          </Form.Item>
          <Form.Item name="display_name" label={t('users.displayName')}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="email" label={t('users.email')} rules={[{ type: 'email', message: t('users.emailInvalid') }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="password" label={editUser === 'new' ? t('users.password') : t('users.newPassword')} rules={[{ required: editUser === 'new', min: 12, message: t('reset.tooShort') }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="role" label={t('users.role')}>
            <Select options={roles.map((r) => ({ value: r.code, label: r.name }))} />
          </Form.Item>
          <Form.Item name="primary_group" label={t('users.group')} extra={t('users.primaryGroupHint')}>
            <Select
              allowClear
              placeholder={t('users.inheritDefault')}
              options={groups.map((g) => ({ value: g.id, label: g.is_default ? `${g.name} · ${t('users.defaultGroupTag')}` : g.name }))}
            />
          </Form.Item>
          <Form.Item name="expires_at" label={t('users.expires')} extra={t('users.expiresHint')}>
            <DatePicker style={{ width: '100%' }} placeholder={t('users.expiresNever')} />
          </Form.Item>
        </Form>
      </Modal>

      {/* reset password */}
      <Modal
        open={!!pwUser}
        title={`${t('users.newPassword')} — ${pwUser ?? ''}`}
        onOk={resetPw}
        onCancel={() => setPwUser(null)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        destroyOnHidden
      >
        <Form form={pwForm} layout="vertical">
          <Form.Item name="password" label={t('users.newPassword')} rules={[{ required: true, min: 12, message: t('reset.tooShort') }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )

  // Tree beside the list, the way an admin console scopes a directory: the OU hierarchy is visible
  // at all times, so inheritance stops being something you can only infer from an edit dialog.
  const accounts = (
    <div className="rp-users-directory">
      <ScopeFold label={ouScoped && ouSelected.length ? ouSelected.map((id) => groups.find((g) => g.id === id)?.name ?? id).join(', ') : t('ou.title')}>
        {(fold, folded) => <OrgUnitPicker
          collapsible={!folded}
          groups={groups}
          loading={!settled}
          unassigned={(data?.users || []).filter((u) => !u.primary_group).length}
          scoped={ouScoped}
          onScopedChange={setOuScoped}
          selected={ouSelected}
          onSelect={setOuSelected}
          onManage={() => setTab('groups')}
          onPick={fold}
        />}
      </ScopeFold>
      {accountList}
    </div>
  )

  return (
    <Tabs className="rp-users-page"
      activeKey={tab}
      onChange={setTab}
      items={[
        { key: 'accounts', label: t('users.tabAccounts'), children: accounts },
        { key: 'groups', label: t('users.tabGroups'), children: <GroupsPanel groups={groups} groupsLoading={!settled} onChanged={load} initialId={Number(linked.get('ou')) || null}
          onMembers={(id) => { setOuSelected([id]); setOuScoped(true); setTab('accounts') }} /> },
      ]}
    />
  )
}

// The directory owns OU identity, hierarchy and membership. Feature policies are edited on
// their respective settings pages, using the OU as the scope.
function GroupsPanel({
  groups,
  groupsLoading,
  onChanged,
  initialId,
  onMembers,
}: {
  groups: UserGroupRow[]
  groupsLoading: boolean
  onChanged: () => void
  initialId: number | null
  onMembers: (id: number) => void
}) {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
  // Which OU the detail pane is showing. Held as an id, not the row: the list is refetched after
  // every save, so a held object would go stale the moment it was edited.
  const [selId, setSelId] = useState<number | null>(initialId)
  const [adding, setAdding] = useState(false)
  const [newName, setNewName] = useState('')
  const [newParent, setNewParent] = useState(0)
  const [detailPending, setDetailPending] = useState({ dirty: false, saving: false })
  usePendingSettings(adding && !!newName.trim())

  const sel = groups.find((g) => g.id === selId) ?? null
  // Default to the root so the pane is never empty on arrival — an admin opening this tab wants to
  // see something, and the Default OU is the one every other setting falls back to.
  // Also when the held id names nothing (a stale link, or a unit deleted elsewhere).
  useEffect(() => {
    if (groups.length > 0 && (selId == null || !groups.some((g) => g.id === selId))) {
      setSelId((groups.find((g) => g.is_default) ?? groups[0]).id)
    }
  }, [groups, selId])

  const createOU = async () => {
    const name = newName.trim()
    if (!name) return
    try {
      await api.post('/api/admin/groups', {
        name, description: '', parent_id: newParent || groups.find((g) => g.is_default)?.id,
      })
      setAdding(false)
      setNewName('')
      setNewParent(0)
      onChanged()
    } catch (e) {
      message.error(errText(e, t))
    }
  }


  return (
    <div className="rp-ou-groups-page">
      <Typography.Paragraph type="secondary">{t('ou.directoryHint')}</Typography.Paragraph>
      <div className="rp-ou-console">
        <ScopeFold label={sel?.name ?? t('ou.title')}>
          {(fold) => <OrgUnitPicker
          mode="manage"
          groups={groups}
          loading={groupsLoading}
          unassigned={0}
          scoped
          onScopedChange={() => {}}
          selected={sel ? [sel.id] : []}
          onSelect={(ids) => {
            const next = ids[0] ?? null
            if (next === selId) return
            if (detailPending.saving) { message.info(t('manage.saveInProgress')); return }
            if (!detailPending.dirty) { setSelId(next); return }
            modal.confirm({ title: t('manage.leaveTitle'), content: t('manage.leaveDescription'),
              okText: t('manage.leave'), cancelText: t('manage.stay'), okButtonProps: { danger: true },
              onOk: () => setSelId(next) })
          }}
          onManage={() => {}}
          onAdd={() => setAdding(true)}
          onPick={fold}
        />
          }
        </ScopeFold>
        {sel ? (
          <OrgUnitDetail
            key={sel.id}
            group={sel}
            groups={groups}
            onChanged={onChanged}
            onPendingChange={setDetailPending}
            onMembers={() => onMembers(sel.id)}
            onDeleted={() => {
              setSelId(null)
              onChanged()
            }}
          />
        ) : (
          <Empty style={{ flex: 1, paddingTop: 48 }} description={t('ou.pickOne')} />
        )}
      </div>

      <Modal
        open={adding}
        title={t('ou.add')}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        onCancel={() => setAdding(false)}
        onOk={createOU}
        destroyOnHidden
      >
        {/* Creation asks only for a name and a place. Everything else has a sensible inherited
            value the moment it exists, and is changed in the pane where it can be seen. */}
        <Typography.Text strong>{t('users.groupName')}</Typography.Text>
        <Input autoFocus value={newName} onChange={(e) => setNewName(e.target.value)} style={{ marginBottom: 12 }} />
        <Typography.Text strong>{t('users.parentOu')}</Typography.Text>
        <Select
          style={{ width: '100%' }}
          value={newParent || groups.find((g) => g.is_default)?.id}
          onChange={setNewParent}
          options={groups.map((g) => ({ value: g.id, label: g.name }))}
        />
      </Modal>
    </div>
  )
}
