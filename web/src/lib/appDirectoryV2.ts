import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { validInstallationId } from './appLaunchV2'
import { DESCRIPTOR_PATTERNS } from './descriptor-contract.generated'

export type AppDirectoryState = 'installed' | 'pending' | 'disabled' | 'attention'
export interface AppDirectoryItem {
  installationId: string
  applicationId: string
  state: AppDirectoryState
  openable: boolean
}
interface Page { items: AppDirectoryItem[]; nextCursor: string }
const applicationId = new RegExp(DESCRIPTOR_PATTERNS.slug)
const states: readonly unknown[] = ['installed', 'pending', 'disabled', 'attention']

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
    if (typeof item.installationId !== 'string' || !validInstallationId(item.installationId) || item.installationId <= previous || typeof item.applicationId !== 'string' || !applicationId.test(item.applicationId) || !states.includes(item.state) || typeof item.openable !== 'boolean' || (item.openable && item.state !== 'installed')) return null
    previous = item.installationId
    items.push({ installationId: item.installationId, applicationId: item.applicationId, state: item.state as AppDirectoryState, openable: item.openable })
  }
  if (v.nextCursor !== '' && (items.length === 0 || previous !== v.nextCursor)) return null
  return { items, nextCursor: v.nextCursor }
}

export function useAppDirectory(owner: string | null) {
  const [state, setState] = useState({ owner, items: [] as AppDirectoryItem[], nextCursor: '', loading: true, failed: false })
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
    load()
    return cancel
  }, [load, cancel])
  // Hide a previous account's cards during render, before the new effect runs.
  return { ...(state.owner === owner ? state : { owner, items: [], nextCursor: '', loading: true, failed: false }), load }
}
