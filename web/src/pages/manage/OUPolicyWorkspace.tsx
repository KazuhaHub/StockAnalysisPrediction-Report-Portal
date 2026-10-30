import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Button, Typography } from 'antd'
import { GlobalOutlined, LeftOutlined, RightOutlined } from '@ant-design/icons'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import { settleOnAnswer, type LoadOpts } from '../../lib/loadFailure'
import type { UserGroupRow } from '../../api/types'
import LoadGate from '../../components/LoadGate'
import OrgUnitPicker from './OrgUnitPicker'
import OrgUnitDetail, { type OUPolicySection } from './OrgUnitDetail'

export type FeaturePolicySection = Exclude<OUPolicySection, 'directory'>

/** The persistent scope rail sits between primary navigation and the feature's editor. */
export function PolicyPage({ section, children, renderOU }: { section: FeaturePolicySection; children: ReactNode; renderOU?: (group: UserGroupRow, groups: UserGroupRow[]) => ReactNode }) {
  const { t } = useTranslation()
  const { catchLoad, retryKey } = useLoadFailure()
  const [params, setParams] = useSearchParams()
  const [collapsed, setCollapsed] = useState(false)
  const [groups, setGroups] = useState<UserGroupRow[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const load = (opts?: LoadOpts) => {
    setError('')
    return api.get<{ groups: UserGroupRow[] }>('/api/admin/groups')
      .then((result) => setGroups(result.groups ?? []))
      .then(...settleOnAnswer(() => setLoading(false), opts)).catch(catchLoad(setError, opts))
  }
  useEffect(() => { void load() }, [retryKey]) // eslint-disable-line react-hooks/exhaustive-deps
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
  return <div className={`rp-policy-workspace ${collapsed ? 'rp-policy-workspace--collapsed' : ''}`} key={params.toString()}>
    <aside className="rp-policy-scope" aria-label={t('ou.unitScope')}>
      <div className="rp-policy-scope-header"><Button aria-label={t('ou.globalScope')} title={t('ou.globalScope')} icon={<GlobalOutlined />} block={!collapsed} type={scoped ? 'text' : 'primary'}
        aria-current={!scoped ? 'true' : undefined} onClick={() => selectScope()}>{!collapsed && t('ou.globalScope')}</Button>
      <Button icon={collapsed ? <RightOutlined /> : <LeftOutlined />} aria-label={t(collapsed ? 'ou.expand' : 'ou.collapse')} title={t(collapsed ? 'ou.expand' : 'ou.collapse')} onClick={() => setCollapsed(value => !value)} /></div>
      {!collapsed && <>
      <LoadGate loading={loading} error={error} onRetry={load}>
        <OrgUnitPicker collapsible={false} mode="manage" groups={groups} unassigned={0} scoped onScopedChange={() => {}}
          selected={scoped && selected ? [selected.id] : []} onManage={() => {}}
          onSelect={(ids) => { if (ids[0]) selectScope(ids[0]) }} />
      </LoadGate>
      <Typography.Paragraph type="secondary">{t('ou.featureScopeHint')}</Typography.Paragraph>
      </>}
    </aside>
    <div className="rp-policy-content">
      {scoped ? <LoadGate loading={loading} error={error} onRetry={load}>
        {selected ? renderOU ? renderOU(selected, groups) : <OrgUnitDetail key={`${section}:${selected.id}`} sections={[section]} group={selected} groups={groups}
          onChanged={() => { void load({ quiet: true }) }} onDeleted={() => {}} /> : <Alert type="warning" showIcon title={t('ou.scopeMissing')} />}
      </LoadGate> : children}
    </div>
  </div>
}
