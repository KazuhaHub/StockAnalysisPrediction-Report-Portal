import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Button, Space, theme } from 'antd'
import { InfoCircleFilled } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { buildKey, type BuildIdentity } from '../lib/buildIdentity'
import { applyUpdate } from '../lib/swUpdate'
import { deferTarget, deferredTarget, useUpdateState, type UpdateState } from '../lib/updateState'
import { productVersionLabel } from '../lib/productVersionLabel'
import ReleaseNotesModal from './ReleaseNotesModal'

// One coordinator for the whole portal.
//
// The update prompt is a single decision — this page is not the build the server is serving, and
// here is what that means for you — so it is made in one place and read by both layouts. The
// release-note dialog is rendered here and nowhere else, which is what makes "no duplicate overlays"
// structural rather than a rule someone has to remember on the management routes.

type UpdateCtxValue = {
  state: UpdateState
  /** Open the release-note dialog for a build. Null means "the one I am running". */
  openNotes: (target: BuildIdentity | null) => void
  refresh: () => void
  refreshing: boolean
}

const Ctx = createContext<UpdateCtxValue | null>(null)

/** Null outside a provider — the management rail can render as a plain label without one. */
export function useUpdate(): UpdateCtxValue | null {
  return useContext(Ctx)
}

// A click that cannot reload — the browser's own "leave site?" prompt, cancelled — must not leave a
// permanently dead button. Both bounded waits inside applyUpdate are <= 3s, so this only ever fires
// after a handover that will not complete.
const REFRESH_UNLOCK_MS = 6000

export function UpdateProvider({ children }: { children: ReactNode }) {
  const state = useUpdateState()
  const [notesTarget, setNotesTarget] = useState<BuildIdentity | null>(null)
  const [notesOpen, setNotesOpen] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  // Which target has already been escalated to automatically, so relaxing the policy and closing the
  // dialog does not bring it straight back.
  const autoOpened = useRef<string | null>(null)

  const openNotes = useCallback((target: BuildIdentity | null) => {
    setNotesTarget(target ?? state.page)
    setNotesOpen(true)
  }, [state.page])

  // Set the moment the reader asks to refresh, and cleared only if the handover does not complete
  // (see REFRESH_UNLOCK_MS). A ref, not the state below: a state updater must be pure, and React
  // double-invokes one in development, which would call applyUpdate twice for one click — two
  // SKIP_WAITING posts and two reload listeners.
  const refreshingRef = useRef(false)

  const refresh = useCallback(() => {
    if (refreshingRef.current) return
    refreshingRef.current = true
    setRefreshing(true)
    void applyUpdate()
    window.setTimeout(() => {
      refreshingRef.current = false
      setRefreshing(false)
    }, REFRESH_UNLOCK_MS)
  }, [])

  // A `required` policy opens the dialog itself. Keyed by the target so a second deploy re-points the
  // same dialog at the new build instead of leaving the reader looking at the previous one's notes.
  useEffect(() => {
    if (state.policy !== 'required' || !state.kind || !state.target) return
    const key = buildKey(state.target)
    if (autoOpened.current === key) return
    autoOpened.current = key
    setNotesTarget(state.target)
    setNotesOpen(true)
  }, [state.policy, state.kind, state.target])

  const value = useMemo<UpdateCtxValue>(() => ({ state, openNotes, refresh, refreshing }), [state, openNotes, refresh, refreshing])
  const isUpdateTarget = !!notesTarget && buildKey(notesTarget) !== buildKey(state.page)

  return (
    <Ctx.Provider value={value}>
      {children}
      <ReleaseNotesModal
        open={notesOpen}
        target={notesTarget}
        current={state.page}
        policy={state.policy}
        refreshing={refreshing}
        onClose={() => setNotesOpen(false)}
        onRefresh={isUpdateTarget ? refresh : undefined}
      />
    </Ctx.Provider>
  )
}

/**
 * The update banner: a sticky, full-width bar under the header.
 *
 * It is deliberately inline rather than floating, so it can never cover the content it is telling
 * the reader to leave. Under a `required` policy there is no banner at all — the dialog is the
 * prompt, and two presentations of one decision is one too many.
 */
export function UpdateBanner({ maxWidth, compact }: { maxWidth: number | string; compact: boolean }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const ctx = useUpdate()
  const [, setTick] = useState(0)

  // Re-read the deferral on every render of the provider's state; the counter below re-renders this
  // component when "Later" is clicked.
  const state = ctx?.state
  const targetKey = state?.target ? buildKey(state.target) : state?.workerReady ? 'worker' : ''
  const dismissed = state?.policy === 'dismissible' && targetKey !== '' && deferredTarget() === targetKey
  if (!ctx || !state || !state.kind || state.policy === 'required' || dismissed) return null

  const v = state.target ? productVersionLabel(state.target.version) : ''
  const title =
    state.kind === 'newer' ? t('update.newTitle', { version: v })
    : state.kind === 'rollback' ? t('update.rollbackTitle', { version: v })
    : t('update.sameTitle')
  const desc =
    state.kind === 'newer' ? t('update.newDesc')
    : state.kind === 'rollback' ? t('update.rollbackDesc', { version: v })
    : t('update.sameDesc')

  return (
    <div className="rp-update-banner" style={{ position: 'sticky', top: 'var(--rp-header-h, 64px)', zIndex: 19, background: token.colorInfoBg }}>
      <div
        style={{
          maxWidth,
          margin: '0 auto',
          padding: compact ? '8px 12px' : '8px 20px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          flexWrap: 'wrap',
          gap: 10,
        }}
      >
        {/* Icon and sentence are one unit: when the row wraps on a phone, only the actions drop to
            their own line — the icon must never strand itself above the text it annotates. */}
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
          <InfoCircleFilled style={{ color: token.colorInfo, fontSize: 15, flexShrink: 0 }} />
          <span style={{ minWidth: 0, color: token.colorText, fontSize: 14 }}>
            <strong>{title}</strong> {desc}
          </span>
        </span>
        <Space wrap size={8}>
          <Button type="primary" size="small" loading={ctx.refreshing} onClick={ctx.refresh}>
            {t('update.refresh')}
          </Button>
          {state.target && (
            <Button size="small" onClick={() => ctx.openNotes(state.target)}>
              {t('update.viewNotes')}
            </Button>
          )}
          {state.policy === 'dismissible' && (
            <Button
              type="text"
              size="small"
              onClick={() => {
                deferTarget(targetKey)
                setTick((n) => n + 1)
              }}
            >
              {t('update.later')}
            </Button>
          )}
        </Space>
      </div>
    </div>
  )
}
