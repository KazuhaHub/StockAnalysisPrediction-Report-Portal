import { useEffect, useState } from 'react'
import { Alert, App, Breadcrumb, Button, Card, Checkbox, ConfigProvider, Empty, Input, InputNumber, Modal, Popconfirm, Radio, Select, Space, Switch, Table, Tag, Typography } from 'antd'
import { DeleteOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import { loadFailure } from '../../lib/loadFailure'
import { useLoadFailure } from '../../lib/useOnReconnect'
import type { ForecastScope, GroupTargetRow, GroupTargetsResp, OUPolicyResults, UserGroupRow } from '../../api/types'
import InheritField from './InheritField'
import { ouPath, presentOUPolicies, resolveOU, type UrgentPolicy } from './ouSettings'
import { subtreeOf } from './OrgUnitPicker'
import { usePendingSettings } from './AdminDraftBoundary'

const RUN_SURFACES = ['run', 'batch', 'recurring', 'chat'] as const
const FORECAST_SCOPES = ['all', 'own', 'none'] as const
// The windows a run cap can be measured over. "total" is the life of the ACCOUNT — a trial with a
// fixed number of runs — and is the one that never refills.
const QUOTA_PERIODS = ['day', 'week', 'month', 'total'] as const
export type OUPolicySection = 'directory' | 'runs' | 'security' | 'forecast' | 'access' | 'targets'
const SECTION_FIELDS: Record<OUPolicySection, string[]> = {
  directory: ['name', 'description', 'parent_id'],
  runs: ['allow_urgent', 'urgent_unlimited', 'weight', 'max_queued', 'run_window', 'priority', 'daily_run_quota', 'run_quota_period'],
  security: ['totp_enroll', 'passkey_enroll', 'require_2fa'],
  forecast: ['forecast_scope'],
  access: ['restricted'],
  targets: [],
}
const POLICY_FIELDS = new Set(Object.values(SECTION_FIELDS).flat())

// Shared OU editor. Each feature supplies only its owned sections; the directory defaults to
// identity and hierarchy. PATCH payloads follow the same ownership boundary as the visible fields.

interface Draft {
  name: string
  description: string
  parent_id: number
  urgentInherit: boolean
  urgentPolicy: UrgentPolicy
  weight: number
  maxqInherit: boolean
  maxQueued: number
  windowInherit: boolean
  runWindow: string
  priority: number | null
  restricted: boolean
  quotaInherit: boolean
  dailyQuota: number
  quotaPeriod: string
  totpInherit: boolean
  totpAllowed: boolean
  passkeyInherit: boolean
  passkeyAllowed: boolean
  requireInherit: boolean
  require2fa: boolean
  // Who may read the forecast record from this OU. null = inherit the parent OU; the Default OU
  // inherits from nobody, so it always holds a value.
  forecastScope: ForecastScope | null
}

// The forecast scope this OU stores, as the form edits it. The Default OU stores 'none' rather than
// NULL, so a NULL read there (an older dump) shows the value that is in force.
const storedForecastScope = (g: UserGroupRow): ForecastScope | null =>
  g.forecast_scope ?? (g.is_default ? 'none' : null)

// What an OU would inherit if it set nothing itself, as the server resolved it for the unit it
// would inherit from: its parent, or the Default OU for a root. The OU's own fields describe what
// is in effect NOW, which is the override it would be replacing.
function inheritedForecast(g: UserGroupRow, groups: UserGroupRow[]): { value: ForecastScope; from: string } {
  const def = groups.find((x) => x.is_default)
  const parent = (g.parent_id ? groups.find((x) => x.id === g.parent_id) : undefined) ?? def
  const value = parent?.forecast_scope_effective ?? 'none'
  const source = groups.find((x) => x.id === parent?.forecast_scope_source) ?? parent ?? def
  return { value, from: source?.name ?? '' }
}

// One announcement that names the OU being deleted. `orphaned` means this OU is its ONLY recipient,
// so deleting leaves it addressed to nobody — the distinction the dialog has to make, because the
// others go on working untouched.
interface AffectedAnnouncement {
  id: number
  title: string
  enabled: boolean
  orphaned: boolean
}

function draftOf(g: UserGroupRow, def: UserGroupRow | undefined, groups: UserGroupRow[]): Draft {
  const r = resolveOU(g, def, groups)
  const isDefault = !!g.is_default
  return {
    name: g.name,
    description: g.description ?? '',
    parent_id: g.parent_id ?? 0,
    urgentInherit: !isDefault && g.allow_urgent == null && g.urgent_unlimited == null && g.weight == null,
    urgentPolicy: r.urgent.value,
    weight: r.weight.value,
    maxqInherit: !isDefault && g.max_queued == null,
    maxQueued: r.maxQueued.value,
    windowInherit: !isDefault && g.run_window == null,
    runWindow: r.runWindow.value,
    priority: r.priority.value,
    restricted: !!g.restricted,
    quotaInherit: !isDefault && g.daily_run_quota == null,
    dailyQuota: r.dailyQuota.value,
    quotaPeriod: r.quotaPeriod.value,
    totpInherit: !isDefault && g.totp_enroll == null,
    totpAllowed: r.totpAllowed.value,
    passkeyInherit: !isDefault && g.passkey_enroll == null,
    passkeyAllowed: r.passkeyAllowed.value,
    requireInherit: !isDefault && g.require_2fa == null,
    require2fa: r.require2fa.value,
    forecastScope: storedForecastScope(g),
  }
}

export default function OrgUnitDetail({
  group,
  groups,
  onChanged,
  onDeleted,
  onPendingChange,
  sections = ['directory'],
  onMembers,
}: {
  group: UserGroupRow
  groups: UserGroupRow[]
  onChanged: () => void
  onDeleted: () => void
  onPendingChange?: (state: { dirty: boolean; saving: boolean }) => void
  sections?: OUPolicySection[]
  onMembers?: () => void
}) {
  const { t } = useTranslation()
  const { failure, retryKey } = useLoadFailure()
  const { message } = App.useApp()
  const def = groups.find((g) => g.is_default)
  const isDefault = !!group.is_default
  const show = (section: OUPolicySection) => sections.includes(section)
  // What this OU WOULD inherit — resolved as if it set nothing itself. The inherit option has to
  // name that, not the OU's current value: an OU overriding max-queued to 9 was offering "inherit
  // Default — 9", which is the override it would be replacing.
  const [previewPolicies, setPreviewPolicies] = useState<OUPolicyResults | null>(null)
  const inheritedPolicies = previewPolicies ?? group.inherited_policy_results
  const wouldInherit = inheritedPolicies ? presentOUPolicies(inheritedPolicies) : resolveOU(
    {
      ...group, policy_results: undefined,
      allow_urgent: null,
      urgent_unlimited: null,
      weight: null,
      max_queued: null,
      run_window: null,
      daily_run_quota: null,
      run_quota_period: '',
      priority: '',
      totp_enroll: null,
      passkey_enroll: null,
      require_2fa: null,
    } as UserGroupRow,
    def,
    groups,
  )

  // Absent on a server that predates the access model: then there is nothing true to show or send.
  const hasForecast = group.forecast_scope_effective !== undefined
  const forecastInherit = inheritedForecast(group, groups)
  if (inheritedPolicies) {
    forecastInherit.value = inheritedPolicies.forecast_scope.effective
    forecastInherit.from = groups.find((g) => g.id === inheritedPolicies.forecast_scope.source_ou)?.name ?? t('ou.systemDefault')
  }

  const [d, setD] = useState<Draft>(() => draftOf(group, def, groups))
  const [saving, setSaving] = useState(false)
  const [previewError, setPreviewError] = useState('')
  // A parent move whose preview has not been answered (still in flight, or the connection was down):
  // the panel must not present the current parent's inheritance as the result, and Save waits.
  const [previewPending, setPreviewPending] = useState(false)
  const [impact, setImpact] = useState<AffectedAnnouncement[] | null>(null)
  const [orphanChoice, setOrphanChoice] = useState<'disable' | 'keep'>('disable')
  const [deleting, setDeleting] = useState(false)
  const [acknowledged, setAcknowledged] = useState(() => draftOf(group, def, groups))
  const [targetsPending, setTargetsPending] = useState({ dirty: false, saving: false })
  const dirty = JSON.stringify(d) !== JSON.stringify(acknowledged)
  usePendingSettings(dirty, saving)
  useEffect(() => { onPendingChange?.({ dirty: dirty || targetsPending.dirty, saving: saving || targetsPending.saving }) }, [dirty, saving, targetsPending, onPendingChange])
  // Re-seed when the tree selection moves, or the pane would keep the previous OU's draft.
  useEffect(() => {
    if (dirty) return
    const next = draftOf(group, def, groups)
    setD(next); setAcknowledged(next)
  }, [group, def, groups]) // eslint-disable-line react-hooks/exhaustive-deps
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((p) => ({ ...p, [k]: v }))
  useEffect(() => {
    setPreviewPolicies(null)
    setPreviewError('')
    setPreviewPending(false)
    if (!group.policy_results || d.parent_id === (group.parent_id ?? 0)) return
    let current = true
    setPreviewPending(true)
    const timer = setTimeout(() => {
      api.post<{ inherited_policy_results: OUPolicyResults }>(`/api/admin/groups/${group.id}/preview`, {
        ...Object.fromEntries(Object.entries(group).filter(([key]) => POLICY_FIELDS.has(key))),
        name: d.name, parent_id: d.parent_id,
      }).then((r) => { if (current) { setPreviewPolicies(r.inherited_policy_results); setPreviewPending(false) } })
        // A lost connection leaves the preview pending and asks again when the connection returns.
        .catch((e) => { const text = failure(e); if (current && text !== null) { setPreviewError(text); setPreviewPending(false) } })
    }, 150)
    return () => { current = false; clearTimeout(timer) }
  }, [group.id, group.parent_id, group.policy_results, d.parent_id, retryKey])

  // Who the inherited values come from. Run governance falls back to the Default group; the OU tree
  // is not involved, which is a distinction the old screen never drew and this label has to.
  const from = def?.name ?? t('users.defaultGroupTag')
  // While a parent move's preview is unanswered, the current parent's values would pass for the
  // result of the move; they are shown as a dash instead.
  const pending = (text: string) => (previewPending ? '—' : text)
  const sourceOf = (key: keyof OUPolicyResults) => pending(inheritedPolicies
    ? groups.find((g) => g.id === inheritedPolicies[key].source_ou)?.name ?? t('ou.systemDefault') : from)
  const num = (n: number) => (n > 0 ? String(n) : t('ou.unlimited'))
  // "20" does not say 20 of what, and "0 per month" is a contradiction — unlimited has no window.
  const quotaLabel = (n: number, period: string) => (n > 0 ? `${n} ${t(`ou.period.${period}`)}` : t('ou.unlimited'))
  const invalidParents = subtreeOf(groups, [group.id])

  const save = async () => {
    setSaving(true)
    try {
      const values = {
        name: d.name,
        description: d.description,
        // The urgent policy is one control mapped back onto three stored fields, so "allowed" and
        // "unlimited" can never contradict each other the way the tags used to.
        allow_urgent: d.urgentInherit ? null : d.urgentPolicy !== 'off',
        urgent_unlimited: d.urgentInherit ? null : d.urgentPolicy === 'unlimited',
        weight: d.urgentInherit ? null : d.urgentPolicy === 'ticket' ? d.weight : 0,
        max_queued: d.maxqInherit ? null : d.maxQueued,
        run_window: d.windowInherit ? null : d.runWindow,
        priority: d.priority == null ? '' : String(d.priority),
        restricted: isDefault ? false : d.restricted,
        daily_run_quota: d.quotaInherit ? null : d.dailyQuota,
        run_quota_period: d.quotaPeriod,
        // null = inherit the parent OU, the same reading daily_run_quota has just above.
        totp_enroll: d.totpInherit ? null : d.totpAllowed,
        passkey_enroll: d.passkeyInherit ? null : d.passkeyAllowed,
        require_2fa: d.requireInherit ? null : d.require2fa,
        ...(!isDefault && d.parent_id !== (group.parent_id ?? 0) ? { parent_id: d.parent_id } : {}),
        // Presence-aware on the server: omitted leaves the stored value alone, so a save that
        // changed only the name can never rewrite who may read the record.
        ...(hasForecast && d.forecastScope !== storedForecastScope(group) ? { forecast_scope: d.forecastScope } : {}),
      }
      const fields = new Set(sections.flatMap((section) => SECTION_FIELDS[section]))
      const res = await api.patch<{ pending_enrolment?: number }>(`/api/admin/groups/${group.id}`,
        Object.fromEntries(Object.entries(values).filter(([key]) => fields.has(key))))
      // Turning a mandate on asks people to do something at their next sign-in, and how many is the
      // difference between a policy and a surprise.
      if (res?.pending_enrolment) {
        message.warning(t('ou.mandatePending', { count: res.pending_enrolment }))
      } else {
        message.success(t('common.saved'))
      }
      onChanged()
      setAcknowledged(d)
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setSaving(false)
    }
  }

  // Deleting an OU takes the announcements addressed to it with it — not the rows, but their
  // recipients, since a grant naming a deleted OU would be inherited by whichever OU is created
  // next. An announcement whose ONLY recipient was this OU is then addressed to nobody: still
  // enabled, still "live", reaching no one and reporting nothing.
  //
  // So the delete asks first. The server refuses without an answer (it is a 409, not a UI-only
  // nicety, so a script cannot take the silent path either); this dialog is where the answer is
  // given, showing which announcements are affected and which of them stop reaching anyone.
  const remove = async () => {
    try {
      const r = await api.get<{ affected: AffectedAnnouncement[] }>(
        `/api/admin/groups/${group.id}/announcements`,
      )
      const affected = r.affected ?? []
      if (affected.some((a) => a.orphaned)) {
        setImpact(affected)
        return
      }
      await doRemove()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const doRemove = async (orphans?: 'disable' | 'keep') => {
    setDeleting(true)
    try {
      await api.del(`/api/admin/groups/${group.id}${orphans ? `?orphans=${orphans}` : ''}`)
      message.success(t('common.saved'))
      setImpact(null)
      onDeleted()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div className={`rp-ou-detail${show('directory') ? '' : ' rp-ou-detail--policy'}`}>
      <ConfigProvider componentDisabled={saving}>
      {(group.policy_available === false || previewError) && <Alert type="error" showIcon title={previewError || t('err.ou_invalid_topology')} />}
      {previewPending && !previewError && <Alert type="info" showIcon title={t('ou.previewPending')} />}
      <Card
        className="rp-ou-profile-card"
        size="small"
        title={
          <Space>
            <span>{group.name}</span>
            {isDefault && <Tag color="green">{t('users.defaultGroupTag')}</Tag>}
            {group.restricted_effective && (
              <Tag color="volcano">
                {t('users.restrictedTag')}
                {!group.restricted && <span style={{ opacity: 0.6 }}> · {t('users.inheritedTag')}</span>}
              </Tag>
            )}
          </Space>
        }
        extra={
          sections.some((section) => SECTION_FIELDS[section].length > 0 && !(section === 'access' && isDefault)) &&
          <Space wrap>
          {dirty && <Button size="small" onClick={() => setD(acknowledged)}>{t('manage.discard')}</Button>}
          <Button type="primary" size="small" loading={saving} disabled={group.policy_available === false || !!previewError || previewPending} onClick={save}>
            {t('common.save')}
          </Button>
          </Space>
        }
      >
        <div className="rp-ou-profile-grid">
          <div className="rp-ou-profile-path">
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('ou.path')}
            </Typography.Text>
            <Breadcrumb items={ouPath(groups, group.id).map((n) => ({ title: n }))} />
          </div>
          {show('directory') && <>
          <label className="rp-ou-form-field">
            <Typography.Text strong>{t('users.groupName')}</Typography.Text>
            <Input value={d.name} onChange={(e) => set('name', e.target.value)} />
          </label>
          {!isDefault && (
            <label className="rp-ou-form-field">
              <Typography.Text strong>{t('users.parentOu')}</Typography.Text>
              <Select
                showSearch
                optionFilterProp="label"
                style={{ width: '100%' }}
                value={d.parent_id}
                onChange={(v) => set('parent_id', v)}
                options={[
                  ...(d.parent_id === 0 ? [{ value: 0, label: t('users.parentOuNone'), disabled: true }] : []),
                  // An OU cannot be placed under itself or one of its descendants. The server also
                  // refuses cycles, but not offering them keeps the admin out of a dead end.
                  ...groups.filter((g) => !invalidParents.has(g.id)).map((g) => ({ value: g.id, label: g.name })),
                ]}
              />
              <Typography.Text type="secondary" style={{ display: 'block', fontSize: 12, marginTop: 4 }}>
                {t('users.parentOuHint')}
              </Typography.Text>
            </label>
          )}
          <label className="rp-ou-form-field rp-ou-form-field--wide">
            <Typography.Text strong>{t('users.groupDesc')}</Typography.Text>
            <Input.TextArea rows={2} value={d.description} onChange={(e) => set('description', e.target.value)} />
          </label>
          </>}
        </div>
        {show('directory') && onMembers && <Button style={{ marginTop: 16 }} onClick={onMembers}>{t('ou.viewMembers')}</Button>}
      </Card>
      {sections.some((section) => !['directory', 'targets'].includes(section)) &&
      <div className="rp-ou-policy-grid">
        {show('runs') &&
        <Card className="rp-ou-policy-card" size="small" title={t('ou.sectionRuns')}>
        <InheritField
          label={t('users.urgentPolicy')}
          hint={t('users.urgentPolicyHint')}
          from={sourceOf('weight')}
          inherited={pending(t(`ou.urgent.${wouldInherit.urgent.value}`))}
          inheriting={d.urgentInherit}
          onInheritingChange={(v) => set('urgentInherit', v)}
        >
          <Space>
            <Select
              size="small"
              style={{ width: 150 }}
              value={d.urgentPolicy}
              onChange={(v) => set('urgentPolicy', v)}
              options={[
                { value: 'off', label: t('users.urgentOff') },
                { value: 'ticket', label: t('users.urgentTicket') },
                { value: 'unlimited', label: t('users.urgentUnlimitedOpt') },
              ]}
            />
            {d.urgentPolicy === 'ticket' && (
              <InputNumber size="small" min={0} max={999} value={d.weight} onChange={(v) => set('weight', v ?? 0)} />
            )}
          </Space>
        </InheritField>

        <InheritField
          label={t('users.maxQueued')}
          hint={t('users.maxQueuedHint')}
          from={sourceOf('max_queued')}
          inherited={pending(num(wouldInherit.maxQueued.value))}
          inheriting={d.maxqInherit}
          onInheritingChange={(v) => set('maxqInherit', v)}
        >
          <InputNumber size="small" min={0} max={999} value={d.maxQueued} onChange={(v) => set('maxQueued', v ?? 0)} />
        </InheritField>

        <InheritField
          label={t('users.runWindow')}
          hint={t('users.runWindowHint')}
          from={sourceOf('run_window')}
          inherited={pending(wouldInherit.runWindow.value || t('ou.anyHour'))}
          inheriting={d.windowInherit}
          onInheritingChange={(v) => set('windowInherit', v)}
        >
          <Input size="small" style={{ width: 120 }} placeholder="9-18" value={d.runWindow} onChange={(e) => set('runWindow', e.target.value)} />
        </InheritField>

        {/* Priority falls back to the SYSTEM default, not to the Default group — a different
            fallback from everything above it, so it names a different source. */}
        <InheritField
          label={t('users.priority')}
          hint={t('users.priorityHint')}
          from={t('ou.systemDefault')}
          inherited=""
          inheriting={d.priority == null}
          onInheritingChange={(v) => set('priority', v ? null : 50)}
        >
          <InputNumber size="small" min={0} max={100} value={d.priority ?? undefined} onChange={(v) => set('priority', v ?? 0)} />
        </InheritField>
      </Card>}

      {/* Whether this OU's members may add a second factor. It is a policy about its own members,
          so a child may allow what its parent withdrew — and withdrawing it never removes a factor
          someone already has, which is what stops this from being a lockout. */}
      {show('security') && <Card className="rp-ou-policy-card" size="small" title={t('ou.sectionSecurity')}>
        <InheritField
          label={t('ou.allowTOTP')}
          hint={t('ou.allowTOTPHint')}
          from={sourceOf('totp_enroll')}
          inherited={pending(wouldInherit.totpAllowed.value ? t('ou.allowed') : t('ou.notAllowed'))}
          inheriting={d.totpInherit}
          onInheritingChange={(v) => set('totpInherit', v)}
        >
          <Switch checked={d.totpAllowed} onChange={(v) => set('totpAllowed', v)} />
        </InheritField>
        <InheritField
          label={t('ou.allowPasskey')}
          hint={t('ou.allowPasskeyHint')}
          from={sourceOf('passkey_enroll')}
          inherited={pending(wouldInherit.passkeyAllowed.value ? t('ou.allowed') : t('ou.notAllowed'))}
          inheriting={d.passkeyInherit}
          onInheritingChange={(v) => set('passkeyInherit', v)}
        >
          <Switch checked={d.passkeyAllowed} onChange={(v) => set('passkeyAllowed', v)} />
        </InheritField>
        {/* The mandate. It reads "inherit" from an ANCESTOR rather than from the Default group,
            which is what sticky means and what the hint says. */}
        <InheritField
          label={t('ou.require2fa')}
          hint={t('ou.require2faHint')}
          from={sourceOf('require_2fa')}
          inherited={pending(wouldInherit.require2fa.value ? t('ou.required') : t('ou.notRequired'))}
          inheriting={d.requireInherit}
          onInheritingChange={(v) => set('requireInherit', v)}
        >
          <Switch checked={d.require2fa} onChange={(v) => set('require2fa', v)} />
        </InheritField>
      </Card>}

      {show('forecast') && hasForecast && (
        <Card className="rp-ou-policy-card" size="small" title={t('nav.forecast_record')}>
          <InheritField
            label={t('forecast.scope.label')}
            hint={t('forecast.scope.help')}
            from={pending(forecastInherit.from)}
            inherited={pending(t(`forecast.scope.${forecastInherit.value}`))}
            inheriting={d.forecastScope == null}
            // Choosing "set here" without picking a value pins what is in effect, as the other
            // fields do.
            onInheritingChange={(v) => set('forecastScope', v ? null : (group.forecast_scope_effective ?? 'none'))}
            canInherit={!isDefault}
          >
            <Select
              size="small"
              style={{ width: 150 }}
              // On the Default unit there is no inherit action around the control, so this is the
              // only thing that names it for a screen reader.
              aria-label={t('forecast.scope.label')}
              value={d.forecastScope ?? undefined}
              onChange={(v) => set('forecastScope', v)}
              options={FORECAST_SCOPES.map((v) => ({ value: v, label: t(`forecast.scope.${v}`) }))}
            />
          </InheritField>
          {group.restricted_effective && (
            <Alert type="info" showIcon title={t('forecast.scope.external_note')} />
          )}
        </Card>
      )}

      {show('access') && !isDefault && (
        <Card className="rp-ou-policy-card" size="small" title={t('ou.accessBoundary')}>
          <Space align="start" style={{ marginBottom: 12 }}>
            <Switch checked={d.restricted} onChange={(v) => set('restricted', v)} />
            <div>
              <Typography.Text strong style={{ display: 'block' }}>
                {t('users.restricted')}
              </Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('users.restrictedHint')}
              </Typography.Text>
            </div>
          </Space>
        </Card>
      )}
      {show('access') && isDefault && <Alert type="info" showIcon title={t('ou.rootAccess')} />}
      {show('runs') && (
        <Card className="rp-ou-policy-card" size="small" title={t('users.dailyRunQuota')}>
          <InheritField
            label={t('users.dailyRunQuota')}
            hint={t('users.dailyRunQuotaHint')}
            from={sourceOf('quota')}
            inherited={pending(quotaLabel(wouldInherit.dailyQuota.value, wouldInherit.quotaPeriod.value))}
            inheriting={d.quotaInherit}
            canInherit={!isDefault}
            onInheritingChange={(v) => set('quotaInherit', v)}
          >
            <Space size={6}>
              <InputNumber size="small" min={0} max={999} value={d.dailyQuota} onChange={(v) => set('dailyQuota', v ?? 0)} />
              {/* The number and the window are one setting — "20" alone does not say 20 of what —
                  so they sit together and are stored together. */}
              <Select
                size="small"
                style={{ width: 118 }}
                value={d.quotaPeriod}
                onChange={(v) => set('quotaPeriod', v)}
                options={QUOTA_PERIODS.map((p) => ({ value: p, label: t(`ou.period.${p}`) }))}
              />
            </Space>
          </InheritField>
        </Card>
      )}
      </div>}

      {/* Only a restricted OU has an allow-list; for anyone else it governs nothing. It used to hide
          behind a small icon button opening a second modal. */}
      {show('targets') && group.restricted_effective && (
        <div className="rp-ou-targets">
          <TargetsSection group={group} onPendingChange={setTargetsPending} />
        </div>
      )}
      {show('targets') && !group.restricted_effective && <Alert type="info" showIcon title={t('ou.unrestrictedTargets')} />}

      {show('directory') && !isDefault && (
        <Popconfirm title={t('users.deleteGroupConfirm')} onConfirm={remove}>
          <Button danger icon={<DeleteOutlined />}>
            {t('ou.deleteOu')}
          </Button>
        </Popconfirm>
      )}

      <Modal
        open={!!impact}
        title={t('ou.announcementImpactTitle')}
        onCancel={() => setImpact(null)}
        okText={t('ou.deleteOu')}
        okButtonProps={{ danger: true, loading: deleting }}
        cancelText={t('common.cancel')}
        onOk={() => doRemove(orphanChoice)}
        destroyOnHidden
      >
        <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          <Typography.Text>{t('ou.announcementImpactDesc', { name: group.name })}</Typography.Text>
          <Space orientation="vertical" size={4} style={{ width: '100%' }}>
            {(impact ?? []).map((a) => (
              <Space key={a.id} size={6}>
                <Typography.Text>{a.title || t('announcementAdmin.untitled')}</Typography.Text>
                {a.orphaned ? (
                  <Tag color="red">{t('ou.announcementOrphaned')}</Tag>
                ) : (
                  <Tag>{t('ou.announcementKeepsWorking')}</Tag>
                )}
              </Space>
            ))}
          </Space>
          <Radio.Group
            value={orphanChoice}
            onChange={(e) => setOrphanChoice(e.target.value)}
            options={[
              { value: 'disable', label: t('ou.orphanDisable') },
              { value: 'keep', label: t('ou.orphanKeep') },
            ]}
          />
        </Space>
      </Modal>
      </ConfigProvider>
    </div>
  )
}

// The "OU × workflow × surface" allow-list (ADR 0022 R3). A restricted OU is default-deny: no row
// here means its members can run nothing, which is what the empty state has to say.
function TargetsSection({ group, onPendingChange }: { group: UserGroupRow; onPendingChange: (state: { dirty: boolean; saving: boolean }) => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [data, setData] = useState<GroupTargetsResp | null>(null)
  const [granted, setGranted] = useState<Record<number, string[]>>({})
  const [loadFailed, setLoadFailed] = useState(false)
  const [saving, setSaving] = useState(false)
  const [inherit, setInherit] = useState(false)
  const [saved, setSaved] = useState<{ inherit: boolean; granted: Record<number, string[]> } | null>(null)
  const dirty = saved !== null && JSON.stringify({ inherit, granted }) !== JSON.stringify(saved)
  usePendingSettings(dirty, saving)
  useEffect(() => { onPendingChange({ dirty, saving }) }, [dirty, saving, onPendingChange])

  useEffect(() => {
    setData(null)
    setLoadFailed(false)
    api
      .get<GroupTargetsResp>(`/api/admin/groups/${group.id}/targets`)
      .then((r) => {
        setData(r)
        setInherit(r.configured === false)
        const m: Record<number, string[]> = {}
        for (const g of r.granted || []) m[g.target_id] = g.surfaces
        setGranted(m)
        setSaved({ inherit: r.configured === false, granted: m })
      })
      // The toast alone was enough while the table rendered an empty list underneath it; now that
      // the table waits for `data`, a failure has to settle the wait too — otherwise the pane
      // spins for ever after a message the admin has already scrolled past.
      .catch((e) => {
        message.error(errText(e, t))
        setLoadFailed(true)
      })
  }, [group.id])

  const save = async () => {
    setSaving(true)
    try {
      await api.put(`/api/admin/groups/${group.id}/targets`, {
        inherit,
        granted: Object.entries(granted).map(([id, surfaces]) => ({ target_id: Number(id), surfaces })),
      })
      message.success(t('common.saved'))
      setSaved({ inherit, granted })
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card
      size="small"
      title={t('ou.sectionTargets')}
      extra={
        // Save PUTs every grant this pane holds, and it holds {} until the GET lands — one click
        // from revoking the OU's whole allow-list without having seen it.
        <Space wrap>
        {dirty && <Button size="small" disabled={saving} onClick={() => { if (saved) { setGranted(saved.granted); setInherit(saved.inherit) } }}>{t('manage.discard')}</Button>}
        <Button size="small" loading={saving} disabled={data == null} onClick={save}>
          {t('common.save')}
        </Button>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t('users.groupTargetsHint')}
      </Typography.Paragraph>
      {data?.configured !== undefined && <Radio.Group disabled={saving} value={inherit} onChange={(e) => setInherit(e.target.value)}>
        <Radio value={true}>{t('ou.targetsInherit')}</Radio>
        <Radio value={false}>{t('ou.targetsOverride')}</Radio>
      </Radio.Group>}
      {inherit && <Typography.Paragraph type="secondary">{t('ou.targetsEffective', { count: data?.effective?.length ?? 0 })}</Typography.Paragraph>}
      <fieldset disabled={inherit || saving} style={{ border: 0, padding: 0, margin: 0 }}>
      {data && data.targets.length === 0 ? (
        <Empty description={t('users.groupTargetsNoTargets')} />
      ) : (
        // A matrix, not a list of expanding rows. Three things were wrong with the list: a
        // CheckableTag looks nearly the same on as off, so the state was unreadable; the surfaces
        // only appeared once the workflow was switched on, so you could not see what a workflow
        // WOULD offer; and there was no way to compare one column across workflows, which is the
        // question an admin actually has ("who can use 批量?").
        <div className="rp-table-scroll">
        <Table
          size="small"
          rowKey="id"
          pagination={false}
          // Otherwise antd's own "No data" placeholder says the portal has no workflows to grant,
          // in the seconds before it has been asked.
          loading={data == null && !loadFailed}
          // Otherwise the failure ends as antd's "No data" — the same false empty claim, just
          // reached by a different route.
          locale={loadFailed ? { emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('common.loadFailedContent')} /> } : undefined}
          dataSource={data?.targets ?? []}
          columns={[
            {
              title: t('ou.targetEnabled'),
              width: 64,
              render: (_: unknown, tg: GroupTargetRow) => (
                <Switch
                  size="small"
                  checked={granted[tg.id] != null}
                  onChange={(v) =>
                    setGranted((g) => {
                      const next = { ...g }
                      // Enabling grants every surface the target itself allows — the common case,
                      // and narrowing from there is one click. Enabling to nothing would be a
                      // workflow that is on and still unusable.
                      if (v) next[tg.id] = tg.surfaces
                      else delete next[tg.id]
                      return next
                    })
                  }
                />
              ),
            },
            {
              title: t('ou.targetName'),
              render: (_: unknown, tg: GroupTargetRow) => (
                <Space size={6}>
                  <Typography.Text>{tg.name}</Typography.Text>
                  {tg.output_subtype && <Tag color="blue">{tg.output_subtype}</Tag>}
                </Space>
              ),
            },
            ...RUN_SURFACES.map((sf) => {
              // The rows this toggle governs: the workflow allows the surface AND its row is on.
              // The state has to be read off the SAME set the click writes to. Reading it off every
              // row instead left the header stuck unchecked whenever one workflow was switched off,
              // so it could only ever grant the surface and never take it back.
              const governed = (data?.targets ?? []).filter((tg) => tg.surfaces.includes(sf) && granted[tg.id] != null)
              const on = governed.filter((tg) => (granted[tg.id] ?? []).includes(sf))
              return {
              title: (
                <Space orientation="vertical" size={2} align="center">
                  <span>{t(`users.surface.${sf}`)}</span>
                  {/* Column-wide toggle: with a dozen workflows, "everyone may use 批量" is one
                      click rather than a dozen. Only offered to targets that allow the surface. */}
                  <Checkbox
                    disabled={governed.length === 0}
                    checked={governed.length > 0 && on.length === governed.length}
                    indeterminate={on.length > 0 && on.length < governed.length}
                    onChange={(e) =>
                      setGranted((g) => {
                        const next = { ...g }
                        for (const tg of governed) {
                          const cur = next[tg.id] ?? []
                          next[tg.id] = e.target.checked
                            ? cur.includes(sf)
                              ? cur
                              : [...cur, sf]
                            : cur.filter((x) => x !== sf)
                        }
                        return next
                      })
                    }
                  />
                </Space>
              ),
              width: 92,
              align: 'center' as const,
              render: (_: unknown, tg: GroupTargetRow) =>
                // A surface the TARGET does not allow is not an unchecked box you may tick — it is
                // not applicable, and saying so beats a control that silently does nothing.
                !tg.surfaces.includes(sf) ? (
                  <Typography.Text type="secondary">—</Typography.Text>
                ) : (
                  <Checkbox
                    // Visible but inert while the workflow is off, so the row still shows what it
                    // would offer.
                    disabled={granted[tg.id] == null}
                    checked={(granted[tg.id] ?? []).includes(sf)}
                    onChange={(e) =>
                      setGranted((g) => {
                        const cur = g[tg.id] ?? []
                        return {
                          ...g,
                          [tg.id]: e.target.checked ? [...cur, sf] : cur.filter((x) => x !== sf),
                        }
                      })
                    }
                  />
                ),
              }
            }),
          ]}
        />
        </div>
      )}
      </fieldset>
    </Card>
  )
}
