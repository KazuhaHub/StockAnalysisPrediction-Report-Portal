import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import ScrollToTop from './ScrollToTop'
const t = (key: string) => key
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t }) }))
afterEach(() => vi.restoreAllMocks())

it('uses the native progress ring and animates the active content scroller', async () => {
  render(<><div data-testid="content" /><aside className="rp-admin-rail"><div data-testid="rail" /></aside><ScrollToTop routeKey="/manage/overview" /></>)
  const content = screen.getByTestId('content')
  Object.defineProperties(content, {scrollHeight:{value:1000},clientHeight:{value:200},scrollTop:{value:400,writable:true}})
  fireEvent.scroll(content)
  const button = await screen.findByRole('button', {name:'nav.backTop'})
  await waitFor(() => expect(button.className).toContain('ant-float-btn-progress'))
  expect(button.style.getPropertyValue('--ant-float-btn-progress')).toBe('0.5turn')
  const rail = screen.getByTestId('rail')
  Object.defineProperties(rail,{scrollHeight:{value:1000},clientHeight:{value:200},scrollTop:{value:800}})
  fireEvent.scroll(rail)
  expect(button.style.getPropertyValue('--ant-float-btn-progress')).toBe('0.5turn')
  fireEvent.click(button)
  await waitFor(() => expect(content.scrollTop).toBeLessThan(400))
  await waitFor(() => expect(content.scrollTop).toBe(0))
})

it('updates progress continuously while scrolling the same target', async () => {
  render(<><div data-testid="content" /><ScrollToTop routeKey="/" /></>)
  const content = screen.getByTestId('content')
  Object.defineProperties(content,{scrollHeight:{value:2000},clientHeight:{value:400},scrollTop:{value:400,writable:true}})
  fireEvent.scroll(content)
  const button = await screen.findByRole('button',{name:'nav.backTop'})
  await waitFor(()=>expect(button.style.getPropertyValue('--ant-float-btn-progress')).toBe('0.25turn'))
  await act(async()=>{content.scrollTop=1200;fireEvent.scroll(content)})
  await waitFor(()=>expect(button.style.getPropertyValue('--ant-float-btn-progress')).toBe('0.75turn'))
})

it('listens to document scrolling for the viewport target', async () => {
  const height = vi.spyOn(document.documentElement, 'scrollHeight', 'get').mockReturnValue(2000)
  const viewport = vi.spyOn(document.documentElement, 'clientHeight', 'get').mockReturnValue(400)
  const top = vi.spyOn(document.documentElement, 'scrollTop', 'get').mockReturnValue(800)
  render(<ScrollToTop routeKey="/" />)
  fireEvent.scroll(document)
  const button = await screen.findByRole('button',{name:'nav.backTop'})
  await waitFor(()=>expect(button.style.getPropertyValue('--ant-float-btn-progress')).toBe('0.5turn'))
  top.mockReturnValue(1200)
  fireEvent.scroll(document)
  await waitFor(()=>expect(button.style.getPropertyValue('--ant-float-btn-progress')).toBe('0.75turn'))
  height.mockRestore();viewport.mockRestore();top.mockRestore()
})

it('returns immediately when reduced motion is enabled', async () => {
  render(<><div data-testid="content" /><ScrollToTop routeKey="/" /></>)
  const content=screen.getByTestId('content')
  Object.defineProperties(content,{scrollHeight:{value:2000},clientHeight:{value:400},scrollTop:{value:800,writable:true}})
  fireEvent.scroll(content)
  const button=await screen.findByRole('button',{name:'nav.backTop'})
  vi.spyOn(window,'matchMedia').mockReturnValue({matches:true} as MediaQueryList)
  fireEvent.click(button)
  expect(content.scrollTop).toBe(0)
})
