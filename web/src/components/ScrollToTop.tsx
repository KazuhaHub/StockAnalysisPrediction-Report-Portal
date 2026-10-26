import { useCallback, useEffect, useState } from 'react'
import { FloatButton } from 'antd'
import { useTranslation } from 'react-i18next'

export default function ScrollToTop({ routeKey }: { routeKey: string }) {
  const { t } = useTranslation()
  const [scrollTarget, setScrollTarget] = useState<{ element: HTMLElement | null; revision: number }>({ element: null, revision: 0 })
  const getTarget = useCallback(() => {
    const element = scrollTarget.element
    return element?.isConnected && element !== document.scrollingElement && element !== document.documentElement ? element : document
  }, [scrollTarget])

  useEffect(() => {
    let frame = 0
    let active: HTMLElement | null = null
    const page = () => (document.scrollingElement || document.documentElement) as HTMLElement
    const sync = () => {
      frame = 0
      setScrollTarget(previous => ({ element: active, revision: previous.revision + 1 }))
    }
    const schedule = () => { if (!frame) frame = requestAnimationFrame(sync) }
    // Rebind the native component after content resizing so its progress uses the new scroll range.
    const observer = new ResizeObserver(schedule)
    const observe = (element: HTMLElement) => {
      observer.disconnect()
      observer.observe(element)
      if (element.firstElementChild) observer.observe(element.firstElementChild)
    }
    const onScroll = (event: Event) => {
      const next = event.target instanceof HTMLElement ? event.target : page()
      // Navigation and overlays must never redirect the page's BackTop action.
      if (next.closest('.rp-admin-rail, .rp-policy-scope, .rp-settings-sections, .ant-modal, .ant-drawer')) return
      if (next !== page() && next.scrollHeight <= next.clientHeight) return
      if (active !== next) {
        active = next
        observe(next)
        sync()
      }
    }
    const content = document.querySelector<HTMLElement>('.rp-admin-page-body')
    active = content && content.scrollHeight > content.clientHeight ? content : page()
    observe(active)
    sync()
    window.addEventListener('scroll', onScroll, { passive: true, capture: true })
    return () => {
      window.removeEventListener('scroll', onScroll, true)
      observer.disconnect()
      cancelAnimationFrame(frame)
    }
  }, [routeKey])

  return <FloatButton.BackTop showProgress duration={450} visibilityHeight={300}
    target={getTarget} className="rp-scroll-top" aria-label={t('nav.backTop')} tooltip={t('nav.backTop')} />
}
