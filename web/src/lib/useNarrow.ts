import { useEffect, useState } from 'react'

/** Phone width. Keep in step with the `max-width: 767px` blocks in index.css. */
export const NARROW_QUERY = '(max-width: 767px)'

function media(): MediaQueryList | null {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? window.matchMedia(NARROW_QUERY) : null
}

/** True while the viewport is phone-width; follows breakpoint crossings. */
export function useNarrow(): boolean {
  const [narrow, setNarrow] = useState(() => media()?.matches ?? false)
  useEffect(() => {
    const mq = media()
    if (!mq) return
    const onChange = () => setNarrow(mq.matches)
    onChange()
    // Older Safari only has the deprecated addListener pair.
    if (mq.addEventListener) mq.addEventListener('change', onChange)
    else mq.addListener?.(onChange)
    return () => {
      if (mq.removeEventListener) mq.removeEventListener('change', onChange)
      else mq.removeListener?.(onChange)
    }
  }, [])
  return narrow
}
