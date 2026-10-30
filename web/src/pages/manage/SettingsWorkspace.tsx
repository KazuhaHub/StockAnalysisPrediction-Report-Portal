import { createContext, useContext, useId, type KeyboardEvent, type ReactNode } from 'react'
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
    document.getElementById(tabId(sections[next].key))?.focus()
  }
  return (
    <div className="rp-settings-workspace">
      <div role="tablist" aria-label={t('manage.sections')} className="rp-settings-tabs" onKeyDown={move}>
        {sections.map((section) => {
          const selected = section.key === active
          return (
            <button key={section.key} type="button" role="tab" id={tabId(section.key)} aria-selected={selected}
              aria-controls={panelId} tabIndex={selected ? 0 : -1}
              className={`rp-settings-tab${selected ? ' rp-settings-tab--active' : ''}`}
              onClick={() => onChange(section.key)}>{section.label}</button>
          )
        })}
      </div>
      <div id={panelId} role="tabpanel" aria-labelledby={tabId(active)} className="rp-settings-content">
        <ActiveSection value={active}>{children}</ActiveSection>
      </div>
    </div>
  )
}
