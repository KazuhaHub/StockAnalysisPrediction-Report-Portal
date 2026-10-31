import { App } from 'antd'
import type { HookAPI } from 'antd/es/modal/useModal'
import { useTranslation } from 'react-i18next'
import { confirmAction } from '../../components/Confirm'
import { usePendingSettings } from './AdminDraftBoundary'

/**
 * The unsaved-changes guard for editors that keep their own state (no antd Form): registers the
 * pending state with the draft boundary (route changes and reloads) and returns the `close` that a
 * modal or drawer routes Cancel, the X, the mask and Esc through. A clean close just closes; a dirty
 * one asks first, with the same dialog useFormDraft shows. Closing is ignored while a save runs.
 */
export default function useLeaveGuard(dirty: boolean, saving: boolean, onClose: () => void) {
  const { modal } = App.useApp()
  const { t } = useTranslation()
  usePendingSettings(dirty, saving)
  return () => {
    if (saving) return
    if (!dirty) { onClose(); return }
    confirmLeave(modal, t, onClose)
  }
}

/**
 * The one "leave with unsaved changes?" dialog, for every way out of an editor: closing it, picking
 * another item, or stopping an edit mode. `okText` names a way out other than leaving ("Discard").
 */
export function confirmLeave(modal: Pick<HookAPI, 'confirm'>, t: (key: string) => string, onLeave: () => void, okText?: string) {
  confirmAction(modal, { title: t('manage.leaveTitle'), description: t('manage.leaveDescription'),
    okText: okText ?? t('manage.leave'), cancelText: t('manage.stay'), onOk: onLeave }, t)
}
