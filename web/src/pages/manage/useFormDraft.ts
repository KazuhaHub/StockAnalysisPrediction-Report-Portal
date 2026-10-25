import { useEffect, useState } from 'react'
import { App, Form, type FormInstance } from 'antd'
import { useTranslation } from 'react-i18next'
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
    modal.confirm({ title: t('manage.leaveTitle'), content: t('manage.leaveDescription'),
      okText: t('manage.leave'), cancelText: t('manage.stay'), okButtonProps: { danger: true }, onOk: onClose })
  }
  return { dirty, close }
}
