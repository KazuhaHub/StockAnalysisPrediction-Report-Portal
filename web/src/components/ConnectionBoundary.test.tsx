import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import ConnectionBoundary from './ConnectionBoundary'
import { api, clearConnectionError } from '../api/client'
const refresh = vi.hoisted(() => vi.fn().mockResolvedValue(undefined))
vi.mock('../auth',()=>({useAuth:()=>({refresh})}))
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
afterEach(()=>{vi.unstubAllGlobals();clearConnectionError()})
it('shows a connection page on transport failure and restores the mounted form after retry',async()=>{
  vi.stubGlobal('fetch',vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
  render(<ConnectionBoundary><input aria-label="draft" defaultValue="unsaved" /></ConnectionBoundary>)
  await act(async()=>{await api.post('/api/login',{}).catch(()=>{})})
  expect(await screen.findByText('connection.title')).toBeTruthy()
  vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response('{"siteTitle":"Portal"}')))
  fireEvent.click(screen.getByRole('button',{name:'connection.retry'}))
  await waitFor(()=>expect(screen.queryByText('connection.title')).toBeNull())
  expect((screen.getByRole('textbox',{name:'draft'}) as HTMLInputElement).value).toBe('unsaved')
  expect(refresh).toHaveBeenCalled()
})
