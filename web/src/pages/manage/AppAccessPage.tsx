import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { App, Button, Card, Radio, Result, Space, Tag, Typography, theme } from 'antd'
import { useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import type { AppAccessOU, UserGroupRow } from '../../api/types'
import LoadGate from '../../components/LoadGate'
import { useAppLabels, useAppsCatalog, type AdminApp } from './appsAdminModel'
import { AppsBreadcrumb } from './appsAdminParts'
import OrgUnitPicker from './OrgUnitPicker'

// The service status of one app by organizational unit (ADR 0048), modelled on the Google Workspace
// Admin console: pick an OU on the left, set that one app on or off on the right. An OU inherits its
// parent's value unless it overrides it, and the page says which of the two it is looking at.
//
// A value is only written when Save is pressed, and "Inherit" removes the override. A built-in app is
// only hidden by this: the server cannot gate the API behind it, which the note under the choice says.
export default function AppAccessPage() {
  const { t } = useTranslation()
  const { failure, catchLoad, retryKey } = useLoadFailure()
  const navigate = useNavigate()
  const key = useParams().key ?? ''
  const catalog = useAppsCatalog()
  const [groups, setGroups] = useState<UserGroupRow[]>([])
  const [groupsLoading, setGroupsLoading] = useState(true)
  const [groupsError, setGroupsError] = useState('')
  const [selectedId, setSelectedId] = useState<number | null>(null)

  const loadGroups = useCallback(async () => {
    setGroupsError('')
    let settled = true
    try {
      const g = await api.get<{ groups: UserGroupRow[] }>('/api/admin/groups')
      setGroups(g?.groups ?? [])
    } catch (e) {
      const text = failure(e)
      if (text === null) settled = false
      else setGroupsError(text)
    } finally {
      if (settled) setGroupsLoading(false)
    }
  }, [t, failure])
  useEffect(() => {
    void loadGroups()
  }, [loadGroups, retryKey])

  const app = catalog.apps.find((a) => a.key === key)
  const retry = () => {
    void catalog.reload()
    void loadGroups()
  }
  return (
    <LoadGate loading={catalog.loading || groupsLoading} error={catalog.error || groupsError} onRetry={retry}>
      {app ? (
        <AccessWorkspace app={app} groups={groups} selectedId={selectedId} onSelect={setSelectedId} reload={catalog.reload} />
      ) : (
        <Result status="404" title={t('apps.notFound')} extra={<Button onClick={() => navigate('/manage/apps')}>{t('apps.backToList')}</Button>} />
      )}
    </LoadGate>
  )
}

function AccessWorkspace({
  app,
  groups,
  selectedId,
  onSelect,
  reload,
}: {
  app: AdminApp
  groups: UserGroupRow[]
  selectedId: number | null
  onSelect: (id: number) => void
  reload: () => Promise<boolean>
}) {
  const { t } = useTranslation()
  const labels = useAppLabels()
  const name = labels.title(app)
  const { modal } = App.useApp()
  const selected = groups.find((g) => g.id === selectedId) ?? groups.find((g) => g.is_default) ?? groups[0]
  // Whether the open editor holds a choice that was not saved; moving to another OU would drop it.
  const dirty = useRef(false)
  const setDirty = useCallback((d: boolean) => {
    dirty.current = d
  }, [])
  const requestSelect = (id: number) => {
    if (!dirty.current) {
      onSelect(id)
      return
    }
    modal.confirm({
      title: t('apps.access.discardTitle'),
      content: t('apps.access.discardBody'),
      okText: t('apps.access.discardOk'),
      cancelText: t('common.cancel'),
      onOk: () => onSelect(id),
    })
  }
  const overridden = useMemo(() => new Set(app.access.filter((a) => a.own !== null).map((a) => a.ou)), [app.access])
  const annotate = useCallback(
    (g: UserGroupRow) => (overridden.has(g.id) ? <Tag color="blue">{t('apps.access.overridden')}</Tag> : null),
    [overridden, t],
  )

  return (
    <>
      <AppsBreadcrumb app={app} name={name} page={t('apps.section.status.title')} />
      <Card styles={{ body: { padding: 0 } }}>
        <div className="rp-app-access">
          <div className="rp-app-access__tree">
            <OrgUnitPicker
              collapsible={false}
              mode="manage"
              groups={groups}
              unassigned={0}
              scoped
              onScopedChange={() => {}}
              selected={selected ? [selected.id] : []}
              onManage={() => {}}
              onSelect={(ids) => {
                if (ids[0] && ids[0] !== selected?.id) requestSelect(ids[0])
              }}
              annotate={annotate}
            />
          </div>
          <div className="rp-app-access__setting">
            {selected ? (
              <AccessEditor
                // A saved or reloaded value starts the choice over from what the server now says.
                key={`${selected.id}:${app.access.find((a) => a.ou === selected.id)?.own}:${app.access.find((a) => a.ou === selected.id)?.effective}`}
                app={app}
                name={name}
                group={selected}
                groups={groups}
                reload={reload}
                onDirtyChange={setDirty}
              />
            ) : (
              <Typography.Text type="secondary">{t('users.noGroups')}</Typography.Text>
            )}
          </div>
        </div>
      </Card>
    </>
  )
}

function AccessEditor({
  app,
  name,
  group,
  groups,
  reload,
  onDirtyChange,
}: {
  app: AdminApp
  name: string
  group: UserGroupRow
  groups: UserGroupRow[]
  reload: () => Promise<boolean>
  onDirtyChange: (dirty: boolean) => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { token } = theme.useToken()
  // No row for this OU means nothing was set anywhere above it: the built-in baseline, open.
  const mine: AppAccessOU = app.access.find((a) => a.ou === group.id) ?? { ou: group.id, own: null, effective: true, source_ou: 0, inherited: false }
  // null = no choice made yet. While inheriting, the radios show the effective value without that
  // being a choice; saving any choice writes an explicit override, which pins the OU against later
  // changes to its parent.
  const [choice, setChoice] = useState<boolean | null>(null)
  const draft = choice ?? mine.effective
  const [busy, setBusy] = useState(false)
  const dirty = choice !== null && choice !== mine.own
  useEffect(() => {
    onDirtyChange(dirty)
    return () => onDirtyChange(false)
  }, [dirty, onDirtyChange])

  const source =
    mine.own !== null
      ? t('apps.access.source.overridden')
      : mine.source_ou === 0
        ? t('apps.access.source.system')
        : t('apps.access.source.inherited', { from: groups.find((g) => g.id === mine.source_ou)?.name ?? String(mine.source_ou) })

  const put = async (enabled: boolean | null) => {
    setBusy(true)
    try {
      await api.put(`/api/admin/apps/${encodeURIComponent(app.key)}/access/${group.id}`, { enabled })
      if (!(await reload())) message.warning(t('apps.access.reloadFailed'))
      message.success(t('common.saved'))
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  // The whole card is the radio's own label, so a click anywhere on it chooses and the keyboard
  // reaches it as an ordinary radio. Its name is the title alone; the consequence describes it.
  const option = (value: boolean, title: string, hint: string, id: string) => (
    <Radio
      value={value}
      aria-label={title}
      aria-describedby={id}
      // onChange does not fire for the radio that is already checked, and while inheriting the
      // effective value is shown checked: choosing it is how an admin pins that value.
      onClick={() => setChoice(value)}
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        width: '100%',
        padding: '12px 14px',
        marginBottom: 8,
        marginInlineEnd: 0,
        borderRadius: token.borderRadiusLG,
        border: `1px solid ${draft === value ? token.colorPrimary : token.colorBorderSecondary}`,
        background: draft === value ? token.colorPrimaryBg : undefined,
      }}
    >
      <div>{title}</div>
      <div id={id} style={{ fontSize: 12, color: token.colorTextSecondary }}>
        {hint}
      </div>
    </Radio>
  )

  return (
    <Space orientation="vertical" size={4} style={{ width: '100%' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>{`${name} · ${group.name}`}</Typography.Title>
      <Typography.Text type="secondary">{t('apps.access.explain')}</Typography.Text>
      <div style={{ margin: '12px 0 16px' }}>
        <Tag bordered={false} color={mine.own !== null ? 'blue' : undefined}>
          {source}
        </Tag>
      </div>
      <Radio.Group value={draft} onChange={(e) => setChoice(e.target.value)} style={{ display: 'block' }} disabled={busy}>
        {option(true, t('apps.access.on'), t('apps.access.onHint'), 'apps-access-on')}
        {option(false, t('apps.access.off'), app.builtin ? t('apps.access.offHintBuiltin') : t('apps.access.offHint'), 'apps-access-off')}
      </Radio.Group>
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, flexWrap: 'wrap', marginTop: 8 }}>
        <Button disabled={busy || mine.own === null} onClick={() => void put(null)}>
          {t('apps.access.inherit')}
        </Button>
        <Button type="primary" loading={busy} disabled={!dirty} onClick={() => void put(draft)}>
          {t('common.save')}
        </Button>
      </div>
      {app.builtin && (
        <Typography.Paragraph type="secondary" style={{ margin: '16px 0 0', fontSize: 13 }}>
          {t('apps.access.builtinNote')}
        </Typography.Paragraph>
      )}
    </Space>
  )
}
