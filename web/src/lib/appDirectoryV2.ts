import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { validInstallationId } from './appLaunchV2'
import { parseAppDisplay, type AppDisplayView } from './appDisplayV2'
import { DESCRIPTOR_PATTERNS } from './descriptor-contract.generated'

export type AppDirectoryState = 'installed' | 'pending' | 'setup_required' | 'disabled' | 'attention'
export interface AppDirectoryItem {
  installationId: string
  applicationId: string
  state: AppDirectoryState
  openable: boolean
  // The viewer administers this app (a Portal administrator, or one the app was delegated to). It can be
  // true while `openable` is false: an administrator need not be in the audience.
  manageable: boolean
  display?: AppDisplayView
  publisherName?: string
}
interface Page { items: AppDirectoryItem[]; nextCursor: string }
const applicationId = new RegExp(DESCRIPTOR_PATTERNS.slug)
const states: readonly unknown[] = ['installed', 'pending', 'setup_required', 'disabled', 'attention']

// Control-state hints for display; opening still requires fresh server checks.
export function parseAppDirectory(value: unknown): Page | null {
  if (!value || typeof value !== 'object') return null
  const v = value as Record<string, unknown>
  if (!Array.isArray(v.items) || v.items.length > 32 || typeof v.nextCursor !== 'string' || (v.nextCursor !== '' && !validInstallationId(v.nextCursor))) return null
  const items: AppDirectoryItem[] = []
  let previous = ''
  for (const raw of v.items) {
    if (!raw || typeof raw !== 'object') return null
    const item = raw as Record<string, unknown>
    if (typeof item.installationId !== 'string' || !validInstallationId(item.installationId) || item.installationId <= previous || typeof item.applicationId !== 'string' || !applicationId.test(item.applicationId) || !states.includes(item.state) || typeof item.openable !== 'boolean' || (item.openable && item.state !== 'installed') || (item.manageable !== undefined && typeof item.manageable !== 'boolean')) return null
    const display = item.display == null ? null : parseAppDisplay(item.display)
    if (item.display != null && !display) return null
    previous = item.installationId
    items.push({ installationId: item.installationId, applicationId: item.applicationId, state: item.state as AppDirectoryState, openable: item.openable, manageable: item.manageable === true, ...(display ? { display } : {}), ...(typeof item.publisherName === 'string' && item.publisherName !== '' ? { publisherName: item.publisherName } : {}) })
  }
  if (v.nextCursor !== '' && (items.length === 0 || previous !== v.nextCursor)) return null
  return { items, nextCursor: v.nextCursor }
}

/** `enabled` false keeps the directory unread (a page that only sometimes needs it). */
export function useAppDirectory(owner: string | null, enabled = true) {
  const [state, setState] = useState({ owner, items: [] as AppDirectoryItem[], nextCursor: '', loading: enabled, failed: false })
  const request = useRef<AbortController | null>(null)
  const epoch = useRef(0)
  const load = useCallback((after = '') => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    const current = ++epoch.current
    setState(previous => ({ owner, items: after && previous.owner === owner ? previous.items : [], nextCursor: after, loading: true, failed: false }))
    api.get<unknown>(`/api/app-platform/apps${after ? `?after=${encodeURIComponent(after)}` : ''}`, { signal: controller.signal }).then(value => {
      if (epoch.current !== current || controller.signal.aborted) return
      const page = parseAppDirectory(value)
      if (!page || (after && page.items.some(item => item.installationId <= after)) || (after && page.nextCursor === after)) throw new Error('invalid application directory')
      setState(previous => ({ owner, items: after ? [...previous.items, ...page.items] : page.items, nextCursor: page.nextCursor, loading: false, failed: false }))
    }).catch(() => {
      if (epoch.current === current && !controller.signal.aborted) setState(previous => ({ ...previous, loading: false, failed: true }))
    })
    return controller
  }, [owner])
  const cancel = useCallback(() => {
    request.current?.abort()
    epoch.current++
  }, [])
  useEffect(() => {
    if (!enabled) return
    load()
    return cancel
  }, [load, cancel, enabled])
  // Hide a previous account's cards during render, before the new effect runs.
  return { ...(state.owner === owner ? state : { owner, items: [], nextCursor: '', loading: true, failed: false }), load }
}

/** Where an app's administration page lives for this viewer: a console tab for a Portal administrator, a page of the app shell otherwise. */
export function manageHref(installation: string, portalAdmin: boolean): string {
  return portalAdmin ? `/manage/apps/installations/${installation}/admin` : `/apps/manage/${installation}`
}
