import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Button, Typography } from 'antd'
import { GlobalOutlined } from '@ant-design/icons'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { UserGroupRow } from '../../api/types'
import LoadGate from '../../components/LoadGate'
import OrgUnitPicker from './OrgUnitPicker'
import OrgUnitDetail, { type OUPolicySection } from './OrgUnitDetail'

export type FeaturePolicySection = Exclude<OUPolicySection, 'directory'>

/** The persistent scope rail sits between primary navigation and the feature's editor. */
export function PolicyPage({ section, children }: { section: FeaturePolicySection; children: ReactNode }) {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  const [groups, setGroups] = useState<UserGroupRow[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const load = () => {
    setError('')
    return api.get<{ groups: UserGroupRow[] }>('/api/admin/groups')
      .then((result) => setGroups(result.groups ?? []))
      .catch((e) => setError(errText(e, t))).finally(() => setLoading(false))
  }
  useEffect(() => { void load() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const scoped = params.get('scope') === 'ou'
  const requested = params.get('ou')
  const selected = requested !== null ? groups.find((group) => group.id === Number(requested) && Number(requested) > 0) : groups.find((group) => group.is_default)
  const selectScope = (id?: number) => {
    if ((id === undefined && !scoped) || (scoped && id !== undefined && id === selected?.id)) return
    const next = new URLSearchParams(params)
    next.set('scope', id === undefined ? 'global' : 'ou')
    if (id !== undefined) next.set('ou', String(id))
    if (next.toString() !== params.toString()) setParams(next)
  }
  return <div className="rp-policy-workspace" key={params.toString()}>
    <aside className="rp-policy-scope" aria-label={t('ou.unitScope')}>
      <Button icon={<GlobalOutlined />} block type={scoped ? 'text' : 'primary'}
        aria-current={!scoped ? 'true' : undefined} onClick={() => selectScope()}>{t('ou.globalScope')}</Button>
      <LoadGate loading={loading} error={error} onRetry={load}>
        <OrgUnitPicker mode="manage" groups={groups} unassigned={0} scoped onScopedChange={() => {}}
          selected={scoped && selected ? [selected.id] : []} onManage={() => {}}
          onSelect={(ids) => { if (ids[0]) selectScope(ids[0]) }} />
      </LoadGate>
      <Typography.Paragraph type="secondary">{t('ou.featureScopeHint')}</Typography.Paragraph>
    </aside>
    <div className="rp-policy-content">
      {scoped ? <LoadGate loading={loading} error={error} onRetry={load}>
        {selected ? <OrgUnitDetail key={`${section}:${selected.id}`} sections={[section]} group={selected} groups={groups}
          onChanged={() => { void load() }} onDeleted={() => {}} /> : <Alert type="warning" showIcon title={t('ou.scopeMissing')} />}
      </LoadGate> : children}
    </div>
  </div>
}
