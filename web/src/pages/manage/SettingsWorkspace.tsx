import { createContext, useContext, useId, type ReactNode } from 'react'
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
  return (
    <div className="rp-settings-workspace">
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
