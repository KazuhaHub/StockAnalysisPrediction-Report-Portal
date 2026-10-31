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

  // A mouse has no sideways swipe: its wheel and a press-and-drag move the row instead, and a drag
  // does not end in a click on the tab it was released over.
  it('scrolls on a vertical mouse wheel and on a mouse drag', () => {
    let left = 0
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(100)
    vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockReturnValue(300)
    vi.spyOn(HTMLElement.prototype, 'scrollLeft', 'get').mockImplementation(() => left)
    vi.spyOn(HTMLElement.prototype, 'scrollLeft', 'set').mockImplementation((v: number) => { left = v })
    const onClick = vi.fn()
    function ClickRow() {
      const ref = useRef<HTMLDivElement>(null)
      useScrollEdges(ref)
      return <div ref={ref} data-testid="row"><button onClick={onClick}>tab</button></div>
    }
    const { getByTestId, getByText } = render(<ClickRow />)
    const row = getByTestId('row')
    // A trackpad's vertical swipe is the page's.
    const swipe = new WheelEvent('wheel', { deltaY: 60, bubbles: true, cancelable: true })
    row.dispatchEvent(swipe)
    expect(left).toBe(0)
    expect(swipe.defaultPrevented).toBe(false)
    const wheel = new WheelEvent('wheel', { deltaY: 3, deltaMode: 1, bubbles: true, cancelable: true })
    row.dispatchEvent(wheel)
    expect(left).toBe(48)
    expect(wheel.defaultPrevented).toBe(true)
    left = 60

    const tab = getByText('tab')
    fireEvent.pointerDown(tab, { pointerType: 'mouse', button: 0, clientX: 100 })
    fireEvent.pointerMove(row, { pointerType: 'mouse', buttons: 1, clientX: 70 })
    fireEvent.pointerUp(row, { pointerType: 'mouse', clientX: 70 })
    fireEvent.click(tab)
    expect(left).toBe(90)
    expect(onClick).not.toHaveBeenCalled()
    // A plain click still selects.
    fireEvent.pointerDown(tab, { pointerType: 'mouse', button: 0, clientX: 100 })
    fireEvent.pointerUp(tab, { pointerType: 'mouse', clientX: 101 })
    fireEvent.click(tab)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('lets the page scroll once the row is at its end', () => {
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(100)
    vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockReturnValue(300)
    vi.spyOn(HTMLElement.prototype, 'scrollLeft', 'get').mockReturnValue(200)
    const { getByTestId } = render(<Row />)
    const wheel = new WheelEvent('wheel', { deltaY: 3, deltaMode: 1, bubbles: true, cancelable: true })
    getByTestId('row').dispatchEvent(wheel)
    expect(wheel.defaultPrevented).toBe(false)
  })

  it('forgets a drag whose release it never saw, and never swallows a later click', async () => {
    let left = 0
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(100)
    vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockReturnValue(300)
    vi.spyOn(HTMLElement.prototype, 'scrollLeft', 'get').mockImplementation(() => left)
    vi.spyOn(HTMLElement.prototype, 'scrollLeft', 'set').mockImplementation((v: number) => { left = v })
    const onClick = vi.fn()
    function ClickRow() {
      const ref = useRef<HTMLDivElement>(null)
      useScrollEdges(ref)
      return <div ref={ref} data-testid="row"><button onClick={onClick}>tab</button></div>
    }
    const { getByTestId, getByText } = render(<ClickRow />)
    const row = getByTestId('row')
    fireEvent.pointerDown(getByText('tab'), { pointerType: 'mouse', button: 0, clientX: 100 })
    fireEvent.pointerMove(row, { pointerType: 'mouse', buttons: 1, clientX: 70 })
    // Released outside, so no click follows; the next activation is the keyboard's.
    fireEvent.pointerUp(window, { pointerType: 'mouse', clientX: 0 })
    await new Promise((resolve) => setTimeout(resolve, 0))
    fireEvent.click(getByText('tab'))
    expect(onClick).toHaveBeenCalledTimes(1)
    // A hover with no button down after a lost release does not move the row.
    fireEvent.pointerDown(getByText('tab'), { pointerType: 'mouse', button: 0, clientX: 100 })
    fireEvent.pointerMove(row, { pointerType: 'mouse', buttons: 0, clientX: 40 })
    expect(left).toBe(30)
    expect(row.dataset.dragging).toBeUndefined()
  })
})
