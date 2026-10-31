import { useEffect, useState } from 'react'
import { App, Form, type FormInstance } from 'antd'
import { useTranslation } from 'react-i18next'
import { confirmAction } from '../../components/Confirm'
import { usePendingSettings } from './AdminDraftBoundary'

/** Modal forms keep their values until a successful save or an explicit discard. */
export default function useFormDraft(form: FormInstance, open: boolean, saving: boolean, onClose: () => void, extra?: unknown) {
  const { modal } = App.useApp()
  const { t } = useTranslation()
  const watched = Form.useWatch([], { form, preserve: true })
  const [baseline, setBaseline] = useState('')
  const signature = JSON.stringify([watched ?? form.getFieldsValue(true), extra])
  useEffect(() => {
    setBaseline(open ? JSON.stringify([form.getFieldsValue(true), extra]) : '')
  }, [open, form]) // eslint-disable-line react-hooks/exhaustive-deps
  const dirty = open && !!baseline && signature !== baseline
  usePendingSettings(dirty, open && saving)
  const close = () => {
    if (saving) return
    if (!dirty) { onClose(); return }
    confirmAction(modal, { title: t('manage.leaveTitle'), description: t('manage.leaveDescription'),
      okText: t('manage.leave'), cancelText: t('manage.stay'), onOk: onClose }, t)
  }
  return { dirty, close }
}
