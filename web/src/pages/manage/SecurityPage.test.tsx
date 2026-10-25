import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import SecurityPage from './SecurityPage'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock }))

// The convention in this directory: `t` keeps its arguments, so a test can assert the NUMBERS a
// sentence states rather than merely that a sentence appeared.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const policy = {
  captcha: {
    provider: 'image',
    site_key: '',
    has_secret: false,
    login: false,
    forgot: false,
    register: false,
    trigger: 'always',
    fail_threshold: '3',
  },
  registration: { enabled: false, require_verify: true, domains: '', default_group: '', expiry_days: '' },
  login: { mode: 'dual', effective: 'dual', sso_only: false, sso_available: false },
  limits: { session_ttl_hours: 168, login_fail_max: 10, login_fail_window_min: 15, apiv1_rate_per_min: 0,
    forecast_slots_official: 2, forecast_slots_explore: 2, forecast_wait_sec: 10, forecast_custom_per_min: 20, forecast_members_per_min: 60 },
  twofa: { totp_enroll: true, passkey_enroll: true, require_staff: false },
  recovery: { enabled: true },
  lockout: { enabled: false, duration_min: 30, scope: 'ip_account' },
  groups: [],
  email_configured: true,
}

function mount(overrides: Partial<typeof policy> = {}) {
  apiMock.get.mockResolvedValue({ ...policy, ...overrides })
  apiMock.post.mockResolvedValue({ ok: true })
  return render(
    <MemoryRouter><App>
      <SecurityPage />
    </App></MemoryRouter>,
  )
}

beforeEach(() => vi.clearAllMocks())

describe('the login-protection page', () => {
  it('edits all five bounded forecast limits and preserves zero exploration capacity', async () => {
    mount()
    await screen.findByText('security.forecastSlotsOfficial')
    await userEvent.click(screen.getByRole('button', { name: 'security.limitsTitle' }))
    const fields = [
      ['forecastSlotsOfficial', 'forecast_slots_official', 1, 8, 3],
      ['forecastSlotsExplore', 'forecast_slots_explore', 0, 8, 0],
      ['forecastWait', 'forecast_wait_sec', 1, 60, 5],
      ['forecastCustomRate', 'forecast_custom_per_min', 0, 600, 0],
      ['forecastMembersRate', 'forecast_members_per_min', 0, 600, 9],
    ] as const
    for (const [label, , min, max, value] of fields) {
      const input = screen.getByRole('spinbutton', { name: `security.${label}` })
      expect(input.getAttribute('aria-valuemin')).toBe(String(min))
      expect(input.getAttribute('aria-valuemax')).toBe(String(max))
      fireEvent.change(input, { target: { value: String(value) } })
      fireEvent.blur(input)
    }
    await userEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    const body = apiMock.post.mock.calls[0][1] as { limits: Record<string, number> }
    for (const [, key, , , value] of fields) expect(body.limits[key]).toBe(value)
  })
  it('offers second-factor, recovery and lockout settings as named sections', async () => {
    mount()
    expect(await screen.findByRole('button', { name: 'security.twofaTitle' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'security.recoveryTitle' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'security.lockoutTitle' })).toBeTruthy()
    // The switch means ALLOW TO ENROL, which is a reading an operator can get wrong from the label
    // alone — so the card has to say it.
    expect(screen.getByText('security.twofaLocalOnly')).toBeTruthy()
  })

  it('saves the blocks it renders', async () => {
    mount()
    await screen.findByRole('button', { name: 'security.twofaTitle' })
    await userEvent.click(screen.getByRole('switch'))
    await userEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    const body = apiMock.post.mock.calls[0][1] as Record<string, any>
    expect(body.twofa).toEqual({ totp_enroll: true, passkey_enroll: true, require_staff: false })
    expect(body.recovery).toEqual({ enabled: true, mode: 'email' })
    expect(body.lockout).toEqual({ enabled: false, duration_min: 30, scope: 'ip_account' })
  })

  it('carries a changed switch into the block it belongs to', async () => {
    mount()
    await screen.findByRole('button', { name: 'security.twofaTitle' })
    await userEvent.click(screen.getByRole('button', { name: 'security.lockoutTitle' }))
    const switches = screen.getAllByRole('switch')
    await userEvent.click(switches[switches.length - 1]) // the last card's only switch: the lockout
    await userEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    const body = apiMock.post.mock.calls[0][1] as Record<string, any>
    expect(body.lockout.enabled).toBe(true)
  })

  it('warns that the account scope can refuse a correct password', async () => {
    mount({ lockout: { enabled: true, duration_min: 30, scope: 'account' } })
    expect(await screen.findByText('security.lockoutAccountWarning')).toBeTruthy()
  })

  it('says nothing about the lockout while it is off', async () => {
    mount()
    await screen.findByRole('button', { name: 'security.twofaTitle' })
    expect(screen.queryByText('security.lockoutDuration')).toBeNull()
    expect(screen.queryByText('security.lockoutAccountWarning')).toBeNull()
  })

  it('warns when recovery is on with no mail service to send the link', async () => {
    mount({ email_configured: false })
    expect(await screen.findByText('security.recoveryNeedsEmail')).toBeTruthy()
  })

  it('preserves a draft between sections and discards it without a server write', async () => {
    mount()
    await screen.findByRole('button', { name: 'security.loginTitle' })
    await userEvent.click(screen.getByRole('switch'))
    await userEvent.click(screen.getByRole('button', { name: 'security.lockoutTitle' }))
    await userEvent.click(screen.getByRole('switch'))
    await userEvent.click(screen.getByRole('button', { name: 'security.loginTitle' }))
    expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('true')
    await userEvent.click(screen.getByRole('button', { name: 'manage.discard' }))
    expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('false')
    await userEvent.click(screen.getByRole('button', { name: 'security.lockoutTitle' }))
    expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('false')
    expect(apiMock.post).not.toHaveBeenCalled()
  })
})

  it('sends the mandate with the methods it needs', async () => {
    mount({ twofa: { totp_enroll: true, passkey_enroll: true, require_staff: true } })
    await screen.findByRole('button', { name: 'security.twofaTitle' })
    await userEvent.click(screen.getByRole('switch'))
    await userEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    const body = apiMock.post.mock.calls[0][1] as Record<string, any>
    expect(body.twofa.require_staff).toBe(true)
  })
