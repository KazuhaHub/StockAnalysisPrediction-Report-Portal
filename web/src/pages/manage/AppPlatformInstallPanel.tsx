import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Checkbox, Descriptions, Empty, Modal, Select, Space, Table, Tag, Typography } from 'antd'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errText } from '../../api/client'
import { useAuth } from '../../auth'
import { usePrefs } from '../../prefs'
import { appDisplayText } from '../../lib/appDisplayV2'
import type { PlatformRelease } from '../../api/appPlatform'
import AppPlatformMarketPanel from './AppPlatformMarketPanel'

type Audience = { type: 'user'; username: string } | { type: 'ou'; group_id: string }
type Release = PlatformRelease
type Subject = { id: string; name: string }
type Page<T> = { items: T[]; next_cursor: string }
type Commit = { plan_id: string; plan_sha256: string; idempotency_key: string }
type InstallReview = { action: 'install'; app_id: string; publisher_id: string; release_sha256: string; declined_capabilities: string[]; audience: Audience[] }
type UpdateReview = {
  action: 'update'
  installation_id: string
  app_id: string
  publisher_id: string
  source_release_sha256: string
  release_sha256: string
  source_generation: string
  target_generation: string
  policy_revision: string
  target_policy_revision: string
  declined_capabilities: string[]
  previous_capabilities: string[]
  target_capabilities: string[]
  declared_data_schema: string
  grants_sha256: string
}
type UninstallReview = {
  action: 'uninstall'
  installation_id: string
  app_id: string
  publisher_id: string
  release_sha256: string
  generation: string
  target_generation: string
  managed: boolean
  data_effect: string
  plugin_data_cleared: boolean
  managed_data_cleared: boolean
}
type DisableReview = {
  action: 'disable'
  installation_id: string
  app_id: string
  publisher_id: string
  release_sha256: string
  generation: string
  target_generation: string
  managed: boolean
  data_effect: string
}
type EnableReview = {
  action: 'enable'
  installation_id: string
  app_id: string
  publisher_id: string
  release_sha256: string
  source_generation: string
  target_generation: string
  policy_revision: string
  grants_sha256: string
  data_effect: string
}
type Review = { plan_id: string; plan_sha256: string; expires_ms: string; review: InstallReview | UpdateReview | UninstallReview | DisableReview | EnableReview }
type Progress = { operation_id: string; installation_id: string; action: string; stage: string; terminal: boolean; control_paused: boolean; error_code?: string }
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const sha = /^[0-9a-f]{64}$/
const root = '/api/admin/app-platform'

function readIntent(key: string): Commit | null {
  try {
    const value = JSON.parse(sessionStorage.getItem(key) ?? 'null') as Commit | null
    return value && uuid.test(value.plan_id) && sha.test(value.plan_sha256) && uuid.test(value.idempotency_key) ? value : null
  } catch { return null }
}

