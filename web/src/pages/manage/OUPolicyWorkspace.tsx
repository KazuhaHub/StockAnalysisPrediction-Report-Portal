import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Alert, Button, Typography } from 'antd'
import { GlobalOutlined, LeftOutlined, RightOutlined } from '@ant-design/icons'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import { settleOnAnswer, type LoadOpts } from '../../lib/loadFailure'
import type { UserGroupRow } from '../../api/types'
import { ScopeFoldToggle } from './ScopeFold'
import { useNarrow } from '../../lib/useNarrow'
import LoadGate from '../../components/LoadGate'
import OrgUnitPicker from './OrgUnitPicker'
import OrgUnitDetail, { type OUPolicySection } from './OrgUnitDetail'

const TREE_ID = 'rp-policy-scope-tree'

export type FeaturePolicySection = Exclude<OUPolicySection, 'directory'>

/** The persistent scope rail sits between primary navigation and the feature's editor. */
export function PolicyPage({ section, children, renderOU }: { section: FeaturePolicySection; children: ReactNode; renderOU?: (group: UserGroupRow, groups: UserGroupRow[]) => ReactNode }) {
  const { t } = useTranslation()
  const { catchLoad, retryKey } = useLoadFailure()
  const [params, setParams] = useSearchParams()
  const phone = useNarrow()
  // Desktop collapses the rail to an icon column; a phone folds the tree away instead and keeps it
  // closed until asked, so the stacked tree never pushes the editor off the first screen.
  const [deskCollapsed, setDeskCollapsed] = useState(false)
  const [treeOpen, setTreeOpen] = useState(false)
  const collapsed = phone ? !treeOpen : deskCollapsed
  // A pick that changes the URL remounts the workspace (its key follows the URL), so the toggle that
  // had focus is gone; the replacement takes focus when it mounts. A pick that changes nothing keeps
  // the toggle, which is focused directly.
  const toggleEl = useRef<HTMLButtonElement | null>(null)
  // The remount can happen more than once while the new URL settles, so the claim lasts a moment
  // rather than ending at the first mount.
  const focusUntil = useRef(0)
  const takeFocus = useCallback((el: HTMLButtonElement | null) => {
    toggleEl.current = el
    if (el && Date.now() < focusUntil.current) el.focus()
  }, [])
  const fold = (remounts = false) => {
    setTreeOpen(false)
    if (!phone || !treeOpen) return
    if (remounts) focusUntil.current = Date.now() + 1000
    else toggleEl.current?.focus()
  }
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
    // Choosing anything, even the scope already shown, folds the phone tree back up.
    if ((id === undefined && !scoped) || (scoped && id !== undefined && id === selected?.id)) { fold(); return }
    const next = new URLSearchParams(params)
    next.set('scope', id === undefined ? 'global' : 'ou')
    if (id !== undefined) next.set('ou', String(id))
    if (next.toString() !== params.toString()) { fold(true); setParams(next) } else fold()
  }
  return <div className={`rp-policy-workspace ${!phone && collapsed ? 'rp-policy-workspace--collapsed' : ''}`} key={params.toString()}>
    <aside className="rp-policy-scope" aria-label={t('ou.unitScope')}>
      <div className="rp-policy-scope-header"><Button aria-label={t('ou.globalScope')} title={t('ou.globalScope')} icon={<GlobalOutlined />} block={phone || !collapsed} type={scoped ? 'text' : 'primary'}
        aria-current={!scoped ? 'true' : undefined} onClick={() => selectScope()}>{(phone || !collapsed) && t('ou.globalScope')}</Button>
      {phone
        // The visible unit name stays in the accessible name; aria-expanded carries the state.
        ? <ScopeFoldToggle open={treeOpen} label={scoped && selected ? selected.name : t('ou.unitScope')} controls={TREE_ID} buttonRef={takeFocus} onClick={() => setTreeOpen(value => !value)} />
        : <Button icon={collapsed ? <RightOutlined /> : <LeftOutlined />} aria-expanded={!collapsed} aria-label={t(collapsed ? 'ou.expand' : 'ou.collapse')} title={t(collapsed ? 'ou.expand' : 'ou.collapse')} onClick={() => setDeskCollapsed(value => !value)} />}</div>
      <div id={TREE_ID} hidden={collapsed}>{(phone || !collapsed) && <LoadGate loading={loading} error={error} onRetry={load}>
        <OrgUnitPicker collapsible={false} mode="manage" groups={groups} unassigned={0} scoped onScopedChange={() => {}}
          selected={scoped && selected ? [selected.id] : []} onManage={() => {}}
          onSelect={(ids) => { if (ids[0]) selectScope(ids[0]) }} onPick={() => fold()} />
      </LoadGate>}</div>
      {!collapsed && <Typography.Paragraph type="secondary">{t('ou.featureScopeHint')}</Typography.Paragraph>}
    </aside>
    <div className="rp-policy-content">
      {scoped ? <LoadGate loading={loading} error={error} onRetry={load}>
        {selected ? renderOU ? renderOU(selected, groups) : <OrgUnitDetail key={`${section}:${selected.id}`} sections={[section]} group={selected} groups={groups}
          onChanged={() => { void load({ quiet: true }) }} onDeleted={() => {}} /> : <Alert type="warning" showIcon title={t('ou.scopeMissing')} />}
      </LoadGate> : children}
    </div>
  </div>
}
