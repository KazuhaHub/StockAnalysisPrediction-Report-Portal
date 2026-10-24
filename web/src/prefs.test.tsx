import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act, cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { PrefsProvider, usePrefs, type ThemeMode } from './prefs'

// A controllable system preference: `matches` is what the OS says, and `flip` fires the change event
// the provider listens for.
let systemDark = false
let listeners: Array<() => void> = []
const realMatchMedia = window.matchMedia

function flipSystem(dark: boolean) {
  systemDark = dark
  listeners.forEach((l) => l())
}

beforeEach(() => {
  systemDark = false
  listeners = []
  window.localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  document.documentElement.style.colorScheme = ''
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (query: string) => ({
      get matches() {
        return query.includes('dark') ? systemDark : false
      },
      media: query,
      onchange: null,
      addEventListener: (_: string, l: () => void) => listeners.push(l),
      removeEventListener: (_: string, l: () => void) => {
        listeners = listeners.filter((x) => x !== l)
      },
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  })
})

afterEach(() => {
  cleanup()
  Object.defineProperty(window, 'matchMedia', { configurable: true, value: realMatchMedia })
})

function Switch() {
  const { setMode } = usePrefs()
  return (
    <>
      {(['light', 'dark', 'auto'] as ThemeMode[]).map((m) => (
        <button key={m} onClick={() => setMode(m)}>
          {m}
        </button>
      ))}
    </>
  )
}

const root = () => document.documentElement

describe('the effective theme drives native colour scheme', () => {
  it('follows the system while the mode is auto', async () => {
    render(<PrefsProvider><Switch /></PrefsProvider>)
    expect(root().style.colorScheme).toBe('light')
    expect(root().dataset.theme).toBe('light')
    act(() => flipSystem(true))
    expect(root().style.colorScheme).toBe('dark')
    expect(root().dataset.theme).toBe('dark')
  })

  it('keeps native controls light when the reader chose light on a dark system', async () => {
    systemDark = true
    render(<PrefsProvider><Switch /></PrefsProvider>)
    expect(root().style.colorScheme).toBe('dark')
    await userEvent.click(screen.getByText('light'))
    expect(root().style.colorScheme).toBe('light')
    expect(root().dataset.theme).toBe('light')
    act(() => flipSystem(false))
    act(() => flipSystem(true))
    expect(root().style.colorScheme).toBe('light')
  })

  it('applies a stored dark choice on first render', () => {
    window.localStorage.setItem('rp_theme', 'dark')
    render(<PrefsProvider><Switch /></PrefsProvider>)
    expect(root().style.colorScheme).toBe('dark')
    expect(root().dataset.theme).toBe('dark')
  })
})
