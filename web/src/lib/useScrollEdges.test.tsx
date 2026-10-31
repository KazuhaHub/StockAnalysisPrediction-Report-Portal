import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render } from '@testing-library/react'
import { useRef } from 'react'

import { useScrollEdges } from './useScrollEdges'

function Row() {
  const ref = useRef<HTMLDivElement>(null)
  useScrollEdges(ref)
  return <div ref={ref} data-testid="row" className="rp-scroll-row" />
}

describe('useScrollEdges', () => {
  afterEach(() => vi.restoreAllMocks())

  it('marks the sides that have more, and follows the scroll', () => {
    let left = 0
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(100)
    vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockReturnValue(300)
    vi.spyOn(HTMLElement.prototype, 'scrollLeft', 'get').mockImplementation(() => left)
    const { getByTestId } = render(<Row />)
    const row = getByTestId('row')
    expect(row.dataset.end).toBe('true')
    expect(row.dataset.start).toBeUndefined()
    left = 100
    fireEvent.scroll(row)
    expect(row.dataset.start).toBe('true')
    expect(row.dataset.end).toBe('true')
    left = 200
    fireEvent.scroll(row)
    expect(row.dataset.end).toBeUndefined()
  })

  it('marks nothing when everything fits', () => {
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(300)
    vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockReturnValue(300)
    const { getByTestId } = render(<Row />)
    expect(getByTestId('row').dataset.start).toBeUndefined()
    expect(getByTestId('row').dataset.end).toBeUndefined()
  })
})
