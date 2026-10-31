import { act, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import LoadGate from './LoadGate'
import { clearConnectionError } from '../api/client'
import { setConnectionError } from '../api/connection'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

afterEach(() => act(() => clearConnectionError()))

it('keeps its spinner and does not render the children while loading', () => {
  render(<LoadGate loading><span>defaults</span></LoadGate>)
  expect(screen.getByText('common.loading')).toBeTruthy()
  expect(screen.queryByText('defaults')).toBeNull()
})

it('says it is waiting for the connection, still without children, while the connection is down', () => {
  render(<LoadGate loading><span>defaults</span></LoadGate>)
  expect(screen.queryByText('common.waitingForConnection')).toBeNull()
  act(() => setConnectionError('network_unavailable'))
  expect(screen.getByText('common.waitingForConnection')).toBeTruthy()
  expect(screen.queryByText('defaults')).toBeNull()
})

// Nothing flashes for an answer that arrives at once; a slow one gets a skeleton in the shape of
// the page, not a spinner [Primer loading; Geist skeleton]. The words stay for assistive tech.
it('shows a skeleton only after a short delay, and keeps an accessible loading status', () => {
  vi.useFakeTimers()
  try {
    const { container } = render(<LoadGate loading><span>defaults</span></LoadGate>)
    expect(screen.getByRole('status').textContent).toBe('common.loading')
    expect(container.querySelector('.ant-skeleton')).toBeNull()
    expect(container.querySelector('.ant-spin')).toBeNull()
    act(() => vi.advanceTimersByTime(300))
    expect(container.querySelector('.ant-skeleton')).not.toBeNull()
  } finally {
    vi.useRealTimers()
  }
})
