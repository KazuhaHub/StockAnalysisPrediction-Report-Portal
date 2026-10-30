import { useId, useState, type ReactNode } from 'react'
import { Button } from 'antd'
import { DownOutlined, UpOutlined } from '@ant-design/icons'
import { useNarrow } from '../../lib/useNarrow'

/**
 * The phone-width button that folds a unit tree away. Its accessible name is the visible unit
 * label; the open state travels in aria-expanded.
 */
export function ScopeFoldToggle({ open, label, controls, onClick }: { open: boolean; label: string; controls: string; onClick: () => void }) {
  return (
    <Button className="rp-policy-scope-toggle" icon={open ? <UpOutlined aria-hidden /> : <DownOutlined aria-hidden />} aria-expanded={open} aria-controls={controls} onClick={onClick}>
      <span className="rp-policy-scope-toggle__label">{label}</span>
    </Button>
  )
}

/**
 * On a phone, shows `children` (a unit tree) behind a current-unit button instead of stacking the
 * whole tree above the page. `fold` collapses it again once a unit has been chosen. At desktop width
 * the children render as they are.
 */
export default function ScopeFold({ label, children }: { label: string; children: (fold: () => void) => ReactNode }) {
  const narrow = useNarrow()
  const [open, setOpen] = useState(false)
  const id = useId()
  if (!narrow) return <>{children(() => {})}</>
  return (
    <div className="rp-scope-fold">
      <ScopeFoldToggle open={open} label={label} controls={id} onClick={() => setOpen((value) => !value)} />
      <div id={id} hidden={!open}>{open && children(() => setOpen(false))}</div>
    </div>
  )
}
