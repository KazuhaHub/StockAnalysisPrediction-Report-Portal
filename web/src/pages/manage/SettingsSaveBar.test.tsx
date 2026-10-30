import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import SettingsSaveBar from './SettingsSaveBar'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

describe('SettingsSaveBar', () => {
  it('renders nothing and reserves no space when clean', () => {
    const { container } = render(<SettingsSaveBar dirty={false} saving={false} onSave={vi.fn()} onDiscard={vi.fn()} />)
    expect(container.innerHTML).toBe('')
  })

  it('shows the summary with Discard and Save when dirty', () => {
    const onSave = vi.fn()
    const onDiscard = vi.fn()
    render(<SettingsSaveBar dirty saving={false} scope="Quotes" onSave={onSave} onDiscard={onDiscard} />)
    expect(screen.getByRole('status').textContent).toContain('Quotes')
    expect(screen.getByRole('status').textContent).toContain('manage.unsavedChanges')
    fireEvent.click(screen.getByRole('button', { name: /manage\.discard/ }))
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    expect(onDiscard).toHaveBeenCalledTimes(1)
    expect(onSave).toHaveBeenCalledTimes(1)
  })

  it('disables Discard while saving', () => {
    render(<SettingsSaveBar dirty saving onSave={vi.fn()} onDiscard={vi.fn()} />)
    expect((screen.getByRole('button', { name: /manage\.discard/ }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('marks the body while shown so floating buttons can clear it', () => {
    const { rerender } = render(<SettingsSaveBar dirty saving={false} onSave={vi.fn()} onDiscard={vi.fn()} />)
    expect(document.body.classList.contains('rp-has-save-dock')).toBe(true)
    rerender(<SettingsSaveBar dirty={false} saving={false} onSave={vi.fn()} onDiscard={vi.fn()} />)
    expect(document.body.classList.contains('rp-has-save-dock')).toBe(false)
  })
})
