import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import ScopeFold from './ScopeFold'

const realMatchMedia = window.matchMedia
let matches = true
let listeners: Array<() => void> = []
beforeEach(() => {
  matches = true
  listeners = []
  Object.defineProperty(window, 'matchMedia', { configurable: true, value: (query: string) => ({
    get matches() { return matches && query.includes('767') }, media: query,
    addEventListener: (_: string, fn: () => void) => { listeners.push(fn) }, removeEventListener: () => {},
  }) })
})
afterEach(() => { Object.defineProperty(window, 'matchMedia', { configurable: true, value: realMatchMedia }) })

const mount = () => render(<ScopeFold label="Research, Sales">{(fold) => <ul aria-label="units"><li><button onClick={fold}>pick</button></li></ul>}</ScopeFold>)

describe('ScopeFold', () => {
  it('renders its content untouched on desktop', () => {
    matches = false
    mount()
    expect(screen.getByRole('list', { name: 'units' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Research/ })).toBeNull()
  })

  it('starts folded on a phone, keeps the unit name in the button and links it to the region', () => {
    mount()
    const toggle = screen.getByRole('button', { name: 'Research, Sales' })
    expect(screen.queryByRole('list')).toBeNull()
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(document.getElementById(toggle.getAttribute('aria-controls') ?? '')).toBeTruthy()
    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByRole('list', { name: 'units' })).toBeTruthy()
  })

  it('folds again when the content reports a choice', () => {
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'Research, Sales' }))
    fireEvent.click(screen.getByRole('button', { name: 'pick' }))
    expect(screen.queryByRole('list')).toBeNull()
  })

  it('shows the content directly once the viewport widens', () => {
    mount()
    matches = false
    act(() => { listeners.forEach((fn) => fn()) })
    expect(screen.getByRole('list', { name: 'units' })).toBeTruthy()
  })
})
