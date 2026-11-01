import { useEffect, useRef, useState, type ReactNode } from 'react'

const reducedMotion = () =>
  typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

/**
 * Content that unfolds below its toggle and folds back before it is unmounted (.rp-fold in
 * index.css). What is open when the view first renders is simply there: only a change the reader
 * made plays the motion, and pressing the toggle again mid-fold turns it back from where it is.
 * Closed content is unmounted, not hidden, so a chart inside it does not keep measuring a box it
 * cannot see.
 */
export default function Expand({ open, children }: { open: boolean; children: ReactNode }) {
  const [shown, setShown] = useState(open)
  const [moved, setMoved] = useState(false)
  // Adjusted during render rather than in an effect, so the first open frame already holds the
  // content and the last closed one never flashes it.
  if (open && !shown) {
    setShown(true)
    setMoved(true)
  }
  if (!open && shown && reducedMotion()) setShown(false)
  else if (!open && shown && !moved) setMoved(true)
  const closing = shown && !open
  // Unmounted once the fold has played: the box's own transition ending, or a short timeout where
  // the browser never reports one.
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const node = box.current
    if (!node || !closing) return
    const done = (event: Event) => {
      if (event.target === node) setShown(false)
    }
    node.addEventListener('transitionend', done)
    const fallback = setTimeout(() => setShown(false), 400)
    return () => {
      node.removeEventListener('transitionend', done)
      clearTimeout(fallback)
    }
  }, [closing])
  if (!shown) return null
  return (
    <div ref={box} className={`rp-fold${moved ? '' : ' rp-fold--static'}${closing ? ' is-closing' : ''}`}>
      <div>{children}</div>
    </div>
  )
}
