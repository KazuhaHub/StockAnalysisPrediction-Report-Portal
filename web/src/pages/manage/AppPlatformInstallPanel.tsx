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
type Review = { plan_id: string; plan_sha256: string; expires_ms: string; review: { app_id: string; publisher_id: string; release_sha256: string; declined_capabilities: string[]; audience: Audience[] } }
type Progress = { operation_id: string; installation_id: string; stage: string; terminal: boolean; control_paused: boolean; error_code?: string }
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
  const generation = useRef(0)
  const directoryGeneration = useRef(0)
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
        if (!result.terminal && !result.control_paused) timer = setTimeout(poll, 2000)
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

  const preview = async (release: Release) => {
    if (writing.current || marketBusy || intent) return
    writing.current = true; setBusy(true); setActionError('')
    const account = user
    const epoch = generation.current
    const audience: Audience[] = closed ? [] : selected.map(value => value.startsWith('user:') ? { type: 'user', username: value.slice(5) } : { type: 'ou', group_id: value.slice(3) })
    try {
      const result = await api.post<Review>(`${root}/plans`, { action: 'install', release_sha256: release.release_sha256, trust_revision: release.trust_revision, audience })
      if (result.review.release_sha256 !== release.release_sha256 || result.review.app_id !== release.app_id || !uuid.test(result.plan_id) || !sha.test(result.plan_sha256)) throw new Error(t('appPlatform.invalidOperation'))
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
  return <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
    <Alert type="info" showIcon title={t('appPlatform.development')} description={t('appPlatform.installPreview')} />
    <AppPlatformMarketPanel disabled={busy || !!intent || !!review || loading} onReady={setMarketRelease} onBusyChange={setMarketBusy} />
    <Typography.Title level={5}>{t('appMarket.verifiedReleases')}</Typography.Title>
    {loadError && <Alert type="error" title={loadError} action={<Button disabled={busy || marketBusy} onClick={() => setRefresh(v => v + 1)}>{t('appPlatform.refresh')}</Button>} />}
    {actionError && <Alert type="error" title={actionError} />}
    {intent && <Alert type="warning" title={t('appPlatform.uncertain')} action={<Space><Button loading={busy} onClick={() => void confirm()}>{t('appPlatform.retrySame')}</Button>{definitive && <Button disabled={busy || marketBusy} onClick={clearRejected}>{t('appPlatform.reviewAgain')}</Button>}</Space>} />}
    {operation && <div aria-live="polite">
      <Typography.Title level={5}>{t('appPlatform.progress')}</Typography.Title>
      {progressError && <Alert type="error" title={progressError} />}
      {progress && <Alert type={progress.stage === 'complete' ? 'success' : progress.error_code ? 'error' : 'info'} title={t(`appPlatform.stage.${progress.stage}`, { defaultValue: progress.stage })} description={progress.control_paused ? t('err.app_platform_control_paused') : progress.error_code ? t(`err.${progress.error_code}`) : undefined} />}
      <Space><Button disabled={busy || marketBusy} onClick={() => setRefresh(v => v + 1)}>{t('appPlatform.refresh')}</Button>{progress?.stage === 'complete' && <Link to="/apps">{t('appPlatform.goApps')}</Link>}</Space>
    </div>}
    <Typography.Title level={5}>{t('appPlatform.audience')}</Typography.Title>
    <Select mode="multiple" aria-label={t('appPlatform.audience')} placeholder={t('appPlatform.chooseAudience')} value={selected} options={choices} disabled={closed || busy || marketBusy || !!intent || loading || !!loadError} onChange={setSelected} optionFilterProp="label" maxCount={128} style={{ width: '100%' }} />
    <Checkbox checked={closed} disabled={busy || marketBusy || !!intent} onChange={e => { setClosed(e.target.checked); setSelected([]) }}>{t('appPlatform.noAudience')}</Checkbox>
    <Space>{userCursor && <Button disabled={busy || marketBusy || loading || !!loadError} onClick={() => void loadMore('user')}>{t('appPlatform.moreUsers')}</Button>}{groupCursor && <Button disabled={busy || marketBusy || loading || !!loadError} onClick={() => void loadMore('ou')}>{t('appPlatform.moreGroups')}</Button>}</Space>
    <Table<Release> rowKey="release_sha256" loading={loading} dataSource={marketRelease ? [marketRelease, ...releases.filter(r => r.release_sha256 !== marketRelease.release_sha256)] : releases} pagination={false} locale={{ emptyText: <Empty description={loadError ? t('appPlatform.loadFailed') : t('appPlatform.noReleases')} /> }} columns={[
      { title: t('appPlatform.application'), render: (_, r) => { const copy = appDisplayText(r.app_id, r.display, lang); return <><Typography.Text strong>{copy.name}</Typography.Text><div>{copy.description}</div><Typography.Text type="secondary">{r.publisher_id} · {r.version}</Typography.Text></> } },
      { title: t('appPlatform.action'), render: (_, r) => r.installable ? <Button disabled={loading || busy || marketBusy || !!intent || !!loadError || (!closed && selected.length === 0)} onClick={() => void preview(r)}>{t('appPlatform.review')}</Button> : <Tag>{t(`appPlatform.reason.${r.reason}`)}</Tag> },
    ]} />
    {releaseCursor && <Button disabled={busy || marketBusy || loading || !!loadError} onClick={() => void loadMore('release')}>{t('appPlatform.moreReleases')}</Button>}
    <Modal open={!!review && !intent} title={t('appPlatform.confirmTitle')} confirmLoading={busy} okText={t('appPlatform.confirm')} cancelText={t('appPlatform.cancel')} okButtonProps={{ disabled: expired }} cancelButtonProps={{ disabled: busy }} mask={{ closable: false }} closable={!busy} onCancel={() => { if (!busy) setReview(null) }} onOk={() => void confirm()}>
      {review && <Space orientation="vertical" style={{ width: '100%' }}><Descriptions column={1} items={[
        { key: 'app', label: t('appPlatform.application'), children: `${appDisplayText(review.review.app_id, [...(marketRelease ? [marketRelease] : []), ...releases].find(r => r.release_sha256 === review.review.release_sha256)?.display, lang).name} · ${[...(marketRelease ? [marketRelease] : []), ...releases].find(r => r.release_sha256 === review.review.release_sha256)?.version ?? ''}` },
        { key: 'publisher', label: t('appPlatform.publisher'), children: review.review.publisher_id },
        { key: 'audience', label: t('appPlatform.audience'), children: review.review.audience.length ? review.review.audience.map(audienceLabel).join('、') : t('appPlatform.noAudience') },
        { key: 'permissions', label: t('appPlatform.permissions'), children: review.review.declined_capabilities.length ? review.review.declined_capabilities.map(capability => t(`appPlatform.capability.${capability}`)).join('、') : t('appPlatform.noPermissions') },
        { key: 'data', label: t('appPlatform.storage'), children: t('appPlatform.noData') },
      ]} /><Alert type={expired ? 'warning' : 'info'} title={t(expired ? 'err.app_platform_plan_expired' : 'appPlatform.confirmHint')} /></Space>}
    </Modal>
  </Space>
}
