import { useLayoutEffect, useRef, type CSSProperties } from 'react'

/**
 * The loading ring every antd Spin draws (ConfigProvider `spin.indicator`). antd clones it with its
 * own class, which sizes it to the Spin's size, and style; both are kept.
 *
 * Its turn is pinned to the document's animation clock (Web Animations: start time 0), so every
 * ring on screen points the same way. A page that waits twice, for its code and then its data,
 * swaps one ring for another; pinned, the new one carries on where the old one was instead of
 * starting its turn again.
 */
export default function SpinRing({ className, style }: { className?: string; style?: CSSProperties }) {
  const ring = useRef<HTMLSpanElement>(null)
  useLayoutEffect(() => {
    const node = ring.current
    if (!node) return
    const pin = () => {
      for (const animation of node.getAnimations?.() ?? []) animation.startTime = 0
    }
    pin()
    // A ring mounted hidden (a folded panel, an unopened tab) has no animation yet; its CSS
    // animation is created when it is shown, and pinned then.
    node.addEventListener('animationstart', pin)
    return () => node.removeEventListener('animationstart', pin)
  }, [])
  return <span ref={ring} className={className ? `rp-spin-ring ${className}` : 'rp-spin-ring'} style={style} />
}
