import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errText } from '../../api/client'
import { useAuth } from '../../auth'
import type { PlatformRelease } from '../../api/appPlatform'

// The review, commit and progress state machine of every platform lifecycle action (install, update, enable,
// disable, uninstall). It owns the safety rules of the original install panel: a review plan is consumed once,
// the confirmed request identity is saved before it is sent so a lost response is retried with the same key, a
// plan expires, and nothing from a previous account or an overtaken request is ever published.

export type LifecycleAction = 'install' | 'update' | 'enable' | 'disable' | 'uninstall'
export type Subject = { id: string; name: string; default?: boolean }
type Page<T> = { items: T[]; next_cursor: string }
type Commit = { plan_id: string; plan_sha256: string; idempotency_key: string }
// What is kept in sessionStorage: the request identity, and what it was for, so a flow resumed after a reload
// still says what it is confirming. Only the three Commit fields are ever sent to the server.
export type PendingIntent = Commit & { action?: LifecycleAction; app_id?: string }
const ACTIONS: readonly string[] = ['install', 'update', 'enable', 'disable', 'uninstall']
export type InstallReview = { action: 'install'; app_id: string; publisher_id: string; release_sha256: string; declined_capabilities: string[]; open_to_ous: string[] }
export type UpdateReview = {
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
  // The quota the update raises before the new release starts (doc 185 section 3.2). All fields are decimal strings.
  data_quota?: {
    current_bytes: string
    current_inodes: string
    requested_bytes: string
    requested_inodes: string
    new_bytes: string
    new_inodes: string
    raise_applied: boolean
    capacity: 'fits' | 'short'
    capacity_dimension?: string
    capacity_requested?: string
    capacity_remaining?: string
  }
}
export type UninstallReview = {
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
export type DisableReview = {
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
export type EnableReview = {
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
export type Review = { plan_id: string; plan_sha256: string; expires_ms: string; review: InstallReview | UpdateReview | UninstallReview | DisableReview | EnableReview }
export type Progress = { operation_id: string; installation_id: string; action: string; stage: string; terminal: boolean; control_paused: boolean; error_code?: string }
export interface LifecycleTarget { release: PlatformRelease; action: LifecycleAction }

export const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const sha = /^[0-9a-f]{64}$/
export const platformRoot = '/api/admin/app-platform'

function readIntent(key: string): PendingIntent | null {
  try {
    const value = JSON.parse(sessionStorage.getItem(key) ?? 'null') as (Commit & { action?: unknown; app_id?: unknown }) | null
    if (!value || !uuid.test(value.plan_id) || !sha.test(value.plan_sha256) || !uuid.test(value.idempotency_key)) return null
    // An entry written before the action was kept has neither: it resumes as a bare request.
    return {
      plan_id: value.plan_id, plan_sha256: value.plan_sha256, idempotency_key: value.idempotency_key,
      ...(typeof value.action === 'string' && ACTIONS.includes(value.action) ? { action: value.action as LifecycleAction } : null),
      ...(typeof value.app_id === 'string' ? { app_id: value.app_id } : null),
    }
  } catch { return null }
}

/** Where a lifecycle flow is: choosing who can use a new app, reviewing the plan, or following the operation. */
export type LifecycleStep = 'audience' | 'review' | 'progress'

export function usePlatformLifecycle(onChange?: () => void) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const [params, setParams] = useSearchParams()
  const operation = params.get('operation') ?? ''
  const storageKey = `portal.app-install.pending:${user ?? ''}`
  const owner = useRef(user)
  owner.current = user
  const mounted = useRef(false)
  const writing = useRef(false)
  const generation = useRef(0)
  const directoryGeneration = useRef(0)
  const terminalSeen = useRef('')
  const trigger = useRef<HTMLElement | null>(null)
  const changed = useRef(onChange)
  changed.current = onChange
  const [target, setTarget] = useState<LifecycleTarget | null>(null)
  const [groups, setGroups] = useState<Subject[]>([])
  const [groupCursor, setGroupCursor] = useState('')
  const [groupsLoading, setGroupsLoading] = useState(false)
  const [groupsError, setGroupsError] = useState('')
  const [actionError, setActionError] = useState('')
  const [busy, setBusy] = useState(false)
  const [selected, setSelected] = useState<string[]>([])
  const [review, setReview] = useState<Review | null>(null)
  const [expired, setExpired] = useState(false)
  const [intent, setIntent] = useState<PendingIntent | null>(null)
  const [progress, setProgress] = useState<Progress | null>(null)
  const [progressError, setProgressError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [definitive, setDefinitive] = useState(false)
  const [hidden, setHidden] = useState(false)
  const live = (account: string | null, epoch: number) => mounted.current && owner.current === account && generation.current === epoch
  const needsGroups = target?.action === 'install'

  useEffect(() => {
    mounted.current = true
    const epoch = generation.current + 1
    generation.current = epoch
    writing.current = false; setBusy(false); setDefinitive(false); setHidden(false)
    setIntent(readIntent(storageKey))
    setReview(null); setActionError(''); setSelected([]); setTarget(null)
    return () => { mounted.current = false; generation.current = epoch + 1 }
  }, [storageKey])

  // The OU directory is only needed while a new app is being opened to some of them.
  useEffect(() => {
    if (!needsGroups) return
    const abort = new AbortController()
    directoryGeneration.current++
    setGroupsLoading(true); setGroupsError('')
    api.get<Page<Subject>>(`${platformRoot}/subjects?kind=ou`, { signal: abort.signal })
      .then(page => { if (!abort.signal.aborted) { setGroups(page.items); setGroupCursor(page.next_cursor) } })
      .catch(e => { if (!abort.signal.aborted) setGroupsError(errText(e, t)) })
      .finally(() => { if (!abort.signal.aborted) setGroupsLoading(false) })
    return () => abort.abort()
  }, [user, needsGroups, t])

  useEffect(() => {
    setProgress(null); setProgressError('')
    if (!operation) return
    if (!uuid.test(operation)) { setProgressError(t('appPlatform.invalidOperation')); return }
    const abort = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const poll = async () => {
      try {
        const result = await api.get<Progress>(`${platformRoot}/operations/${operation}`, { signal: abort.signal })
        if (abort.signal.aborted) return
        if (result.operation_id !== operation) throw new Error(t('appPlatform.invalidOperation'))
        setProgress(result)
        if (result.terminal && terminalSeen.current !== operation) {
          terminalSeen.current = operation
          changed.current?.()
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

  const loadMoreGroups = async () => {
    if (writing.current || groupsLoading || groupsError) return
    writing.current = true; setBusy(true); setActionError('')
    const account = user
    const epoch = generation.current
    const directoryEpoch = directoryGeneration.current
    try {
      const page = await api.get<Page<Subject>>(`${platformRoot}/subjects?kind=ou&after=${encodeURIComponent(groupCursor)}`)
      if (!live(account, epoch) || directoryGeneration.current !== directoryEpoch) return
      setGroups(v => [...v, ...page.items]); setGroupCursor(page.next_cursor)
    } catch (e) { if (live(account, epoch) && directoryGeneration.current === directoryEpoch) setActionError(errText(e, t)) }
    finally { if (live(account, epoch)) { writing.current = false; setBusy(false) } }
  }

  const plan = async (current: LifecycleTarget, openToOUs: string[], ignoreIntent = false) => {
    const { release, action } = current
    if (writing.current || (intent && !ignoreIntent)) return
    if (action !== 'install' && !release.installation_id) return
    writing.current = true; setBusy(true); setActionError('')
    const account = user
    const epoch = generation.current
    try {
      // Organizational units only: who can use the app is an OU policy (doc 190, D7). None selected keeps it closed.
      const body = action === 'uninstall' || action === 'disable' || action === 'enable'
        ? { action, installation_id: release.installation_id }
        : action === 'update'
        ? { action, release_sha256: release.release_sha256, trust_revision: release.trust_revision, installation_id: release.installation_id }
        : { action, release_sha256: release.release_sha256, trust_revision: release.trust_revision, open_to_ous: openToOUs }
      const result = await api.post<Review>(`${platformRoot}/plans`, body)
      const returnedInstallation = result.review.action === 'install' ? '' : result.review.installation_id
      if (result.review.action !== action || result.review.release_sha256 !== release.release_sha256 || result.review.app_id !== release.app_id || (action !== 'install' && returnedInstallation !== release.installation_id) || !uuid.test(result.plan_id) || !sha.test(result.plan_sha256)) throw new Error(t('appPlatform.invalidOperation'))
      if (live(account, epoch)) { setReview(result); setDefinitive(false) }
    } catch (e) { if (live(account, epoch)) setActionError(errText(e, t)) }
    finally { if (live(account, epoch)) { writing.current = false; setBusy(false) } }
  }

  /** Starts a flow. An install first asks who can use the app; every other action goes straight to its review. */
  // `opener` is the control the person used, captured before any download: it may have lost focus by now (a button
  // is disabled while a package downloads), and it is where focus returns when the flow ends.
  const begin = (release: PlatformRelease, action: LifecycleAction, opener?: HTMLElement | null) => {
    if (writing.current || intent) return
    trigger.current = opener ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null)
    const next = { release, action }
    setTarget(next); setSelected([]); setReview(null); setActionError(''); setHidden(false)
    if (action !== 'install') void plan(next, [])
  }
  /** Install step one done: ask the server for the review of the chosen audience. */
  const next = () => { if (target) void plan(target, selected) }

  const confirm = async () => {
    if (writing.current || (!intent && (!review || expired))) return
    const request: PendingIntent = intent ?? { plan_id: review!.plan_id, plan_sha256: review!.plan_sha256, idempotency_key: crypto.randomUUID(), action: review!.review.action, app_id: review!.review.app_id }
    writing.current = true; setBusy(true); setActionError('')
    const account = user
    const epoch = generation.current
    try {
      // Persist only the already-confirmed request identity before transmitting.
      // A lost response/reload must retry this exact operation, even after expiry.
      sessionStorage.setItem(storageKey, JSON.stringify(request))
      setIntent(request)
      const result = await api.post<{ operation_id: string }>(`${platformRoot}/operations`, { plan_id: request.plan_id, plan_sha256: request.plan_sha256, idempotency_key: request.idempotency_key })
      if (!live(account, epoch)) return
      if (!uuid.test(result.operation_id)) throw new Error(t('appPlatform.invalidOperation'))
      sessionStorage.removeItem(storageKey)
      setIntent(null); setReview(null); setHidden(false)
      setParams(p => { p.set('operation', result.operation_id); return p }, { replace: true })
      changed.current?.()
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
    // The rejected plan is spent. Another action reviews the same target again at once; an install is back at its
    // audience step, and a flow resumed after a reload has no target left to review, so it simply ends.
    if (target && target.action !== 'install') void plan(target, [], true)
  }
  /** Back from the review to the audience choice of an install. */
  const back = () => { if (!busy && !intent) { setReview(null); setActionError('') } }
  /** Leaves the flow. A pending request identity is kept (the modal is only hidden) so it is never lost. */
  const close = () => {
    if (busy) return
    if (intent) { setHidden(true); return }
    setReview(null); setTarget(null); setActionError('')
    if (operation) setParams(p => { p.delete('operation'); return p }, { replace: true })
    // Hand focus back to what opened the flow.
    setTimeout(() => { if (trigger.current?.isConnected) trigger.current.focus(); trigger.current = null }, 0)
  }

  const action: LifecycleAction | undefined = target?.action ?? review?.review.action ?? intent?.action
  const step: LifecycleStep | null = operation ? 'progress' : review || intent ? 'review' : target ? (target.action === 'install' ? 'audience' : 'review') : null
  return {
    target, action, step, open: step !== null && !hidden, hiddenPending: !!intent && hidden, reopen: () => setHidden(false),
    groups, groupCursor, groupsLoading, groupsError, loadMoreGroups, selected, setSelected,
    review, expired, intent, definitive, busy, actionError,
    operation, progress, progressError, refreshProgress: () => setRefresh(v => v + 1),
    begin, next, confirm, clearRejected, back, close,
  }
}
export type PlatformLifecycle = ReturnType<typeof usePlatformLifecycle>
