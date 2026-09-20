import { useEffect, useMemo, useState } from 'react'
import { api } from '../api/client'
import { buildKey, classifyChange, pageBuildIdentity, type BuildIdentity, type UpdateKind } from './buildIdentity'
import { useSWUpdateReady } from './swUpdate'

// Update detection as structured state, so every surface that asks "is this page stale?" asks the
// same one and gets the same answer. It used to be a boolean, which could not name the target
// version, could not tell a new release from a rollback, and stopped looking the moment it had
// answered once — so a second deploy under the same tab, or an administrator relaxing the prompt
// policy, went unnoticed.
//
// It reads the (session-gated) /api/version endpoint, which carries both the server's build identity
// and the effective update-prompt policy. Failure is not an update: an offline blip or a transient
// 401 answers nothing, and nothing is what the state records.

export type UpdatePolicy = 'dismissible' | 'persistent' | 'required' | 'automatic'
export const DEFAULT_UPDATE_POLICY: UpdatePolicy = 'dismissible'

// normalizePolicy maps whatever the server sent onto a policy. An unreadable value degrades to the
// default rather than to `required`, so a corrupt settings row cannot start blocking every reader.
export function normalizePolicy(raw: unknown): UpdatePolicy {
  return raw === 'persistent' || raw === 'required' || raw === 'automatic' || raw === 'dismissible'
    ? raw
    : DEFAULT_UPDATE_POLICY
}

type VersionResp = {
  version: string
  commit: string
  buildDate: string
  updatePromptPolicy?: string
  automaticUpdate?: boolean
}

export type UpdateState = {
  /** The build the bundle in this tab was compiled from. Never the server's answer. */
  page: BuildIdentity
  /** The build the server is serving, when it differs from the page's. */
  target: BuildIdentity | null
  /** How the target relates to the page, or null when there is nothing to prompt about. */
  kind: UpdateKind | null
  policy: UpdatePolicy
  /** A waiting service worker has installed beside us. */
  workerReady: boolean
  /** The last check did not complete — offline, or the session ended. */
  failed: boolean
}

// The deferral key lives in sessionStorage: "Later" hides one target for this tab session, and a
// different target — or a reload — prompts again. localStorage would let a reader silence updates
// for every future session, and a variable would forget the choice on the next navigation.
const DEFERRED_KEY = 'rp.update.deferred'
const AUTOMATIC_ATTEMPTED_KEY = 'rp.update.automatic.attempted'
const AUTOMATIC_PENDING_KEY = 'rp.update.automatic.pending'

type AutomaticPending = { key: string; version: string; from: string }

/** Record the target before reloading. Returning false means storage is unavailable, so an
 * automatic reload would have no loop guard and must not start. */
export function automaticAttempt(page: BuildIdentity, target: BuildIdentity | null): boolean {
  const pending: AutomaticPending = {
    key: target ? buildKey(target) : 'worker',
    version: target?.version ?? '',
    from: buildKey(page),
  }
  try {
    sessionStorage.setItem(AUTOMATIC_ATTEMPTED_KEY, pending.key)
    sessionStorage.setItem(AUTOMATIC_PENDING_KEY, JSON.stringify(pending))
    return true
  } catch {
    return false
  }
}

export function automaticAttemptedTarget(): string | null {
  try {
    return sessionStorage.getItem(AUTOMATIC_ATTEMPTED_KEY)
  } catch {
    return null
  }
}

/** Consume a pending handover only after its target build is running. An empty version is a
 * worker-only update whose target identity was unavailable. */
export function completedAutomaticUpdate(page: BuildIdentity): string | null {
  try {
    const raw = sessionStorage.getItem(AUTOMATIC_PENDING_KEY)
    if (!raw) return null
    const pending = JSON.parse(raw) as Partial<AutomaticPending>
    if (typeof pending.key !== 'string' || typeof pending.version !== 'string' || typeof pending.from !== 'string') return null
    const loaded = buildKey(page)
    if (loaded === pending.from) return null
    sessionStorage.removeItem(AUTOMATIC_PENDING_KEY)
    if (sessionStorage.getItem(AUTOMATIC_ATTEMPTED_KEY) === pending.key) {
      sessionStorage.removeItem(AUTOMATIC_ATTEMPTED_KEY)
    }
    return loaded === pending.key ? pending.version : page.version
  } catch {
    return null
  }
}

export function deferredTarget(): string | null {
  try {
    return sessionStorage.getItem(DEFERRED_KEY)
  } catch {
    return null // private mode / storage disabled — the prompt simply reappears
  }
}

export function deferTarget(key: string): void {
  try {
    sessionStorage.setItem(DEFERRED_KEY, key)
  } catch {
    /* storage disabled: the choice is not remembered, which is the safe direction */
  }
}

export function useUpdateState(pollMs = 5 * 60_000): UpdateState {
  const page = useMemo(() => pageBuildIdentity(), [])
  const [target, setTarget] = useState<BuildIdentity | null>(null)
  const [policy, setPolicy] = useState<UpdatePolicy>(DEFAULT_UPDATE_POLICY)
  const [failed, setFailed] = useState(false)
  const workerReady = useSWUpdateReady()

  useEffect(() => {
    let stopped = false
    let timer: number | undefined
    let inFlight = false
    // Out-of-order guard: a slow answer must not overwrite a newer one, or a rollback arriving
    // before a stale "you are current" would flicker the prompt away.
    let issued = 0
    let answered = 0

    const schedule = () => {
      if (stopped) return
      timer = window.setTimeout(check, pollMs)
    }

    const check = async () => {
      if (stopped) return
      window.clearTimeout(timer)
      // Coalesce: a visibility-triggered check must not stack a second request on one already out.
      if (inFlight) return
      inFlight = true
      const mine = ++issued
      const info = await api.get<VersionResp>('/api/version').catch(() => null)
      inFlight = false
      if (stopped) return
      if (mine < answered) {
        schedule()
        return
      }
      answered = mine
      if (info) {
        setFailed(false)
        setPolicy(info.automaticUpdate === true ? 'automatic' : normalizePolicy(info.updatePromptPolicy))
        const server = { version: info.version, commit: info.commit, buildDate: info.buildDate }
        // Re-evaluated every tick, in both directions: a second deploy moves the target, and a
        // rollback to the page's own build clears it.
        setTarget(buildKey(server) === buildKey(page) ? null : server)
      } else {
        setFailed(true)
      }
      schedule()
    }

    const onVisible = () => {
      if (document.visibilityState === 'visible') void check()
    }

    void check()
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      stopped = true
      window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [page, pollMs])

  const kind = useMemo<UpdateKind | null>(() => {
    if (target) return classifyChange(page, target) ?? 'same'
    // A worker that installed beside us is a real signal with no version attached to it: never
    // infer one from it, and never describe it as a new release.
    return workerReady ? 'same' : null
  }, [page, target, workerReady])

  return { page, target, kind, policy, workerReady, failed }
}
