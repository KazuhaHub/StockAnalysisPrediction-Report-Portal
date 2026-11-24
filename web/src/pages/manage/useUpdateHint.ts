import { useEffect, useSyncExternalStore } from 'react'
import { api } from '../../api/client'
import { parsePlatformMarketApps, type PlatformMarketApp } from '../../api/appPlatform'
import { useAuth } from '../../auth'
import { offeredUpdate } from '../../lib/semver'
import { parseSources } from './platformMarket'
import { platformRoot } from './platformLifecycle'

// The "update available" hint: one background read of the official catalogue, shared by every card and installation
// page for as long as the page session lives, never blocking what is on screen. Any failure leaves the hint out
// (no alert, no badge). Which version counts as an update is the version step's own rule (`offeredUpdate`).

/** How long a read catalogue is trusted, and how long a failed read is left alone, before the next page asks again. */
const FRESH_MS = 10 * 60_000
const RETRY_MS = 60_000

type Snapshot = { owner: string | null; apps: PlatformMarketApp[] | null }
let snapshot: Snapshot = { owner: null, apps: null }
let reading = false
let readAt = 0
let readOk = false
const listeners = new Set<() => void>()
const publish = (next: Snapshot) => { snapshot = next; listeners.forEach(listener => listener()) }

/** Forgets what was read (tests, and a change of account). */
export function resetCatalogueHint() {
  snapshot = { owner: null, apps: null }; reading = false; readAt = 0; readOk = false
  listeners.forEach(listener => listener())
}

async function read(owner: string | null) {
  try {
    const sources = parseSources(await api.get<unknown>(`${platformRoot}/market/sources`))
    const apps = sources?.enabled ? parsePlatformMarketApps(await api.post<unknown>(`${platformRoot}/market/catalogue`, { catalogue_url: sources.url })) : []
    readOk = true
    publish({ owner, apps })
  } catch {
    readOk = false
    publish({ owner, apps: null })
  } finally {
    reading = false; readAt = Date.now()
  }
}

function ensureRead(owner: string | null) {
  if (reading) return
  const age = Date.now() - readAt
  if (snapshot.owner === owner && readAt > 0 && age < (readOk ? FRESH_MS : RETRY_MS)) return
  reading = true
  if (snapshot.owner !== owner) publish({ owner, apps: null })
  void read(owner)
}

export interface HintApp { publisherId?: string; appId?: string; version?: string }

/**
 * The newest release of the same publisher and app that is an update to `app.version`, or undefined: nothing newer, not
 * in the catalogue, the catalogue not read yet or failed. Asking starts the one shared read, in the background.
 */
export function useUpdateHint(app: HintApp | null | undefined): string | undefined {
  const { user } = useAuth()
  const snap = useSyncExternalStore(listener => { listeners.add(listener); return () => { listeners.delete(listener) } }, () => snapshot)
  const wanted = !!app?.publisherId && !!app.appId && !!app.version
  useEffect(() => { if (wanted) ensureRead(user) }, [wanted, user])
  if (!wanted || snap.owner !== user || !snap.apps) return undefined
  const entry = snap.apps.find(c => c.publisher_id === app!.publisherId && c.app_id === app!.appId)
  return entry ? offeredUpdate(app!.version!, entry.versions.map(v => v.version)) : undefined
}
