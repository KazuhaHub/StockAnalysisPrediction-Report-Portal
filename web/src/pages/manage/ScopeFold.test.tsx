import { useState } from 'react'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { mockNarrowViewport } from '../../test/narrow'
import ScopeFold from './ScopeFold'

const viewport = mockNarrowViewport()

function Counter() {
  const [n, setN] = useState(0)
  return <button onClick={() => setN(n + 1)}>count {n}</button>
}
const mount = () => render(<ScopeFold label="Research, Sales">{(fold, folded) => <ul aria-label="units" data-folded={String(folded)}><li><button onClick={fold}>pick</button></li><li><Counter /></li></ul>}</ScopeFold>)

describe('ScopeFold', () => {
  it('renders its content untouched on desktop', () => {
    viewport.set(false)
    mount()
    expect(screen.getByRole('list', { name: 'units' }).getAttribute('data-folded')).toBe('false')
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
    expect(screen.getByRole('list', { name: 'units' }).getAttribute('data-folded')).toBe('true')
  })

  it('folds again on a choice and returns focus to the toggle', () => {
    mount()
    const toggle = screen.getByRole('button', { name: 'Research, Sales' })
    fireEvent.click(toggle)
    fireEvent.click(screen.getByRole('button', { name: 'pick' }))
    expect(screen.queryByRole('list')).toBeNull()
    expect(document.activeElement).toBe(toggle)
  })

  it('keeps the tree mounted while folded so its own state survives', () => {
    mount()
    const toggle = screen.getByRole('button', { name: 'Research, Sales' })
    fireEvent.click(toggle)
    fireEvent.click(screen.getByRole('button', { name: 'count 0' }))
    fireEvent.click(screen.getByRole('button', { name: 'pick' }))
    expect(screen.queryByRole('button', { name: /count/ })).toBeNull()
    fireEvent.click(toggle)
    expect(screen.getByRole('button', { name: 'count 1' })).toBeTruthy()
  })

  it('shows the content directly once the viewport widens', () => {
    mount()
    act(() => viewport.set(false))
    expect(screen.getByRole('list', { name: 'units' })).toBeTruthy()
  })
})
