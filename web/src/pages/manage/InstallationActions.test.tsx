import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import InstallationActions, { availableActions } from './InstallationActions'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
afterEach(cleanup)

describe('availableActions', () => {
  it('offers Update only when a newer release is known, and always Switch version', () => {
    expect(availableActions({ state: 'installed' })).toEqual(['switch', 'disable', 'uninstall'])
    expect(availableActions({ state: 'installed', updateTo: '1.1.0' })).toEqual(['update', 'switch', 'disable', 'uninstall'])
  })
  it('offers Repair alone, not Enable too, for a disabled app the server can repair: they open the same flow', () => {
    expect(availableActions({ state: 'disabled', repairEligible: true })).toEqual(['switch', 'repair', 'uninstall'])
    expect(availableActions({ state: 'disabled' })).toEqual(['switch', 'enable', 'uninstall'])
  })
  it('keeps Disable beside Repair for an enabled app', () => {
    expect(availableActions({ state: 'installed', repairEligible: true })).toEqual(['switch', 'disable', 'repair', 'uninstall'])
  })
})

describe('a blocked control', () => {
  const props = { variant: 'toolbar' as const, state: 'installed', busy: false, onPick: vi.fn(), onLifecycle: vi.fn() }
  it('stays reachable, says it is unavailable and why, and a press answers with the reason instead of acting', async () => {
    const onRefuse = vi.fn()
    render(<InstallationActions {...props} blocked="pending" onRefuse={onRefuse} />)
    const button = screen.getByRole('button', { name: 'appStore.switchVersion' })
    expect(button.hasAttribute('disabled')).toBe(false)
    expect(button.getAttribute('aria-disabled')).toBe('true')
    expect(button.getAttribute('title')).toBe('appStore.busyPending')
    expect(document.getElementById(button.getAttribute('aria-describedby')!)?.textContent).toBe('appStore.busyPending')
    await userEvent.click(button)
    expect(onRefuse).toHaveBeenCalledTimes(1)
    expect(props.onPick).not.toHaveBeenCalled()
  })
  it('gives the working reason while the console is only busy', () => {
    render(<InstallationActions {...props} blocked="working" />)
    expect(screen.getByRole('button', { name: 'appStore.disable' }).getAttribute('title')).toBe('appStore.busyWorking')
  })
})
