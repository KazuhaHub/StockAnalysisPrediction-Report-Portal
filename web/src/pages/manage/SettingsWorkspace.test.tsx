import { useState } from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import SettingsWorkspace, { SettingsPanel } from './SettingsWorkspace'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

function Harness() {
  const [active, setActive] = useState('a')
  return (
    <SettingsWorkspace active={active} onChange={setActive} sections={[{ key: 'a', label: 'Alpha' }, { key: 'b', label: 'Beta' }]}>
      <SettingsPanel section="a"><input aria-label="draft-a" /></SettingsPanel>
      <SettingsPanel section="b"><input aria-label="draft-b" /></SettingsPanel>
    </SettingsWorkspace>
  )
}

describe('SettingsWorkspace', () => {
  it('renders a horizontal tab bar and no side column', () => {
    const { container } = render(<Harness />)
    expect(screen.getByRole('tablist')).toBeTruthy()
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Alpha', 'Beta'])
    expect(screen.getByRole('tab', { name: 'Alpha' }).getAttribute('aria-selected')).toBe('true')
    expect(container.querySelector('.rp-settings-sections')).toBeNull()
  })

  it('sets no inline fixed height', () => {
    const { container } = render(<Harness />)
    const workspace = container.querySelector('.rp-settings-workspace') as HTMLElement
    expect(workspace.style.height).toBe('')
  })

  it('keeps every panel mounted and preserves state across tab switches', () => {
    render(<Harness />)
    const a = document.querySelector('input[aria-label="draft-a"]') as HTMLInputElement
    fireEvent.change(a, { target: { value: 'typed' } })
    fireEvent.click(screen.getByRole('tab', { name: 'Beta' }))
    expect(screen.getByRole('tab', { name: 'Beta' }).getAttribute('aria-selected')).toBe('true')
    expect(document.querySelector('input[aria-label="draft-a"]')).toBe(a)
    expect(a.closest('section')!.hidden).toBe(true)
    expect(a.value).toBe('typed')
    fireEvent.click(screen.getByRole('tab', { name: 'Alpha' }))
    expect(a.closest('section')!.hidden).toBe(false)
  })

  it('labels the tablist and points every tab at the real content region', () => {
    render(<Harness />)
    expect(screen.getByRole('tablist', { name: 'manage.sections' })).toBeTruthy()
    const panel = screen.getByRole('tabpanel')
    for (const tab of screen.getAllByRole('tab')) expect(tab.getAttribute('aria-controls')).toBe(panel.id)
    expect(panel.getAttribute('aria-labelledby')).toBe(screen.getByRole('tab', { name: 'Alpha' }).id)
    expect(panel.querySelectorAll('section')).toHaveLength(2)
  })
})
