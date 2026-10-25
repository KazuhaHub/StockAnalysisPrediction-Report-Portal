import { createContext, useContext, useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Button, Select } from 'antd'
import { useTranslation } from 'react-i18next'

const ActiveSection = createContext('')

export function SettingsPanel({ section, children }: { section: string; children: ReactNode }) {
  const active = useContext(ActiveSection)
  // Keep every panel mounted so switching sections preserves drafts and form validation still
  // includes fields outside the currently visible section.
  return <section hidden={section !== active} className="rp-settings-panel">{children}</section>
}

export default function SettingsWorkspace({ sections, active, onChange, children }: {
  sections: { key: string; label: string }[]
  active: string
  onChange: (key: string) => void
  children: ReactNode
}) {
  const { t } = useTranslation()
  const id = useId()
  const workspace = useRef<HTMLDivElement>(null)
  const [height, setHeight] = useState<number>()
  useLayoutEffect(() => {
    const element = workspace.current
    if (!element) return
    const container = element.closest('.rp-policy-content, .rp-admin-page-body')
    const actionBars = [...(container?.querySelectorAll<HTMLElement>('.rp-settings-save-bar') ?? [])]
      .filter((bar) => !element.contains(bar)).map((bar) => bar.parentElement!)
    // Fix the workspace height for the viewport, independently of its active panel. Hiding a long
    // form must not shrink the outer page and clamp its scroll position back to the top.
    const fit = () => {
      const bottom = container?.getBoundingClientRect().bottom || window.innerHeight
      const reserved = Math.max(0, ...actionBars.map((bar) => bar.getBoundingClientRect().height ? bar.getBoundingClientRect().height + 8 : 0))
      setHeight(Math.max(280, bottom - element.getBoundingClientRect().top - reserved))
    }
    fit()
    const observer = new ResizeObserver(fit)
    if (container) observer.observe(container)
    actionBars.forEach((bar) => observer.observe(bar))
    window.addEventListener('resize', fit)
    return () => { observer.disconnect(); window.removeEventListener('resize', fit) }
  }, [])
  return (
    <div ref={workspace} className="rp-settings-workspace" style={{ height }}>
      <div className="rp-settings-section-picker">
        <Select aria-label={t('manage.sections')} value={active} onChange={onChange}
          options={sections.map((section) => ({ value: section.key, label: section.label }))} />
      </div>
      <nav className="rp-settings-sections" aria-label={t('manage.sections')}>
        {sections.map((section) => (
          <Button key={section.key} type={active === section.key ? 'primary' : 'text'}
            aria-current={active === section.key ? 'true' : undefined} aria-controls={id}
            onClick={() => onChange(section.key)}>{section.label}</Button>
        ))}
      </nav>
      <div id={id} className="rp-settings-content">
        <ActiveSection value={active}>{children}</ActiveSection>
      </div>
    </div>
  )
}
