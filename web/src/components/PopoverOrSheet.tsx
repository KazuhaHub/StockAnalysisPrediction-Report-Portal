import { cloneElement, type ReactElement, type ReactNode } from 'react'
import { Drawer, Grid, Popover } from 'antd'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  content: ReactNode
  /** The control that opens it; it receives an onClick when the panel is a sheet. */
  children: ReactElement<{ onClick?: () => void }>
}

// A panel that opens beside its control where there is room, and as a bottom sheet where there is
// not. A popover wider than the space beside its control is pushed off one edge, cutting off its
// first column. The audit filter panel is 480px wide; measured, it still overflowed at 768px, so
// below the lg breakpoint panels take the whole width from the bottom instead.
export default function PopoverOrSheet({ open, onOpenChange, title, content, children }: Props) {
  const wide = Grid.useBreakpoint().lg
  if (wide) {
    return (
      <Popover open={open} onOpenChange={onOpenChange} trigger="click" placement="bottomLeft" content={content} destroyOnHidden>
        {children}
      </Popover>
    )
  }
  return (
    <>
      {cloneElement(children, { onClick: () => onOpenChange(!open) })}
      <Drawer
        open={open}
        onClose={() => onOpenChange(false)}
        placement="bottom"
        size="auto"
        rootClassName="rp-sheet"
        title={title}
        destroyOnHidden
        styles={{ wrapper: { maxHeight: '85vh' } }}
      >
        {content}
      </Drawer>
    </>
  )
}
