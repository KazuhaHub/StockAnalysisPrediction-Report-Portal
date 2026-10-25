import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import VendorSchedulePanel from './VendorSchedulePanel'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

describe('VendorSchedulePanel', () => {
  it('edits one source without changing another and exposes actual traffic and cooldown', () => {
    const onChange = vi.fn()
    render(<VendorSchedulePanel
      policies={{ tencent: { requestsPerMinute: 30, backgroundPerMinute: 6 }, sina: { requestsPerMinute: 20, backgroundPerMinute: 2 } }}
      sources={['tencent', 'sina']} sourceLabel={(name) => name}
      schedule={{ tencent: { requests: 91, failures: 3, deferred: 40, requestsLastMinute: 12, queued: 2, inFlight: 1,
        oldestWaitMillis: 2000, waitMillis: 3000, durationMillis: 9000, coolingUntil: '2026-10-26T08:00:00Z', probing: false } }}
      onChange={onChange}
    />)
    fireEvent.change(screen.getByRole('spinbutton', { name: 'tencent quoteAdmin.traffic.total' }), { target: { value: '24' } })
    expect(onChange).toHaveBeenLastCalledWith({ tencent: { requestsPerMinute: 24, backgroundPerMinute: 6 }, sina: { requestsPerMinute: 20, backgroundPerMinute: 2 } })
    expect(screen.getByText('91')).toBeTruthy()
    expect(screen.getByRole('columnheader', { name: 'quoteAdmin.sources' })).toBeTruthy()
    expect(screen.getByText('2026-10-26T08:00:00Z')).toBeTruthy()
    expect(screen.queryByText('0')).toBeNull()
  })
})
