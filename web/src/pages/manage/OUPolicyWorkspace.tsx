import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Tabs, Typography } from 'antd'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { UserGroupRow } from '../../api/types'
import LoadGate from '../../components/LoadGate'
import OrgUnitPicker from './OrgUnitPicker'
import OrgUnitDetail, { type OUPolicySection } from './OrgUnitDetail'

export type FeaturePolicySection = Exclude<OUPolicySection, 'directory'>

/** Each feature owns its OU policy; the directory owns only structure and membership. */
export default function OUPolicyWorkspace({ section }: { section: FeaturePolicySection }) {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  const [groups, setGroups] = useState<UserGroupRow[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const load = () => {
    setLoading(true); setError('')
    return api.get<{ groups: UserGroupRow[] }>('/api/admin/groups')
      .then((result) => setGroups(result.groups ?? []))
      .catch((e) => setError(errText(e, t))).finally(() => setLoading(false))
  }
  useEffect(() => { void load() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const requested = params.get('ou')
  const selected = requested !== null ? groups.find((group) => group.id === Number(requested) && Number(requested) > 0) : groups.find((group) => group.is_default)
  return <LoadGate loading={loading} error={error} onRetry={load}>
    <Typography.Paragraph type="secondary">{t('ou.featureScopeHint')}</Typography.Paragraph>
    <div className="rp-ou-console">
      <OrgUnitPicker mode="manage" groups={groups} unassigned={0} scoped onScopedChange={() => {}}
        selected={selected ? [selected.id] : []} onManage={() => {}}
        onSelect={(ids) => {
          if (!ids[0] || ids[0] === selected?.id) return
          const next = new URLSearchParams(params)
          next.set('scope', 'ou'); next.set('ou', String(ids[0])); setParams(next)
        }} />
      {selected ? <OrgUnitDetail key={`${section}:${selected.id}`} sections={[section]} group={selected} groups={groups}
        onChanged={() => { void load() }} onDeleted={() => {}} /> : <Alert type="warning" showIcon title={t('ou.scopeMissing')} />}
    </div>
  </LoadGate>
}

export function PolicyPage({ section, children }: { section: FeaturePolicySection; children: ReactNode }) {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  return <Tabs key={params.toString()} activeKey={params.get('scope') === 'ou' ? 'ou' : 'global'}
    onChange={(scope) => {
      const next = new URLSearchParams(params)
      next.set('scope', scope); setParams(next)
    }} items={[
      { key: 'global', label: t('ou.globalScope'), children },
      { key: 'ou', label: t('ou.unitScope'), children: <OUPolicyWorkspace section={section} /> },
    ]} />
}