export default function AppPlatformInstallPanel() {
  const { t } = useTranslation()
  const { user } = useAuth()
  const { lang } = usePrefs()
  const [params, setParams] = useSearchParams()
  const operation = params.get('operation') ?? ''
  const storageKey = `portal.app-install.pending:${user ?? ''}`
  const owner = useRef(user)
  owner.current = user
  const mounted = useRef(false)
  const writing = useRef(false)
  const reviewFocus = useRef<HTMLDivElement>(null)
  const reviewTrigger = useRef<HTMLElement | null>(null)
  const generation = useRef(0)
  const directoryGeneration = useRef(0)
  const terminalRefresh = useRef('')
  const [releases, setReleases] = useState<Release[]>([])
  const [releaseCursor, setReleaseCursor] = useState('')
  const [users, setUsers] = useState<Subject[]>([])
  const [groups, setGroups] = useState<Subject[]>([])
  const [userCursor, setUserCursor] = useState('')
  const [groupCursor, setGroupCursor] = useState('')
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [actionError, setActionError] = useState('')
  const [busy, setBusy] = useState(false)
  const [marketBusy, setMarketBusy] = useState(false)
  const [marketRelease, setMarketRelease] = useState<Release | null>(null)
  const [selected, setSelected] = useState<string[]>([])
  const [closed, setClosed] = useState(false)
  const [review, setReview] = useState<Review | null>(null)
  const [expired, setExpired] = useState(false)
  const [intent, setIntent] = useState<Commit | null>(null)
  const [progress, setProgress] = useState<Progress | null>(null)
  const [progressError, setProgressError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [definitive, setDefinitive] = useState(false)
  const live = (account: string | null, epoch: number) => mounted.current && owner.current === account && generation.current === epoch

  useEffect(() => {
    mounted.current = true
    const epoch = generation.current + 1
    generation.current = epoch
    writing.current = false; setBusy(false); setDefinitive(false)
    setIntent(readIntent(storageKey))
    setReview(null); setActionError(''); setSelected([]); setClosed(false); setMarketRelease(null); setMarketBusy(false)
    return () => { mounted.current = false; generation.current = epoch + 1 }
  }, [storageKey])

  useEffect(() => {
    const abort = new AbortController()
    directoryGeneration.current++
    setLoading(true); setLoadError('')
    Promise.all([
      api.get<Page<Release>>(`${root}/releases`, { signal: abort.signal }),
      api.get<Page<Subject>>(`${root}/subjects?kind=user`, { signal: abort.signal }),
      api.get<Page<Subject>>(`${root}/subjects?kind=ou`, { signal: abort.signal }),
    ]).then(([r, u, g]) => {
      if (abort.signal.aborted) return
      setReleases(r.items); setReleaseCursor(r.next_cursor)
      setUsers(u.items); setUserCursor(u.next_cursor)
      setGroups(g.items); setGroupCursor(g.next_cursor)
    }).catch(e => { if (!abort.signal.aborted) setLoadError(errText(e, t)) })
      .finally(() => { if (!abort.signal.aborted) setLoading(false) })
    return () => abort.abort()
  }, [user, refresh, t])

  useEffect(() => {
    setProgress(null); setProgressError('')
    if (!operation) return
    if (!uuid.test(operation)) { setProgressError(t('appPlatform.invalidOperation')); return }
    const abort = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const poll = async () => {
      try {
        const result = await api.get<Progress>(`${root}/operations/${operation}`, { signal: abort.signal })
        if (abort.signal.aborted) return
        if (result.operation_id !== operation) throw new Error(t('appPlatform.invalidOperation'))
        setProgress(result)
        if (result.terminal && terminalRefresh.current !== operation) {
          terminalRefresh.current = operation
          setRefresh(v => v + 1)
        } else if (!result.terminal && !result.control_paused) timer = setTimeout(poll, 2000)
      } catch (e) { if (!abort.signal.aborted) setProgressError(errText(e, t)) }
    }
    void poll()
    return () => { abort.abort(); clearTimeout(timer) }
  }, [operation, user, refresh, t])

  useEffect(() => {
    setExpired(false)
    if (!review) return
    const timer = setTimeout(() => setExpired(true), Math.max(0, Number(review.expires_ms) - Date.now()))
    return () => clearTimeout(timer)
  }, [review])

  useEffect(() => {
    const timer = setTimeout(() => {
      if (review && !intent) {
        reviewFocus.current?.focus()
      } else if (reviewTrigger.current?.isConnected) {
        reviewTrigger.current.focus()
        reviewTrigger.current = null
      }
    }, 0)
    return () => clearTimeout(timer)
  }, [review, intent])

  const loadMore = async (kind: 'release' | 'user' | 'ou') => {
    if (writing.current || marketBusy || loading || loadError) return
    writing.current = true; setBusy(true); setActionError('')
    const account = user
    const epoch = generation.current
    const directoryEpoch = directoryGeneration.current
    const cursor = kind === 'release' ? releaseCursor : kind === 'user' ? userCursor : groupCursor
    try {
      const page = await api.get<Page<Release | Subject>>(kind === 'release' ? `${root}/releases?after=${encodeURIComponent(cursor)}` : `${root}/subjects?kind=${kind}&after=${encodeURIComponent(cursor)}`)
      if (!live(account, epoch) || directoryGeneration.current !== directoryEpoch) return
      if (kind === 'release') { setReleases(v => [...v, ...page.items as Release[]]); setReleaseCursor(page.next_cursor) }
      else if (kind === 'user') { setUsers(v => [...v, ...page.items as Subject[]]); setUserCursor(page.next_cursor) }
      else { setGroups(v => [...v, ...page.items as Subject[]]); setGroupCursor(page.next_cursor) }
    } catch (e) { if (live(account, epoch) && directoryGeneration.current === directoryEpoch) setActionError(errText(e, t)) }
    finally { if (live(account, epoch)) { writing.current = false; setBusy(false) } }
  }

  const preview = async (release: Release, action: 'install' | 'update' | 'enable' | 'disable' | 'uninstall') => {
    if (writing.current || marketBusy || intent) return
    if (action !== 'install' && !release.installation_id) return
    reviewTrigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    writing.current = true; setBusy(true); setActionError('')
    const account = user
    const epoch = generation.current
    const audience: Audience[] = closed ? [] : selected.map(value => value.startsWith('user:') ? { type: 'user', username: value.slice(5) } : { type: 'ou', group_id: value.slice(3) })
    try {
      const body = action === 'uninstall' || action === 'disable'
        ? { action, installation_id: release.installation_id }
        : action === 'enable'
        ? { action, installation_id: release.installation_id }
        : action === 'update'
        ? { action, release_sha256: release.release_sha256, trust_revision: release.trust_revision, installation_id: release.installation_id }
        : { action, release_sha256: release.release_sha256, trust_revision: release.trust_revision, audience }
      const result = await api.post<Review>(`${root}/plans`, body)
      const returnedInstallation = result.review.action === 'install' ? '' : result.review.installation_id
      if (result.review.action !== action || result.review.release_sha256 !== release.release_sha256 || result.review.app_id !== release.app_id || (action !== 'install' && returnedInstallation !== release.installation_id) || !uuid.test(result.plan_id) || !sha.test(result.plan_sha256)) throw new Error(t('appPlatform.invalidOperation'))
      if (live(account, epoch)) { setReview(result); setDefinitive(false) }
    } catch (e) { if (live(account, epoch)) setActionError(errText(e, t)) }
    finally { if (live(account, epoch)) { writing.current = false; setBusy(false) } }
  }

  const confirm = async () => {
    if (writing.current || marketBusy || (!intent && (!review || expired))) return
    const request = intent ?? { plan_id: review!.plan_id, plan_sha256: review!.plan_sha256, idempotency_key: crypto.randomUUID() }
    writing.current = true; setBusy(true); setActionError('')
    const account = user
    const epoch = generation.current
    try {
      // Persist only the already-confirmed request identity before transmitting.
      // A lost response/reload must retry this exact operation, even after expiry.
      sessionStorage.setItem(storageKey, JSON.stringify(request))
      setIntent(request)
      const result = await api.post<{ operation_id: string }>(`${root}/operations`, request)
      if (!live(account, epoch)) return
      if (!uuid.test(result.operation_id)) throw new Error(t('appPlatform.invalidOperation'))
      sessionStorage.removeItem(storageKey)
      setIntent(null); setReview(null)
      setParams(p => { p.set('operation', result.operation_id); return p }, { replace: true })
      setMarketRelease(null)
      setRefresh(v => v + 1)
    } catch (e) {
      if (live(account, epoch)) {
        setActionError(errText(e, t))
        setDefinitive(e instanceof ApiError && [400, 403, 409].includes(e.status))
      }
    } finally { if (live(account, epoch)) { writing.current = false; setBusy(false) } }
  }

  const clearRejected = () => {
    if (!definitive || busy) return
    sessionStorage.removeItem(storageKey); setIntent(null); setReview(null); setDefinitive(false); setActionError('')
  }
  const choices = [...users.map(u => ({ value: `user:${u.id}`, label: `${t('appPlatform.user')}: ${u.name} (${u.id})` })), ...groups.map(g => ({ value: `ou:${g.id}`, label: `${t('appPlatform.ou')}: ${g.name} (${g.id})` }))]
  const audienceLabel = (target: Audience) => target.type === 'user' ? `${t('appPlatform.user')}: ${target.username}` : `${t('appPlatform.ou')}: ${groups.find(g => g.id === target.group_id)?.name ?? target.group_id}`
  const capabilityLabel = (capability: string) => t(`appPlatform.capability.${capability}`, { defaultValue: capability })
  const updateReview = review?.review.action === 'update' ? review.review : null
  const enableReview = review?.review.action === 'enable' ? review.review : null
  const uninstallReview = review?.review.action === 'uninstall' ? review.review : null
  const disableReview = review?.review.action === 'disable' ? review.review : null
  const updateDiff = updateReview ? (() => {
    const previous = updateReview.previous_capabilities ?? []
    const target = updateReview.target_capabilities ?? []
    return {
      added: target.filter(capability => !previous.includes(capability)),
      removed: previous.filter(capability => !target.includes(capability)),
    }
  })() : null
  const reviewRelease = review ? [...(marketRelease ? [marketRelease] : []), ...releases].find(r => r.release_sha256 === review.review.release_sha256) : undefined
  const permissionList = (values?: string[]) => values?.length ? values.map(capabilityLabel).join('、') : t('appPlatform.none')
  const compatibility = reviewRelease?.compatibility ? `${reviewRelease.compatibility.platform_api} · ${reviewRelease.compatibility.runner_protocol}` : '—'
  const resources = reviewRelease?.backend && reviewRelease.resources ? t('appPlatform.resourceSummary', {
    platforms: reviewRelease.platforms?.length ? reviewRelease.platforms.join(', ') : t('appPlatform.none'),
    memory: reviewRelease.resources.memory_mib,
    cpu: reviewRelease.resources.cpu_millis,
    pids: reviewRelease.resources.pids,
  }) : reviewRelease ? t('appPlatform.frontendOnly') : '—'
  const declined = review?.review.action === 'install' ? review.review.declined_capabilities : review?.review.action === 'update' ? review.review.declined_capabilities : []
  const lifecycleTitle = review?.review.action === 'update'
    ? 'appPlatform.confirmUpdateTitle'
    : review?.review.action === 'disable'
    ? 'appPlatform.confirmDisableTitle'
    : review?.review.action === 'uninstall'
    ? 'appPlatform.confirmUninstallTitle'
    : review?.review.action === 'enable'
    ? 'appPlatform.confirmEnableTitle'
    : 'appPlatform.confirmTitle'
  const lifecycleOK = review?.review.action === 'update'
    ? 'appPlatform.confirmUpdate'
    : review?.review.action === 'disable'
    ? 'appPlatform.confirmDisable'
    : review?.review.action === 'uninstall'
    ? 'appPlatform.confirmUninstall'
    : review?.review.action === 'enable'
    ? 'appPlatform.confirmEnable'
    : 'appPlatform.confirm'
  const progressType = progress?.control_paused
    ? 'warning'
    : progress?.terminal && ['failed', 'recovery_required'].includes(progress.stage)
    ? 'error'
    : progress?.stage === 'complete'
    ? 'success'
    : 'info'
  return <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
    <Alert type="info" showIcon title={t('appPlatform.development')} description={t('appPlatform.installPreview')} />
    <AppPlatformMarketPanel disabled={busy || !!intent || !!review || loading} onReady={setMarketRelease} onBusyChange={setMarketBusy} />
    <Typography.Title level={5}>{t('appMarket.verifiedReleases')}</Typography.Title>
    {loadError && <Alert type="error" title={loadError} action={<Button disabled={busy || marketBusy} onClick={() => setRefresh(v => v + 1)}>{t('appPlatform.refresh')}</Button>} />}
    {actionError && <Alert type="error" title={actionError} />}
    {intent && <Alert type="warning" title={t('appPlatform.uncertain')} action={<Space><Button loading={busy} onClick={() => void confirm()}>{t('appPlatform.retrySame')}</Button>{definitive && <Button disabled={busy || marketBusy} onClick={clearRejected}>{t('appPlatform.reviewAgain')}</Button>}</Space>} />}
    {operation && <div role="status" aria-live="polite">
      <Typography.Title level={5}>{t('appPlatform.progress')}</Typography.Title>
      {progressError && <Alert type="error" title={progressError} />}
      {progress && <Alert
        type={progressType}
        title={progress.control_paused
          ? t('err.app_platform_control_paused')
          : t(`appPlatform.stage.${progress.stage}`, { defaultValue: progress.stage })}
        description={progress.error_code && !progress.control_paused
          ? t(`err.${progress.error_code}`, { defaultValue: t('appPlatform.operationNeedsReview') })
          : undefined}
      />}
      <Space><Button disabled={busy || marketBusy} onClick={() => setRefresh(v => v + 1)}>{t('appPlatform.refresh')}</Button>{progress?.stage === 'complete' && <Link to="/apps">{t('appPlatform.goApps')}</Link>}</Space>
    </div>}
    <Typography.Title level={5}>{t('appPlatform.audience')}</Typography.Title>
    <Select mode="multiple" aria-label={t('appPlatform.audience')} placeholder={t('appPlatform.chooseAudience')} value={selected} options={choices} disabled={closed || busy || marketBusy || !!intent || loading || !!loadError} onChange={setSelected} optionFilterProp="label" maxCount={128} style={{ width: '100%' }} />
    <Checkbox checked={closed} disabled={busy || marketBusy || !!intent} onChange={e => { setClosed(e.target.checked); setSelected([]) }}>{t('appPlatform.noAudience')}</Checkbox>
    <Space>{userCursor && <Button disabled={busy || marketBusy || loading || !!loadError} onClick={() => void loadMore('user')}>{t('appPlatform.moreUsers')}</Button>}{groupCursor && <Button disabled={busy || marketBusy || loading || !!loadError} onClick={() => void loadMore('ou')}>{t('appPlatform.moreGroups')}</Button>}</Space>
    <div className="rp-table-scroll rp-platform-release-table">
    <Table<Release> rowKey="release_sha256" loading={loading} dataSource={marketRelease ? [marketRelease, ...releases.filter(r => r.release_sha256 !== marketRelease.release_sha256)] : releases} pagination={false} locale={{ emptyText: <Empty description={loadError ? t('appPlatform.loadFailed') : t('appPlatform.noReleases')} /> }} columns={[
      { title: t('appPlatform.application'), render: (_, r) => { const copy = appDisplayText(r.app_id, r.display, lang); return <><Typography.Text strong>{copy.name}</Typography.Text><div>{copy.description}</div><Typography.Text type="secondary">{r.publisher_id} · {r.version}</Typography.Text></> } },
      { title: t('appPlatform.action'), render: (_, r) => r.installed ? <Space wrap><Button disabled={loading || busy || marketBusy || !!intent || !!loadError} onClick={() => void preview(r, 'disable')}>{t('appPlatform.reviewDisable')}</Button><Button danger disabled={loading || busy || marketBusy || !!intent || !!loadError} onClick={() => void preview(r, 'uninstall')}>{t('appPlatform.reviewUninstall')}</Button></Space> : r.enableable ? <Button disabled={loading || busy || marketBusy || !!intent || !!loadError} onClick={() => void preview(r, 'enable')}>{t('appPlatform.reviewEnable')}</Button> : r.installable ? <Button disabled={loading || busy || marketBusy || !!intent || !!loadError || (!closed && selected.length === 0)} onClick={() => void preview(r, 'install')}>{t('appPlatform.review')}</Button> : r.updateable ? <Button disabled={loading || busy || marketBusy || !!intent || !!loadError} onClick={() => void preview(r, 'update')}>{t('appPlatform.reviewUpdate')}</Button> : <Tag>{t(`appPlatform.reason.${r.reason}`)}</Tag> },
    ]} />
    </div>
    {releaseCursor && <Button disabled={busy || marketBusy || loading || !!loadError} onClick={() => void loadMore('release')}>{t('appPlatform.moreReleases')}</Button>}
    <Modal className="rp-platform-review-modal" open={!!review && !intent} title={t(lifecycleTitle)} confirmLoading={busy} okText={t(lifecycleOK)} cancelText={t('appPlatform.cancel')} okButtonProps={{ danger: review?.review.action === 'uninstall', disabled: expired }} cancelButtonProps={{ disabled: busy }} mask={{ closable: false }} closable={!busy} onCancel={() => { if (!busy) setReview(null) }} onOk={() => void confirm()}>
      {review && <div ref={reviewFocus} role="document" tabIndex={0} className="rp-platform-review rp-platform-review-scroll"><Space orientation="vertical" style={{ width: '100%' }}><Descriptions column={1} items={[
        { key: 'action', label: t('appPlatform.action'), children: t(review.review.action === 'update' ? 'appPlatform.update' : review.review.action === 'disable' ? 'appPlatform.disable' : review.review.action === 'uninstall' ? 'appPlatform.uninstall' : review.review.action === 'enable' ? 'appPlatform.enable' : 'appPlatform.install') },
        { key: 'app', label: t('appPlatform.application'), children: `${appDisplayText(review.review.app_id, reviewRelease?.display, lang).name} · ${reviewRelease?.version ?? ''}` },
        { key: 'publisher', label: t('appPlatform.publisher'), children: review.review.publisher_id },
        { key: 'release', label: t('appPlatform.releaseDigest'), children: <Typography.Text code copyable style={{ wordBreak: 'break-all' }}>{review.review.release_sha256}</Typography.Text> },
        { key: 'compatibility', label: t('appPlatform.compatibility'), children: compatibility },
        { key: 'resources', label: t('appPlatform.resources'), children: resources },
        ...(review.review.action === 'install' ? [{ key: 'audience', label: t('appPlatform.audience'), children: review.review.audience.length ? review.review.audience.map(audienceLabel).join('、') : t('appPlatform.noAudience') }] : review.review.action === 'update' || review.review.action === 'enable' ? [{ key: 'generation', label: t('appPlatform.generation'), children: `${review.review.source_generation} → ${review.review.target_generation}` }] : [{ key: 'generation', label: t('appPlatform.generation'), children: `${review.review.generation} → ${review.review.target_generation}` }]),
        ...(uninstallReview ? [] : [
          { key: 'requiredPermissions', label: t('appPlatform.requiredPermissions'), children: permissionList(reviewRelease?.required_capabilities) },
          { key: 'optionalPermissions', label: t('appPlatform.optionalPermissions'), children: permissionList(reviewRelease?.optional_capabilities) },
          ...(declined.length ? [{ key: 'declinedPermissions', label: t('appPlatform.declinedPermissions'), children: permissionList(declined) }] : []),
          ...(updateDiff ? [{ key: 'permissionChanges', label: t('appPlatform.permissionDiffLabel'), children: t('appPlatform.permissionDiff', { added: permissionList(updateDiff.added), removed: permissionList(updateDiff.removed) }) }] : []),
        ]),
        { key: 'data', label: t('appPlatform.storage'), children: uninstallReview ? t(uninstallReview.managed_data_cleared ? 'appPlatform.uninstallManagedData' : 'appPlatform.uninstallPluginData') : disableReview ? t('appPlatform.disableDataRetained') : review.review.action === 'update' ? t('appPlatform.dataSchemaRetained', { schema: review.review.declared_data_schema }) : enableReview ? t('appPlatform.enableDataRetained') : t('appPlatform.noData') },
      ]} /><Alert type={expired ? 'warning' : 'info'} title={t(expired ? 'err.app_platform_plan_expired' : uninstallReview ? 'appPlatform.uninstallConfirmHint' : disableReview ? 'appPlatform.disableConfirmHint' : review.review.action === 'update' ? 'appPlatform.updateConfirmHint' : enableReview ? 'appPlatform.enableConfirmHint' : 'appPlatform.confirmHint')} /></Space></div>}
    </Modal>
  </Space>
}
