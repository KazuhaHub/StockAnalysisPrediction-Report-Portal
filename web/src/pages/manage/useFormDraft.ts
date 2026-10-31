import { useEffect, useState } from 'react'
import { Form, type FormInstance } from 'antd'
import useLeaveGuard from './useLeaveGuard'

/** Modal forms keep their values until a successful save or an explicit discard. */
export default function useFormDraft(form: FormInstance, open: boolean, saving: boolean, onClose: () => void, extra?: unknown) {
  const watched = Form.useWatch([], { form, preserve: true })
  const [baseline, setBaseline] = useState('')
  const signature = JSON.stringify([watched ?? form.getFieldsValue(true), extra])
  useEffect(() => {
    setBaseline(open ? JSON.stringify([form.getFieldsValue(true), extra]) : '')
  }, [open, form]) // eslint-disable-line react-hooks/exhaustive-deps
  const dirty = open && !!baseline && signature !== baseline
  const close = useLeaveGuard(dirty, open && saving, onClose)
  return { dirty, close }
}
