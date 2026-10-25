import { useEffect, useRef, useState } from 'react'
import { VerticalAlignTopOutlined } from '@ant-design/icons'
import { Tooltip } from 'antd'
import { useTranslation } from 'react-i18next'

export function scrollFraction(top: number, height: number, viewport: number) {
  return height > viewport ? Math.max(0, Math.min(1, top / (height - viewport))) : 0
}

export default function ScrollToTop({ routeKey }: { routeKey: string }) {
  const { t } = useTranslation()
  const target = useRef<HTMLElement | null>(null)
  const [position, setPosition] = useState({ top: 0, fraction: 0 })
  useEffect(() => {
    let frame = 0
    const page = () => (document.scrollingElement || document.documentElement) as HTMLElement
    const measure = () => {
      frame = 0
      const element = target.current?.isConnected ? target.current : page()
      const isPage = element === page()
      setPosition({ top: isPage ? window.scrollY : element.scrollTop,
        fraction: scrollFraction(isPage ? window.scrollY : element.scrollTop, element.scrollHeight, isPage ? window.innerHeight : element.clientHeight) })
    }
    const schedule = () => { if (!frame) frame = requestAnimationFrame(measure) }
    const observer = new ResizeObserver(schedule)
    const observe = (element: HTMLElement) => {
      observer.disconnect(); observer.observe(element)
      if (element.firstElementChild) observer.observe(element.firstElementChild)
    }
    const onScroll = (event: Event) => {
      const next = event.target instanceof HTMLElement ? event.target : page()
      // Navigation rails and overlays own their scrolling and must not redirect this page action.
      if (next.closest('.rp-admin-rail, .rp-policy-scope, .rp-settings-sections, .ant-modal, .ant-drawer')) return
      if (next !== page() && next.scrollHeight <= next.clientHeight) return
      if (target.current !== next) { target.current = next; observe(next) }
      schedule()
    }
    target.current = page(); observe(page()); measure()
    window.addEventListener('scroll', onScroll, { passive: true, capture: true })
    window.addEventListener('resize', schedule)
    return () => { window.removeEventListener('scroll', onScroll, true); window.removeEventListener('resize', schedule); observer.disconnect(); cancelAnimationFrame(frame) }
  }, [routeKey])
  if (position.top <= 300) return null
  const percent = Math.round(position.fraction * 100)
  const progress = t('nav.scrollProgress', { percent })
  return <Tooltip title={`${t('nav.backTop')} · ${progress}`}>
    <button type="button" className="rp-scroll-top" aria-label={t('nav.backTop')} aria-description={progress} onClick={() => {
      const behavior = window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth'
      const element = target.current
      if (!element || element === document.scrollingElement || element === document.documentElement) window.scrollTo({ top: 0, behavior })
      else element.scrollTo({ top: 0, behavior })
    }}>
      <svg viewBox="0 0 48 48" role="progressbar" aria-label={t('nav.scrollPosition')} aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent}>
        <circle cx="24" cy="24" r="21" className="rp-scroll-top-track" />
        <circle cx="24" cy="24" r="21" className="rp-scroll-top-progress" pathLength="100" strokeDasharray={`${position.fraction*100} 100`} transform="rotate(-90 24 24)" />
      </svg>
      <VerticalAlignTopOutlined />
    </button>
  </Tooltip>
}
