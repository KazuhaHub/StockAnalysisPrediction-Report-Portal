import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import type { LoadOpts } from '../../lib/loadFailure'
import type { AppAccessOU, AppLayoutEntry, AppLayoutResp } from '../../api/types'
import { BUILTIN_PIN_PREFIX, builtinAppByKey, type BuiltinApp } from '../../lib/builtinApps'
import { parseAppDisplay, type AppDisplayView } from '../../lib/appDisplayV2'

/**
 * The publisher name of an installed platform app. The layout carries the verified name with the app, so it is known
 * from the first paint; the catalogue, which the page has already waited for, can add one. The identifier is the
 * fallback only when neither names the publisher.
 */
export function installedPublisher(app: AdminApp, catalogue: { publisher_name?: string; publisher_id: string } | undefined): string | undefined {
  const name = app.publisherName || catalogue?.publisher_name
  if (name) return name
  return app.platform && catalogue ? catalogue.publisher_id : undefined
}

export interface AdminApp {
  key: string // `builtin:<slug>`, the installed app's id, or `platform:<installation id>`
  builtin: boolean
  platform: boolean
  installationId?: string
  appId?: string // a platform installation's application id
  publisherName?: string // the layout's verified publisher name, when it verifies
  display?: AppDisplayView // the layout's verified display metadata, when it verifies
  publisherId?: string // and its publisher: an application id is only unique within one publisher
  state?: string // a platform installation's control state
  builtinDef?: BuiltinApp
  name: string // an installed app's declared name; a built-in app's slug until translated
  icon?: string
  version?: string
  entry?: string
  scopes: string[]
  access: AppAccessOU[]
}

export type ServiceStatusKind = 'all' | 'some' | 'none'
export interface ServiceStatusSummary {
  kind: ServiceStatusKind
  open: number
  total: number
}

/** Counts the OUs an app is effectively open for. With nothing to count, nothing is closed. */
export function serviceStatus(access: AppAccessOU[]): ServiceStatusSummary {
  const total = access.length
  const open = access.filter((a) => a.effective).length
  return { kind: open === total ? 'all' : open === 0 ? 'none' : 'some', open, total }
}

export function builtinSlug(key: string): string {
  return key.startsWith(BUILTIN_PIN_PREFIX) ? key.slice(BUILTIN_PIN_PREFIX.length) : key
}

export const appPath = (key: string) => `/manage/apps/${encodeURIComponent(key)}`

/** Display strings for an app: a built-in one is translated, an installed one speaks for itself. */
export function useAppLabels() {
  const { t } = useTranslation()
  return {
    title: (a: AdminApp) => (a.builtinDef ? t(a.builtinDef.titleKey) : a.name),
    description: (a: AdminApp) => (a.builtinDef ? t(a.builtinDef.descKey) : ''),
  }
}

function merge(entry: AppLayoutEntry): AdminApp {
  const def = entry.builtin ? builtinAppByKey(builtinSlug(entry.key)) : undefined
  return {
    key: entry.key,
    builtin: entry.builtin,
    platform: entry.kind === 'platform',
    installationId: entry.installation_id,
    appId: entry.app_id,
    publisherId: entry.publisher_id,
    publisherName: entry.publisher_name || undefined,
    display: entry.display == null ? undefined : parseAppDisplay(entry.display) ?? undefined,
    state: entry.state,
    builtinDef: def,
    name: entry.name,
    icon: entry.icon,
    version: entry.version,
    entry: entry.entry,
    scopes: entry.scopes ?? [],
    access: entry.access ?? [],
  }
}

/**
 * Loads every app in the administrator's order, with each installed app's detail, from the admin
 * layout alone: the reader listing shows only what the caller's switches leave open, so the console
 * must not depend on it. `reload` resolves
 * false when a refresh failed: before the first load that is the page's error, after it the page it
 * already has stays and the caller decides how to warn.
 */
export function useAppsCatalog() {
  const { t } = useTranslation()
  const [apps, setApps] = useState<AdminApp[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const loaded = useRef(false)
  const { failure, retryKey } = useLoadFailure()
  const load = useCallback(async (opts?: LoadOpts): Promise<boolean> => {
    // A lost connection before the first load keeps the page loading; it loads again when the
    // connection returns.
    let settled = true
    try {
      const layout = await api.get<AppLayoutResp>('/api/admin/apps/layout')
      setApps((layout?.apps ?? []).map(merge))
      setError('')
      loaded.current = true
      return true
    } catch (e) {
      const text = failure(e, opts)
      if (text === null) settled = loaded.current || !!opts?.quiet
      else if (!loaded.current) setError(text)
      return false
    } finally {
      if (settled) setLoading(false)
    }
  }, [t, failure])
  useEffect(() => {
    void load()
  }, [load, retryKey])
  return { apps, loading, error, reload: load }
}
