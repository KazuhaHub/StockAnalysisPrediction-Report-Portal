import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Button, Result, Spin } from 'antd'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from './api/client'
import type { AppsResp } from './api/types'

// Which apps are open for the signed-in reader (ADR 0048). The server decides per OU and enforces it
// for installed apps (the token mint and the bundle are refused). It cannot gate a built-in app's own
// API, which is shared with the reading pages or already gated by a permission, so for those this file
// is the gate: the route guard, the hub and the entry buttons all read this one answer.
//
// The answer is a UI gate and fails OPEN: if the list cannot be read the page stays usable and the
// server still refuses what it can refuse. A bare page with no provider is unrestricted too, which is
// what keeps every page that merely sits near an entry button testable on its own.

interface AppAccessValue {
  ready: boolean
  isOpen: (key: string) => boolean
}

const OPEN: AppAccessValue = { ready: true, isOpen: () => true }

interface Ctx extends AppAccessValue {
  ensureLoaded: () => void
}

const AppAccessContext = createContext<Ctx | null>(null)

export function AppAccessProvider({ user, children }: { user: string; children: ReactNode }) {
  // null = no usable answer yet (or none obtainable): everything counts as open.
  const [open, setOpen] = useState<Set<string> | null>(null)
  const [ready, setReady] = useState(false)
  const requested = useRef(false)
  void user // the provider is keyed by user at its call site; a new account builds a fresh one

  const ensureLoaded = useCallback(() => {
    if (requested.current) return
    requested.current = true
    api
      .get<AppsResp>('/api/apps')
      .then((r) => {
        // An older server sends no layout: leave everything open rather than hide what it never gated.
        setOpen(r.order ? new Set(r.order) : null)
      })
      .catch(() => setOpen(null))
      .finally(() => setReady(true))
  }, [])

  const value = useMemo<Ctx>(
    () => ({ ready, ensureLoaded, isOpen: (key) => open === null || open.has(key) }),
    [ready, open, ensureLoaded],
  )
  return <AppAccessContext.Provider value={value}>{children}</AppAccessContext.Provider>
}

export function useAppAccess(): AppAccessValue {
  const ctx = useContext(AppAccessContext)
  const ensureLoaded = ctx?.ensureLoaded
  useEffect(() => {
    ensureLoaded?.()
  }, [ensureLoaded])
  return ctx ?? OPEN
}

// RequireAppOpen is the route guard for a built-in app: it renders the route when the app is open for
// the reader's OU and a not-available result when it is closed. It waits for the answer rather than
// rendering the route first, so a closed app is never shown for a moment.
export function RequireAppOpen({ appKey, children }: { appKey: string; children: ReactNode }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const access = useAppAccess()
  if (!access.ready) {
    return (
      <div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}>
        <Spin size="large" />
      </div>
    )
  }
  if (!access.isOpen(appKey)) {
    return (
      <Result
        status="403"
        title={t('apps.unavailable')}
        subTitle={t('apps.unavailableHint')}
        extra={
          <Button type="primary" onClick={() => navigate('/apps')}>
            {t('apps.back')}
          </Button>
        }
      />
    )
  }
  return <>{children}</>
}
