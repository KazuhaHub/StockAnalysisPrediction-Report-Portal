import { useCallback, useEffect, useMemo, useState } from 'react'
import { App, Alert, Button, Card, InputNumber, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../../api/client'
import type { ExportPolicyEntry, UserGroupRow } from '../../../api/types'
import LoadGate from '../../../components/LoadGate'
import { inheritedView, policyDraft, policyDraftValid, policyPatch, quotaBelowMax, type PolicyDraft } from '../../../lib/exportMode'
import InheritField from '../InheritField'
import OrgUnitPicker from '../OrgUnitPicker'

// The reader-export policy console (ADR 0046), the section at the foot of the administrator's Export
// page: the OU tree on the left, and for the chosen OU three settings that each either inherit or are
// set here, showing the effective value and the OU it comes from, as Workspace's service settings do.
// The Export page owns this policy; the OU detail page does not repeat it.
export default function ReaderExportPolicy() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [groups, setGroups] = useState<UserGroupRow[]>([])
  const [entries, setEntries] = useState<ExportPolicyEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selectedId, setSelectedId] = useState<number | null>(null)

  const load = useCallback(async () => {
    setError('')
    try {
      const [g, p] = await Promise.all([
        api.get<{ groups: UserGroupRow[] }>('/api/admin/groups'),
        api.get<{ groups: ExportPolicyEntry[] }>('/api/admin/exports/policy'),
      ])
      setGroups(g?.groups ?? [])
      setEntries(p?.groups ?? [])
    } catch (e) {
      setError(errText(e, t))
    } finally {
      setLoading(false)
    }
  }, [t])
  useEffect(() => {
    void load()
  }, [load])

  const byId = useMemo(() => new Map(entries.map((e) => [e.id, e])), [entries])
  const selected = (selectedId !== null ? byId.get(selectedId) : undefined) ?? entries.find((e) => e.is_default) ?? entries[0]

  return (
    <Card title={t('export.reader.policyTitle')} extra={<Typography.Text type="secondary">{t('export.reader.policyHint')}</Typography.Text>}>
      <LoadGate loading={loading} error={error} onRetry={load}>
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
          {selected && (
            <PolicyEditor
              // A fresh draft for each OU, and again whenever the server's answer for it changes.
              key={`${selected.id}:${JSON.stringify([selected.allowed, selected.max_reports, selected.daily_quota])}`}
              entry={selected}
              entries={byId}
              defaultId={entries.find((e) => e.is_default)?.id ?? 0}
              onSaved={() => {
                message.success(t('common.saved'))
                void load()
              }}
            />
          )}
        </div>
      </LoadGate>
    </Card>
  )
}

function PolicyEditor({ entry, entries, defaultId, onSaved }: { entry: ExportPolicyEntry; entries: Map<number, ExportPolicyEntry>; defaultId: number; onSaved: () => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [draft, setDraft] = useState<PolicyDraft>(() => policyDraft(entry))
  const [busy, setBusy] = useState(false)
  const patch = policyPatch(entry, draft)
  const dirty = Object.keys(patch).length > 0 && policyDraftValid(entry, draft)
  const setField = <K extends keyof PolicyDraft>(key: K, next: Partial<PolicyDraft[K]>) => setDraft((d) => ({ ...d, [key]: { ...d[key], ...next } }))
  const parent = entries.get(entry.parent_id || defaultId)
  const from = (id: number) => (id === 0 ? t('ou.systemDefault') : (entries.get(id)?.name ?? String(id)))
  const inAllowed = inheritedView(entry, parent, 'allowed')
  const inMax = inheritedView(entry, parent, 'max_reports')
  const inQuota = inheritedView(entry, parent, 'daily_quota')
  const canInherit = !entry.is_default

  const effectiveMax = draft.max_reports.inheriting ? inMax.value : draft.max_reports.value
  const effectiveQuota = draft.daily_quota.inheriting ? inQuota.value : draft.daily_quota.value

  const save = async () => {
    setBusy(true)
    try {
      await api.put(`/api/admin/exports/policy/${entry.id}`, patch)
      onSaved()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Space orientation="vertical" size={12} style={{ width: '100%' }}>
      <Typography.Title level={5} style={{ margin: 0 }}>
        {entry.name}
      </Typography.Title>
      <InheritField
        label={t('export.reader.fieldAllowed')}
        hint={t('export.reader.fieldAllowedHint')}
        from={from(inAllowed.source)}
        inherited={inAllowed.value ? t('ou.allowed') : t('ou.notAllowed')}
        inheriting={draft.allowed.inheriting}
        onInheritingChange={(v) => setField('allowed', { inheriting: v })}
        canInherit={canInherit}
      >
        <Switch
          checked={draft.allowed.value}
          onChange={(v) => setField('allowed', { value: v })}
          checkedChildren={t('ou.allowed')}
          unCheckedChildren={t('ou.notAllowed')}
          aria-label={t('export.reader.fieldAllowed')}
        />
      </InheritField>
      <InheritField
        label={t('export.reader.fieldMax')}
        hint={t('export.reader.fieldMaxHint')}
        from={from(inMax.source)}
        inherited={String(inMax.value)}
        inheriting={draft.max_reports.inheriting}
        onInheritingChange={(v) => setField('max_reports', { inheriting: v })}
        canInherit={canInherit}
      >
        <InputNumber size="small" min={1} max={5000} precision={0} value={draft.max_reports.value} aria-label={t('export.reader.fieldMax')} onChange={(v) => setField('max_reports', { value: typeof v === 'number' ? v : null })} />
      </InheritField>
      <InheritField
        label={t('export.reader.fieldQuota')}
        hint={t('export.reader.fieldQuotaHint')}
        from={from(inQuota.source)}
        inherited={String(inQuota.value)}
        inheriting={draft.daily_quota.inheriting}
        onInheritingChange={(v) => setField('daily_quota', { inheriting: v })}
        canInherit={canInherit}
      >
        <InputNumber size="small" min={1} max={100000} precision={0} value={draft.daily_quota.value} aria-label={t('export.reader.fieldQuota')} onChange={(v) => setField('daily_quota', { value: typeof v === 'number' ? v : null })} />
      </InheritField>
      {quotaBelowMax(effectiveMax, effectiveQuota) && <Alert type="info" showIcon title={t('export.reader.quotaBelowMax', { quota: effectiveQuota, max: effectiveMax })} />}
      <div>
        <Button type="primary" disabled={!dirty} loading={busy} onClick={save}>
          {t('common.save')}
        </Button>
      </div>
    </Space>
  )
}
