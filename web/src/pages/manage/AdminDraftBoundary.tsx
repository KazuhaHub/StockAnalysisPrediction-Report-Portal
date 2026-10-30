import { markDraft } from '../../lib/pendingDrafts'
import { createContext, useCallback, useContext, useEffect, useId, useMemo, useState, type ReactNode } from 'react'
import { Modal } from 'antd'
import { useBlocker } from 'react-router'
import { useTranslation } from 'react-i18next'

type DraftState = { dirty: boolean; saving: boolean }
const DraftRegistry = createContext<((id: string, state: DraftState | null) => void) | null>(null)

/** Register only pending state; credentials and draft values never leave the editor. */
export function usePendingSettings(dirty: boolean, saving = false) {
  const register = useContext(DraftRegistry)
  const id = useId()
  useEffect(() => {
    register?.(id, { dirty, saving })
    return () => register?.(id, null)
  }, [register, id, dirty, saving])
  useEffect(() => {
    markDraft(id, dirty)
    return () => markDraft(id, false)
  }, [id, dirty])
  useEffect(() => {
    if (!dirty && !saving) return
    const preventUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', preventUnload)
    return () => window.removeEventListener('beforeunload', preventUnload)
  }, [dirty, saving])
}

export default function AdminDraftBoundary({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  const [drafts, setDrafts] = useState<Record<string, DraftState>>({})
  const register = useCallback((id: string, state: DraftState | null) => {
    setDrafts((current) => {
      if (!state) {
        if (!(id in current)) return current
        const next = { ...current }
        delete next[id]
        return next
      }
      if (current[id]?.dirty === state.dirty && current[id]?.saving === state.saving) return current
      return { ...current, [id]: state }
    })
  }, [])
  const states = useMemo(() => Object.values(drafts), [drafts])
  const pending = states.some((draft) => draft.dirty || draft.saving)
  const saving = states.some((draft) => draft.saving)
  const blocker = useBlocker(({ currentLocation, nextLocation }) => pending &&
    (currentLocation.pathname !== nextLocation.pathname || currentLocation.search !== nextLocation.search))
  return (
    <DraftRegistry value={register}>
      {children}
      <Modal open={blocker.state === 'blocked'} title={t('manage.leaveTitle')}
        okText={t('manage.leave')} cancelText={t('manage.stay')}
        okButtonProps={{ danger: true, disabled: saving }}
        onCancel={() => { if (blocker.state === 'blocked') blocker.reset() }}
        onOk={() => { if (!saving && blocker.state === 'blocked') blocker.proceed() }}>
        {t(saving ? 'manage.saveInProgress' : 'manage.leaveDescription')}
      </Modal>
    </DraftRegistry>
  )
}
