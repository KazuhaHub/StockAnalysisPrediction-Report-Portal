import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
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
  limits: { session_ttl_hours: 168, login_fail_max: 10, login_fail_window_min: 15, apiv1_rate_per_min: 0 },
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
    <App>
      <SecurityPage />
    </App>,
  )
}

beforeEach(() => vi.clearAllMocks())

describe('the login-protection page', () => {
  it('renders the second-factor, recovery and lockout cards', async () => {
    mount()
    expect(await screen.findByText('security.twofaTitle')).toBeTruthy()
    expect(screen.getByText('security.recoveryTitle')).toBeTruthy()
    expect(screen.getByText('security.lockoutTitle')).toBeTruthy()
    // The switch means ALLOW TO ENROL, which is a reading an operator can get wrong from the label
    // alone — so the card has to say it.
    expect(screen.getByText('security.twofaLocalOnly')).toBeTruthy()
  })

  it('saves the blocks it renders', async () => {
    mount()
    await screen.findByText('security.twofaTitle')
    await userEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    const body = apiMock.post.mock.calls[0][1] as Record<string, any>
    expect(body.twofa).toEqual({ totp_enroll: true, passkey_enroll: true, require_staff: false })
    expect(body.recovery).toEqual({ enabled: true })
    expect(body.lockout).toEqual({ enabled: false, duration_min: 30, scope: 'ip_account' })
  })

  it('carries a changed switch into the block it belongs to', async () => {
    mount()
    await screen.findByText('security.twofaTitle')
    // Rows render label + switch in order; the first switch on the page is the login-mode one, so
    // this takes the switches by their card instead.
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
    await screen.findByText('security.twofaTitle')
    expect(screen.queryByText('security.lockoutDuration')).toBeNull()
    expect(screen.queryByText('security.lockoutAccountWarning')).toBeNull()
  })

  it('warns when recovery is on with no mail service to send the link', async () => {
    mount({ email_configured: false })
    expect(await screen.findByText('security.recoveryNeedsEmail')).toBeTruthy()
  })
})

  it('sends the mandate with the methods it needs', async () => {
    mount({ twofa: { totp_enroll: true, passkey_enroll: true, require_staff: true } })
    await screen.findByText('security.twofaTitle')
    await userEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    const body = apiMock.post.mock.calls[0][1] as Record<string, any>
    expect(body.twofa.require_staff).toBe(true)
  })
