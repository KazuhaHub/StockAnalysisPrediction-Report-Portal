import { useEffect, useRef, useState, type ReactNode } from 'react'

const reducedMotion = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches

// Content that unfolds when opened and folds back before it goes, rather than popping in and out:
// the grid row grows from 0fr to 1fr while it fades (.rp-expand in index.css). Closing keeps the
// content mounted until the fold has played; with reduced motion there is no fold to wait for.
export default function Fold({ open, children }: { open: boolean; children: ReactNode }) {
  const [mounted, setMounted] = useState(open)
  const ref = useRef<HTMLDivElement>(null)
  // Adjusting state during render (React's documented pattern): opening mounts at once.
  if (open && !mounted) setMounted(true)
  useEffect(() => {
    const el = ref.current
    if (open || !mounted || !el) return
    const done = (e?: Event) => {
      if (!e || e.target === el) setMounted(false)
    }
    // A native listener: the fold is the element's own animation, and a backstop for a fold that
    // never plays (the element hidden mid-close) so the content cannot linger.
    el.addEventListener('animationend', done)
    const backstop = setTimeout(done, 400)
    return () => {
      el.removeEventListener('animationend', done)
      clearTimeout(backstop)
    }
  }, [open, mounted])
  if (!mounted || (!open && reducedMotion())) return null
  return (
    <div ref={ref} className={`rp-expand${open ? '' : ' is-closing'}`}>
      <div>{children}</div>
    </div>
  )
}
