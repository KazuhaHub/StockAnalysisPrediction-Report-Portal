import { useId, useRef, useState, type ReactNode, type Ref } from 'react'
import { Button } from 'antd'
import { DownOutlined, UpOutlined } from '@ant-design/icons'
import { useNarrow } from '../../lib/useNarrow'

/**
 * The phone-width button that folds a unit tree away. Its accessible name is the visible unit
 * label; the open state travels in aria-expanded.
 */
export function ScopeFoldToggle({ open, label, controls, onClick, buttonRef }: { open: boolean; label: string; controls: string; onClick: () => void; buttonRef?: Ref<HTMLButtonElement> }) {
  return (
    <Button ref={buttonRef} className="rp-policy-scope-toggle" icon={open ? <UpOutlined aria-hidden /> : <DownOutlined aria-hidden />} aria-expanded={open} aria-controls={controls} onClick={onClick}>
      <span className="rp-policy-scope-toggle__label">{label}</span>
    </Button>
  )
}

/**
 * On a phone, shows `children` (a unit tree) behind a current-unit button instead of stacking the
 * whole tree above the page. `fold` collapses it again once a unit has been chosen and hands focus
 * back to the button; `folded` tells the tree it is inside the fold. The tree stays mounted while
 * folded, so its own state (multi-select, expansion, search) survives. At desktop width the
 * children render as they are.
 */
export default function ScopeFold({ label, children }: { label: string; children: (fold: () => void, folded: boolean) => ReactNode }) {
  const narrow = useNarrow()
  const [open, setOpen] = useState(false)
  const id = useId()
  const toggle = useRef<HTMLButtonElement>(null)
  const fold = () => {
    setOpen(false)
    toggle.current?.focus()
  }
  // The same element structure at both widths, so crossing the breakpoint does not remount the tree.
  return (
    <div className={narrow ? 'rp-scope-fold' : 'rp-scope-fold rp-scope-fold--inline'}>
      {narrow && <ScopeFoldToggle buttonRef={toggle} open={open} label={label} controls={id} onClick={() => setOpen((value) => !value)} />}
      <div id={id} className="rp-scope-fold__body" hidden={narrow && !open}>{children(narrow ? fold : () => {}, narrow)}</div>
    </div>
  )
}
