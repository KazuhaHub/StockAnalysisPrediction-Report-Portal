import { createContext, useContext, useId, type ReactNode } from 'react'
import { Tabs } from 'antd'
import { useTranslation } from 'react-i18next'

const ActiveSection = createContext('')

export function SettingsPanel({ section, children }: { section: string; children: ReactNode }) {
  const active = useContext(ActiveSection)
  // Keep every panel mounted so switching sections preserves drafts and form validation still
  // includes fields outside the currently visible section.
  return <section hidden={section !== active} className="rp-settings-panel">{children}</section>
}

// Sections are a horizontal tab bar directly under the page header; the page itself scrolls. The
// bar scrolls sideways (with antd's overflow menu) when the sections do not fit, so narrow screens
// need no separate picker.
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
      <Tabs className="rp-settings-tabs" aria-label={t('manage.sections')} size="small" activeKey={active}
        onChange={onChange} items={sections.map((section) => ({ key: section.key, label: section.label }))} />
      <div id={id} className="rp-settings-content">
        <ActiveSection value={active}>{children}</ActiveSection>
      </div>
    </div>
  )
}
