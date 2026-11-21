import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import LongId from './LongId'

afterEach(cleanup)

describe('LongId', () => {
  const value = '5540b2ae2aece83ece682a14f4dd0d864b43ff3467d33af938140d44e413f0b1'

  it('keeps the whole value readable, copyable and the tail always visible', () => {
    render(<LongId value={value} />)
    const id = screen.getByTitle(value)
    expect(id.textContent).toContain(value)
    // The tail never shrinks, so the end of the identifier survives the ellipsis.
    expect(id.querySelector('.rp-id__tail')?.textContent).toBe(value.slice(-8))
    expect(id.querySelector('.rp-id__head')?.textContent).toBe(value.slice(0, -8))
    expect(screen.getByRole('button')).toBeTruthy()
  })

  it('does not split a short value', () => {
    render(<LongId value="abc" />)
    expect(screen.getByTitle('abc').querySelector('.rp-id__head')?.textContent).toBe('')
  })
})
