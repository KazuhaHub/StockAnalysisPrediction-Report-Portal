import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { AppAccessOU, AppLayoutEntry, AppLayoutResp, AppsResp, AppSummary } from '../../api/types'
import { BUILTIN_PIN_PREFIX, builtinAppByKey, type BuiltinApp } from '../../lib/builtinApps'

// The administrator's view of one app (ADR 0048): the layout entry (key, kind, per-OU access) joined
// with what the installed list knows about an installed app (version, entry route, bridge scopes).
export interface AdminApp {
  key: string // `builtin:<slug>` or the installed app's id
  builtin: boolean
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

function merge(entry: AppLayoutEntry, installed: Map<string, AppSummary>): AdminApp {
  const def = entry.builtin ? builtinAppByKey(builtinSlug(entry.key)) : undefined
  const inst = entry.builtin ? undefined : installed.get(entry.key)
  return {
    key: entry.key,
    builtin: entry.builtin,
    builtinDef: def,
    name: entry.name,
    icon: entry.icon ?? inst?.icon,
    version: inst?.version,
    entry: inst?.entry,
    scopes: inst?.scopes ?? [],
    access: entry.access ?? [],
  }
}

/** Loads every app in the administrator's order. The installed list only adds detail, so its failure is not fatal. */
export function useAppsCatalog() {
  const { t } = useTranslation()
  const [apps, setApps] = useState<AdminApp[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const load = useCallback(async () => {
    setError('')
    try {
      const [layout, installed] = await Promise.all([
        api.get<AppLayoutResp>('/api/admin/apps/layout'),
        api.get<AppsResp>('/api/apps').catch(() => null),
      ])
      const byID = new Map((installed?.apps ?? []).map((a) => [a.id, a]))
      setApps((layout?.apps ?? []).map((e) => merge(e, byID)))
    } catch (e) {
      setError(errText(e, t))
    } finally {
      setLoading(false)
    }
  }, [t])
  useEffect(() => {
    void load()
  }, [load])
  return { apps, loading, error, reload: load }
}
