import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import { App, Grid } from 'antd'
import { MemoryRouter } from 'react-router'
import ChatPage from './ChatPage'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), del: vi.fn() }))
vi.mock('../api/client', () => ({
  api: apiMock,
  errText: (e: unknown) => String((e as Error)?.message ?? e),
  ApiError: class ApiError extends Error {},
}))
vi.mock('../auth', () => ({ useAuth: () => ({ name: 'Alice', user: 'alice', admin: false }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('../components/Markdown', () => ({ default: ({ md }: { md: string }) => <div>{md}</div> }))
vi.mock('../lib/batchUi', () => ({ difyModeTag: () => <span>agent-chat</span> }))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })
const calls = (url: string) => apiMock.get.mock.calls.filter(([u]) => u === url).length

describe('ChatPage while the connection is down', () => {
  beforeEach(() => {
    HTMLElement.prototype.scrollTo = vi.fn()
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    apiMock.get.mockReset()
  })

  it('reloads only the conversation list on reconnect, never the assistant list or the chosen assistant', async () => {
    let convs = 0
    apiMock.get.mockImplementation((url: string) => {
      if (url === '/api/chat/targets') return Promise.resolve({ targets: [{ id: 1, name: 'Technical Analysis', mode: 'agent-chat' }] })
      if (url === '/api/chat/config') return Promise.resolve({ stream: true, reconcile_seconds: 300 })
      if (url === '/api/chat/conversations?target_id=1') { convs += 1; return convs === 1 ? Promise.reject(lost()) : Promise.resolve({ conversations: [] }) }
      if (url === '/api/chat/targets/1/intro') return Promise.resolve({ opening: 'How can I help?' })
      return Promise.resolve({})
    })
    render(<App><MemoryRouter><ChatPage /></MemoryRouter></App>)
    await waitFor(() => expect(convs).toBe(1))
    await act(async () => { await Promise.resolve() })
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    await waitFor(() => expect(convs).toBe(2))
    expect(calls('/api/chat/targets')).toBe(1)
    expect(calls('/api/chat/targets/1/intro')).toBe(1)
  })
})
