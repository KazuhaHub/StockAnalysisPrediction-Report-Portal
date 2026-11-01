import { useEffect, useState } from 'react'

/** A mouse or trackpad: hover and a precise pointer. A touch screen answers no, whatever its width. */
export const FINE_POINTER_QUERY = '(hover: hover) and (pointer: fine)'

function media(): MediaQueryList | null {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? window.matchMedia(FINE_POINTER_QUERY) : null
}

/** True while the primary pointer is fine and can hover; follows changes such as docking a tablet. */
export function useFinePointer(): boolean {
  const [fine, setFine] = useState(() => media()?.matches ?? false)
  useEffect(() => {
    const mq = media()
    if (!mq) return
    const onChange = () => setFine(mq.matches)
    onChange()
    if (mq.addEventListener) mq.addEventListener('change', onChange)
    else mq.addListener?.(onChange)
    return () => {
      if (mq.removeEventListener) mq.removeEventListener('change', onChange)
      else mq.removeListener?.(onChange)
    }
  }, [])
  return fine
}
