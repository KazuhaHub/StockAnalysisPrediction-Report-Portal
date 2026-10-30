import { useCallback, useEffect, useMemo, useState } from 'react'
import { App, Card, Space, Switch, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { AppAccessOU, AppLayoutEntry, AppLayoutResp, UserGroupRow } from '../../api/types'
import LoadGate from '../../components/LoadGate'
import { BUILTIN_PIN_PREFIX, builtinAppByKey } from '../../lib/builtinApps'
import { DragHandle, SortableItem, SortableWrapper } from './dnd'
import InheritField from './InheritField'
import OrgUnitPicker from './OrgUnitPicker'

// App order and per-OU access (ADR 0048), the two sections of the Apps administration page that sit
// above the install list: one shared order for built-in and installed apps, and for each OU whether an
// app is open, closed or inherited, with the effective value and the OU it comes from — the same
// override/inherit model as the reader export policy (ADR 0046).
//
// A built-in app is only hidden by this: the server cannot gate the API behind it (ADR 0048), which the
// hint under the access card says.

// appTitle is what a row calls an app: a built-in app by its translated name, an installed one by the
// name it declares.
function useAppTitle() {
  const { t } = useTranslation()
  return (a: AppLayoutEntry) => {
    if (!a.builtin) return a.name
    const slug = a.key.startsWith(BUILTIN_PIN_PREFIX) ? a.key.slice(BUILTIN_PIN_PREFIX.length) : a.key
    const builtin = builtinAppByKey(slug)
    return builtin ? t(builtin.titleKey) : a.name
  }
}

export default function AppLayoutAdmin() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [groups, setGroups] = useState<UserGroupRow[]>([])
  const [apps, setApps] = useState<AppLayoutEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selectedId, setSelectedId] = useState<number | null>(null)

  const load = useCallback(async () => {
    setError('')
    try {
      const [g, l] = await Promise.all([
        api.get<{ groups: UserGroupRow[] }>('/api/admin/groups'),
        api.get<AppLayoutResp>('/api/admin/apps/layout'),
      ])
      setGroups(g?.groups ?? [])
      setApps(l?.apps ?? [])
    } catch (e) {
      setError(errText(e, t))
    } finally {
      setLoading(false)
    }
  }, [t])
  useEffect(() => {
    void load()
  }, [load])

  const title = useAppTitle()

  // Not async: SortableWrapper calls this without awaiting, so a rejected save must be handled here.
  const reorder = (keys: string[]) => {
    const byKey = new Map(apps.map((a) => [a.key, a]))
    setApps(keys.map((k) => byKey.get(k)).filter((a): a is AppLayoutEntry => !!a))
    api
      .put<AppLayoutResp>('/api/admin/apps/order', { keys })
      .then((r) => {
        if (r?.apps) setApps(r.apps)
      })
      .catch((e) => {
        message.error(errText(e, t))
        void load()
      })
  }

  const selected = groups.find((g) => g.id === selectedId) ?? groups.find((g) => g.is_default) ?? groups[0]
  const defaultId = groups.find((g) => g.is_default)?.id ?? 0

  return (
    <LoadGate loading={loading} error={error} onRetry={load}>
      <Space orientation="vertical" size={16} style={{ width: '100%' }}>
        <Card title={t('apps.order.title')} extra={<Typography.Text type="secondary">{t('apps.order.hint')}</Typography.Text>}>
          <SortableWrapper ids={apps.map((a) => a.key)} onReorder={reorder}>
            {apps.map((a) => (
              <SortableItem key={a.key} id={a.key}>
                <Space>
                  <DragHandle label={t('apps.order.drag', { name: title(a) })} />
                  {a.icon && <span>{a.icon}</span>}
                  {title(a)}
                </Space>
              </SortableItem>
            ))}
          </SortableWrapper>
        </Card>
        <Card title={t('apps.access.title')} extra={<Typography.Text type="secondary">{t('apps.access.hint')}</Typography.Text>}>
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(220px, 280px) minmax(0, 1fr)', gap: 24, alignItems: 'start' }}>
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
                if (ids[0]) setSelectedId(ids[0])
              }}
            />
            {selected && <AccessEditor group={selected} groups={groups} defaultId={defaultId} apps={apps} title={title} onChanged={load} />}
          </div>
          <Typography.Paragraph type="secondary" style={{ margin: '16px 0 0' }}>
            {t('apps.access.builtinNote')}
          </Typography.Paragraph>
        </Card>
      </Space>
    </LoadGate>
  )
}

function AccessEditor({
  group,
  groups,
  defaultId,
  apps,
  title,
  onChanged,
}: {
  group: UserGroupRow
  groups: UserGroupRow[]
  defaultId: number
  apps: AppLayoutEntry[]
  title: (a: AppLayoutEntry) => string
  onChanged: () => Promise<void>
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [busy, setBusy] = useState(false)
  const names = useMemo(() => new Map(groups.map((g) => [g.id, g.name])), [groups])
  const from = (id: number) => (id === 0 ? t('ou.systemDefault') : (names.get(id) ?? String(id)))
  const parentId = group.is_default ? 0 : group.parent_id || defaultId
  const word = (open: boolean) => (open ? t('apps.access.open') : t('apps.access.closed'))

  const put = async (key: string, enabled: boolean | null) => {
    if (busy) return
    setBusy(true)
    try {
      await api.put(`/api/admin/apps/${encodeURIComponent(key)}/access/${group.id}`, { enabled })
      await onChanged()
      message.success(t('common.saved'))
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Space orientation="vertical" size={12} style={{ width: '100%' }}>
      <Typography.Title level={5} style={{ margin: 0 }}>
        {group.name}
      </Typography.Title>
      {apps.map((a) => {
        const mine: AppAccessOU | undefined = a.access.find((x) => x.ou === group.id)
        const parent = a.access.find((x) => x.ou === parentId)
        // What clearing the override would give this OU: its parent's standing, or the built-in
        // baseline (open) for the Default OU, which inherits from nobody.
        const inheritedOpen = parent ? parent.effective : true
        const inheritedFrom = parent ? parent.source_ou : 0
        const own = mine?.own ?? null
        const label = title(a)
        return (
          <InheritField
            key={a.key}
            label={label}
            from={from(inheritedFrom)}
            inherited={word(inheritedOpen)}
            inheriting={own === null}
            onInheritingChange={(inheriting) => void put(a.key, inheriting ? null : (mine?.effective ?? inheritedOpen))}
          >
            <Switch
              checked={own ?? inheritedOpen}
              disabled={busy}
              onChange={(v) => void put(a.key, v)}
              checkedChildren={t('apps.access.open')}
              unCheckedChildren={t('apps.access.closed')}
              aria-label={label}
            />
          </InheritField>
        )
      })}
      {apps.length === 0 && <Tag>{t('apps.empty')}</Tag>}
    </Space>
  )
}
