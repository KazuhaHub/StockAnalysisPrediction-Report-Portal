import { cloneElement, useId, type ReactElement, type ReactNode } from 'react'
import { Typography } from 'antd'

export default function SettingsField({ label, hint, children }: {
  label: string
  hint?: ReactNode
  children: ReactElement
}) {
  const id = useId()
  return <div className="rp-settings-field">
    <div><label htmlFor={id}>{label}</label>{hint && <Typography.Text type="secondary">{hint}</Typography.Text>}</div>
    <div>{cloneElement(children as ReactElement<{ id?: string; 'aria-label'?: string }>, { id, 'aria-label': label })}</div>
  </div>
}
