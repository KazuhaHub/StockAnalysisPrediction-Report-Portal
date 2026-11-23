import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, Modal, Select, Typography } from 'antd'
import { PlusOutlined, DeleteOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { UsersResp } from '../../api/types'
import CardTable, { type CardColumn } from '../../components/CardTable'
import Confirm from '../../components/Confirm'
import LoadGate from '../../components/LoadGate'
import { notifyError } from '../../lib/notify'
import { usePrefs } from '../../prefs'

type AppAdmin = { username: string; displayName: string; grantedBy: string; grantedAt: number }

const root = '/api/admin/app-platform'

/**
 * The users a Portal administrator delegated an installation's administration page to. Portal administrators can
 * always manage every app and are not listed; only a Portal administrator grants or withdraws, so this card is
 * part of the console's settings tab and nowhere else. It is shown for a version with no admin page too, so
 * delegations left over from an earlier version can still be seen and withdrawn; nothing can be added then.
 */
export default function AppAdministratorsCard({ installation, adminPage = true }: { installation: string; adminPage?: boolean }) {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const url = `${root}/installations/${installation}/app-admins`
  const [admins, setAdmins] = useState<AppAdmin[] | null>(null)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [removing, setRemoving] = useState('')
  const [adding, setAdding] = useState(false)

  useEffect(() => {
    const abort = new AbortController()
    setError('')
    api.get<{ admins: AppAdmin[] }>(url, { signal: abort.signal })
      .then(value => { if (!abort.signal.aborted) setAdmins(value?.admins ?? []) })
      .catch(failure => { if (!abort.signal.aborted) setError(errText(failure, t)) })
    return () => abort.abort()
  }, [url, attempt, t])

  const when = (value: number) => Number.isSafeInteger(value)
    ? new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
    : '—'
  const label = (admin: Pick<AppAdmin, 'username' | 'displayName'>) => admin.displayName && admin.displayName !== admin.username ? admin.displayName : admin.username
  const remove = async (admin: AppAdmin) => {
    setRemoving(admin.username)
    try {
      await api.del(`${url}/${encodeURIComponent(admin.username)}`)
      setAdmins(current => current?.filter(item => item.username !== admin.username) ?? current)
    } catch (failure) {
      notifyError(errText(failure, t))
    } finally {
      setRemoving('')
    }
  }

  return <Card title={t('appAdmin.admins.title')} extra={admins && adminPage
    ? <Button icon={<PlusOutlined aria-hidden="true" />} onClick={() => setAdding(true)}>{t('appAdmin.admins.add')}</Button>
    : undefined}>
    {!adminPage && <Alert type="info" showIcon title={t('appAdmin.admins.noAdminPage')} />}
    <Typography.Paragraph type="secondary">{t('appAdmin.admins.portalAdminsNote')}</Typography.Paragraph>
    <LoadGate loading={admins === null && !error} error={error} onRetry={() => { setAdmins(null); setAttempt(value => value + 1) }} minHeight={120}>
      <CardTable<AppAdmin> rowKey="username" size="small" pagination={false} dataSource={admins ?? []}
        locale={{ emptyText: t('appAdmin.admins.empty') }}
        columns={([
          { title: t('appAdmin.admins.user'), key: 'user', render: (_: unknown, admin) => <div>
            <div>{label(admin)}</div>
            {label(admin) !== admin.username && <Typography.Text type="secondary">{admin.username}</Typography.Text>}
          </div> },
          { title: t('appAdmin.admins.grantedBy'), dataIndex: 'grantedBy', key: 'grantedBy' },
          { title: t('appAdmin.admins.grantedAt'), key: 'grantedAt', render: (_: unknown, admin) => when(admin.grantedAt) },
          { title: '', key: 'actions', fixed: 'right', phoneLabel: t('appAdmin.admins.actions'), render: (_: unknown, admin) =>
            <Confirm title={t('appAdmin.admins.removeTitle', { name: label(admin) })} description={t('appAdmin.admins.removeBody')}
              okText={t('appAdmin.admins.removeAction')} onConfirm={() => remove(admin)}>
              <Button size="small" danger loading={removing === admin.username} icon={<DeleteOutlined aria-hidden="true" />}>{t('appAdmin.admins.remove')}</Button>
            </Confirm> },
        ] as CardColumn<AppAdmin>[])} />
    </LoadGate>
    {adding && <AddAdministrator url={url} existing={admins ?? []} onClose={() => setAdding(false)}
      onAdded={admin => { setAdmins(current => [...(current ?? []).filter(item => item.username !== admin.username), admin].sort((a, b) => a.username.localeCompare(b.username))); setAdding(false) }} />}
  </Card>
}

function AddAdministrator({ url, existing, onClose, onAdded }: { url: string; existing: AppAdmin[]; onClose: () => void; onAdded: (admin: AppAdmin) => void }) {
  const { t } = useTranslation()
  const [users, setUsers] = useState<UsersResp['users'] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [choice, setChoice] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const load = useCallback(() => {
    const abort = new AbortController()
    setLoadError('')
    api.get<UsersResp>('/api/admin/users', { signal: abort.signal })
      .then(value => { if (!abort.signal.aborted) setUsers(value?.users ?? []) })
      .catch(failure => { if (!abort.signal.aborted) setLoadError(errText(failure, t)) })
    return () => abort.abort()
  }, [t])
  useEffect(() => load(), [load])
  // An account that cannot sign in, or already administers the app, is not offered.
  const options = useMemo(() => (users ?? [])
    .filter(user => user.active && !existing.some(admin => admin.username === user.username))
    .map(user => ({ value: user.username, label: user.display_name && user.display_name !== user.username ? `${user.display_name} (${user.username})` : user.username })), [users, existing])
  const submit = async () => {
    if (!choice || busy) return
    setBusy(true)
    setError('')
    try {
      const result = await api.post<{ admin: AppAdmin }>(url, { username: choice })
      onAdded(result.admin)
    } catch (failure) {
      setError(errText(failure, t))
      setBusy(false)
    }
  }
  return <Modal open title={t('appAdmin.admins.addTitle')} okText={t('appAdmin.admins.addAction')} cancelText={t('common.cancel')}
    onOk={() => void submit()} onCancel={onClose} okButtonProps={{ disabled: !choice, loading: busy }} cancelButtonProps={{ disabled: busy }} destroyOnHidden>
    <Typography.Paragraph type="secondary">{t('appAdmin.admins.addHint')}</Typography.Paragraph>
    {loadError
      ? <Alert type="warning" showIcon title={t('common.loadFailed')} description={loadError} action={<Button size="small" onClick={() => load()}>{t('common.retry')}</Button>} />
      : <Select showSearch optionFilterProp="label" style={{ width: '100%' }} loading={users === null} value={choice} onChange={setChoice}
        placeholder={t('appAdmin.admins.pick')} aria-label={t('appAdmin.admins.pick')} options={options} />}
    {error && <Alert type="error" showIcon title={error} />}
  </Modal>
}
