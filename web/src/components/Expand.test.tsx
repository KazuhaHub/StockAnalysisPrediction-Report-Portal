import { describe, expect, it } from 'vitest'
import { act, render } from '@testing-library/react'

import Expand from './Expand'

describe('Expand', () => {
  it('mounts open content without playing the unfold', () => {
    const { container } = render(<Expand open>body</Expand>)
    const box = container.querySelector('.rp-fold')!
    expect(box.textContent).toBe('body')
    expect(box.className).toContain('rp-fold--static')
  })

  it('unfolds on open and stays mounted until the fold has played', () => {
    const { container, rerender } = render(<Expand open={false}>body</Expand>)
    expect(container.textContent).toBe('')
    rerender(<Expand open>body</Expand>)
    const box = container.querySelector('.rp-fold')!
    expect(box.className).not.toContain('rp-fold--static')
    rerender(<Expand open={false}>body</Expand>)
    expect(container.querySelector('.rp-fold.is-closing')).not.toBeNull()
    act(() => { container.querySelector('.rp-fold')!.dispatchEvent(new Event('transitionend', { bubbles: true })) })
    expect(container.textContent).toBe('')
  })

  it('reopening mid-fold keeps the content', () => {
    const { container, rerender } = render(<Expand open>body</Expand>)
    rerender(<Expand open={false}>body</Expand>)
    rerender(<Expand open>body</Expand>)
    expect(container.querySelector('.rp-fold.is-closing')).toBeNull()
    expect(container.textContent).toBe('body')
  })
})
