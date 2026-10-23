import type { ReactNode } from 'react'

// A table wider than its container scrolls sideways, which on a phone is every table in the drawer.
// A scroll area with nothing focusable inside it cannot be scrolled from the keyboard (a member
// without a report is plain text, so a row has no link), so the area is itself a named region that
// takes focus. The table is laid out at its natural width and this region does the scrolling; giving
// the table a scroll width as well would put a second scroll area inside it.
export default function ScrollRegion({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div role="region" aria-label={label} tabIndex={0} className="rp-fc-scroll">
      {children}
    </div>
  )
}
