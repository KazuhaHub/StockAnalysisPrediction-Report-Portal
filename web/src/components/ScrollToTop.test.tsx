import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import ScrollToTop, { scrollFraction } from './ScrollToTop'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
afterEach(() => vi.restoreAllMocks())
it('clamps progress and handles content without a scroll range', () => {
  expect(scrollFraction(400,1000,200)).toBe(0.5)
  expect(scrollFraction(1200,1000,200)).toBe(1)
  expect(scrollFraction(-20,1000,200)).toBe(0)
  expect(scrollFraction(100,200,200)).toBe(0)
})
it('tracks the active content scroller and returns that area to its top', async () => {
  render(<><div data-testid="content" /><aside className="rp-admin-rail"><div data-testid="rail" /></aside><ScrollToTop routeKey="/manage/overview" /></>)
  const content=screen.getByTestId('content')
  Object.defineProperties(content,{scrollHeight:{value:1000},clientHeight:{value:200},scrollTop:{value:400,configurable:true}})
  content.scrollTo=vi.fn()
  fireEvent.scroll(content)
  await waitFor(()=>expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('50'))
  const rail=screen.getByTestId('rail')
  Object.defineProperties(rail,{scrollHeight:{value:1000},clientHeight:{value:200},scrollTop:{value:800}})
  fireEvent.scroll(rail)
  expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('50')
  fireEvent.click(screen.getByRole('button',{name:'nav.backTop'}))
  expect(content.scrollTo).toHaveBeenCalledWith({top:0,behavior:'smooth'})
  Object.defineProperty(content,'scrollTop',{value:0})
  fireEvent.scroll(content)
  await waitFor(()=>expect(screen.queryByRole('button',{name:'nav.backTop'})).toBeNull())
})
