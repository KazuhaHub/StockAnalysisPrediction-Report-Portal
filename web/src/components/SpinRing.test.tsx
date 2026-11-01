import { afterEach, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import SpinRing from './SpinRing'

afterEach(() => vi.restoreAllMocks())

// antd clones the indicator with its own class (ant-spin-dot, which sizes the ring to the Spin's
// size) and style; the ring must keep both.
it('keeps the class and style antd gives the indicator', () => {
  const { container } = render(<SpinRing className="ant-spin-dot" style={{ color: 'red' }} />)
  const ring = container.firstElementChild as HTMLElement
  expect(ring.className).toBe('rp-spin-ring ant-spin-dot')
  expect(ring.style.color).toBe('red')
})

// Every ring's turn is pinned to the document's animation clock, so one that replaces another
// (the page's data wait after its code wait) points the same way instead of restarting.
it('pins its rotation to the shared animation clock', () => {
  const animation = { startTime: 1234 as number | null }
  const getAnimations = vi.fn(() => [animation])
  Object.defineProperty(HTMLElement.prototype, 'getAnimations', { configurable: true, value: getAnimations })
  try {
    render(<SpinRing />)
    expect(getAnimations).toHaveBeenCalled()
    expect(animation.startTime).toBe(0)
  } finally {
    delete (HTMLElement.prototype as { getAnimations?: unknown }).getAnimations
  }
})

it('pins a ring whose animation only starts once it is shown', () => {
  const animation = { startTime: null as number | null }
  let animations: Array<typeof animation> = []
  Object.defineProperty(HTMLElement.prototype, 'getAnimations', { configurable: true, value: () => animations })
  try {
    const { container } = render(<SpinRing />)
    animations = [animation]
    container.firstElementChild!.dispatchEvent(new Event('animationstart'))
    expect(animation.startTime).toBe(0)
  } finally {
    delete (HTMLElement.prototype as { getAnimations?: unknown }).getAnimations
  }
})
