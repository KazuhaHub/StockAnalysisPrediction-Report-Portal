import { createContext, useContext, useId, useLayoutEffect, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

const ActiveSection = createContext('')

export function SettingsPanel({ section, children }: { section: string; children: ReactNode }) {
  const active = useContext(ActiveSection)
  // Keep every panel mounted so switching sections preserves drafts and form validation still
  // includes fields outside the currently visible section.
  return <section hidden={section !== active} className="rp-settings-panel">{children}</section>
}

// Sections are a horizontal tab bar directly under the page header; the page itself scrolls. The
// bar scrolls sideways when the sections do not fit, so narrow screens need no separate picker.
// Every tab controls the one content region, which is labelled by the active tab.
export default function SettingsWorkspace({ sections, active, onChange, children }: {
  sections: { key: string; label: string }[]
  active: string
  onChange: (key: string) => void
  children: ReactNode
}) {
  const { t } = useTranslation()
  const id = useId()
  const tabId = (key: string) => `${id}-tab-${key}`
  const panelId = `${id}-panel`
  // One underline that moves under the selected tab, so a switch slides it across (like antd's ink
  // bar) instead of the old per-tab border jumping. It slides only when the selection changes; first
  // paint, a resize, late web fonts and a language switch put it in place without motion. Written
  // straight to the element: it is decoration, and a re-render per measurement would buy nothing.
  const listRef = useRef<HTMLDivElement>(null)
  const inkRef = useRef<HTMLSpanElement>(null)
  const placedFor = useRef<string | null>(null)
  // Where the bar was last put, so a re-measure that finds the tab unmoved leaves a slide running.
  const placedAt = useRef<string | null>(null)
  // Callers build `sections` inline, so the array is new on every render (some pages re-render per
  // keystroke). What the bar depends on is the tabs' keys and labels.
  const shape = sections.map((section) => `${section.key}\u0000${section.label}`).join('\u0001')
  const placeInk = (animate: boolean) => {
    const ink = inkRef.current
    if (!ink) return
    const tab = placedFor.current === null ? null : document.getElementById(tabId(placedFor.current))
    const at = tab ? `${tab.offsetLeft} ${tab.offsetWidth}` : ''
    // A resize callback mid-slide (the page scrollbar appearing narrows the list, not the tabs) must
    // not take the transition away from a moving bar: that snaps it to the end.
    if (!animate && at === placedAt.current) return
    placedAt.current = at
    ink.classList.toggle('rp-settings-tab-ink--animated', animate)
    // No tab for the selection: underline nothing rather than whichever tab now sits there.
    ink.style.visibility = tab ? '' : 'hidden'
    if (!tab) return
    ink.style.transform = `translateX(${tab.offsetLeft}px)`
    ink.style.width = `${tab.offsetWidth}px`
  }
  // A row wider than its space scrolls without a scrollbar (index.css); a fade marks each side that
  // has more tabs. Read from the list itself, so it holds after a resize, a scroll or a new label.
  const markEdges = () => {
    const list = listRef.current
    if (!list) return
    const more = (on: boolean, side: 'start' | 'end') => {
      if (on) list.dataset[side] = 'true'
      else delete list.dataset[side]
    }
    more(list.scrollLeft > 1, 'start')
    more(list.scrollLeft + list.clientWidth < list.scrollWidth - 1, 'end')
  }
  // The chosen tab is brought fully into view, with a little of its neighbour showing past it.
  const reveal = (smooth: boolean) => {
    const list = listRef.current
    const tab = placedFor.current === null ? null : document.getElementById(tabId(placedFor.current))
    if (!list || !tab) return
    const pad = 24
    const view = list.clientWidth
    let left = list.scrollLeft
    if (tab.offsetLeft < left) left = Math.max(0, tab.offsetLeft - pad)
    else if (tab.offsetLeft + tab.offsetWidth > left + view) left = tab.offsetLeft + tab.offsetWidth - view + pad
    else return
    const still = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
    list.scrollTo?.({ left, behavior: smooth && !still ? 'smooth' : 'auto' })
  }
  useLayoutEffect(() => {
    const animate = placedFor.current !== null && placedFor.current !== active
    placedFor.current = active
    placeInk(animate)
    reveal(animate)
    markEdges()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, shape])
  // Labels change width with the language and once web fonts land, and a bar mounted while hidden
  // measures nothing until shown; follow them. Kept apart from the selection so its first callback
  // never cuts a slide short.
  useLayoutEffect(() => {
    const list = listRef.current
    if (!list || typeof ResizeObserver === 'undefined') return
    // Also the first chance to bring the chosen tab into view when the row mounted hidden.
    const ro = new ResizeObserver(() => {
      placeInk(false)
      reveal(false)
      markEdges()
    })
    ro.observe(list)
    for (const tab of list.querySelectorAll('[role="tab"]')) ro.observe(tab)
    return () => ro.disconnect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shape])
  const move = (event: KeyboardEvent<HTMLDivElement>) => {
    const index = sections.findIndex((section) => section.key === active)
    let next = index
    if (event.key === 'ArrowRight') next = (index + 1) % sections.length
    else if (event.key === 'ArrowLeft') next = (index - 1 + sections.length) % sections.length
    else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = sections.length - 1
    else return
    event.preventDefault()
    onChange(sections[next].key)
    // Focus without the browser's own jump into view: reveal() scrolls it there, smoothly and with a
    // little of the next tab showing.
    document.getElementById(tabId(sections[next].key))?.focus({ preventScroll: true })
  }
  return (
    <div className="rp-settings-workspace">
      <div ref={listRef} role="tablist" aria-label={t('manage.sections')} className="rp-settings-tabs" onKeyDown={move}
        onScroll={markEdges}>
        {sections.map((section) => {
          const selected = section.key === active
          return (
            <button key={section.key} type="button" role="tab" id={tabId(section.key)} aria-selected={selected}
              aria-controls={panelId} tabIndex={selected ? 0 : -1}
              className={`rp-settings-tab${selected ? ' rp-settings-tab--active' : ''}`}
              onClick={() => onChange(section.key)}>{section.label}</button>
          )
        })}
        <span ref={inkRef} aria-hidden="true" className="rp-settings-tab-ink" />
      </div>
      <div id={panelId} role="tabpanel" aria-labelledby={tabId(active)} className="rp-settings-content">
        <ActiveSection value={active}>{children}</ActiveSection>
      </div>
    </div>
  )
}
