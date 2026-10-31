import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'

import Fold from './Fold'

describe('Fold', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('renders nothing while closed', () => {
    render(<Fold open={false}>inside</Fold>)
    expect(screen.queryByText('inside')).toBeNull()
  })

  it('unfolds when opened, and stays until its fold has played when closed', () => {
    const { rerender, container } = render(<Fold open={false}>inside</Fold>)
    rerender(<Fold open>inside</Fold>)
    expect(screen.getByText('inside')).toBeTruthy()
    expect(container.querySelector('.rp-expand.is-closing')).toBeNull()

    rerender(<Fold open={false}>inside</Fold>)
    const fold = container.querySelector('.rp-expand.is-closing') as HTMLElement
    expect(fold).not.toBeNull()
    fireEvent.animationEnd(fold)
    expect(screen.queryByText('inside')).toBeNull()
  })

  it('closes at once when the reader asks for less motion', () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: q.includes('reduce'), addEventListener() {}, removeEventListener() {} }))
    const { rerender } = render(<Fold open>inside</Fold>)
    rerender(<Fold open={false}>inside</Fold>)
    expect(screen.queryByText('inside')).toBeNull()
  })
})
