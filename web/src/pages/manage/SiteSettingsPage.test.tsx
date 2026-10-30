import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import SiteSettingsPage from './SiteSettingsPage'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  upload: vi.fn(),
}))

const refreshMock = vi.hoisted(() => vi.fn())

vi.mock('../../api/client', () => ({
  api: apiMock,
}))

vi.mock('../../site', () => ({
  useSite: () => ({ refresh: refreshMock }),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

const loadedSettings = {
  oldBase: '',
  oldUser: '',
  hasPass: false,
  timezone: 'Asia/Shanghai',
  publicUrl: 'https://portal.example.com',
  siteTitle: '智研平台',
  siteLogoUrl: '/brand/logo.png',
  homeMoreStyle: 'popover',
  footerText: '<strong>备案</strong>',
  footerShowInfo: false,
  versionDisplay: 'header',
  pwaEnabled: false,
  pwaIconUrl: '/brand/app.png',
  announcementEnabled: true,
  announcementPopup: true,
  announcementLevel: 'error',
  announcementTitle: '不要被站点页覆盖',
  announcementContent: '公告正文也要保留。',
  newCount: 9,
}

function renderPage() {
  return render(
    <App>
      <SiteSettingsPage />
    </App>,
  )
}

describe('SiteSettingsPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.upload.mockReset()
    refreshMock.mockReset()
    // The page now also renders the IP-database section, which fetches its own status.
    apiMock.get.mockImplementation((url: string) =>
      url.includes('/geoip') ? Promise.resolve({}) : Promise.resolve({ ...loadedSettings }),
    )
    apiMock.post.mockResolvedValue({})
    refreshMock.mockResolvedValue({})
  })

  it('saves only site chrome settings so announcement settings are left untouched server-side', async () => {
    const user = userEvent.setup()
    renderPage()

    const title = await screen.findByPlaceholderText('brand')
    await user.clear(title)
    await user.type(title, '新站点名')

    await user.click(screen.getByRole('button', { name: /common\.save/ }))

    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(apiMock.post).toHaveBeenCalledWith('/api/admin/settings', {
      siteTitle: '新站点名',
      siteLogoUrl: '/brand/logo.png',
      footerText: '<strong>备案</strong>',
      footerShowInfo: false,
      versionDisplay: 'header',
      pwaEnabled: false,
      pwaIconUrl: '/brand/app.png',
      timezone: 'Asia/Shanghai',
      // The portal's own origin belongs with the deployment's settings, not on the email page —
      // reset links were only the first of seven things to derive from it.
      publicUrl: 'https://portal.example.com',
      // An absent stored policy reads as the default, so a first save writes the default rather
      // than dropping the field (which would leave it unknowable to a later reader).
      updatePromptPolicy: 'dismissible',
    })
    expect(Object.keys(apiMock.post.mock.calls[0][1])).not.toContain('announcementTitle')
    expect(Object.keys(apiMock.post.mock.calls[0][1])).not.toContain('announcementContent')
    expect(refreshMock).toHaveBeenCalledTimes(1)
  })

  // One radio group, not a pair of switches: "may the reader defer" and "must they refresh" are one
  // decision, and two booleans can spell a state nobody chose.
  it('shows the stored update policy and saves a change to it', async () => {
    const user = userEvent.setup()
    apiMock.get.mockImplementation((url: string) =>
      url.includes('/geoip') ? Promise.resolve({}) : Promise.resolve({ ...loadedSettings, updatePromptPolicy: 'persistent' }),
    )
    renderPage()

    await screen.findByPlaceholderText('brand')
    await user.click(screen.getByRole('tab', { name: 'settings.updatePrompt' }))
    const persisted = await screen.findByRole('radio', { name: /settings\.policy\.persistent/ })
    const automatic = screen.getByRole('radio', { name: /settings\.policy\.automatic/ })
    expect((persisted as HTMLInputElement).checked).toBe(true)

    await user.click(automatic)
    await user.click(screen.getByRole('button', { name: /common\.save/ }))

    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(apiMock.post.mock.calls[0][1].updatePromptPolicy).toBe('automatic')
  })

  it('preserves edits when moving between settings sections', async () => {
    const user = userEvent.setup()
    renderPage()
    const title = await screen.findByPlaceholderText('brand')
    await user.clear(title)
    await user.type(title, 'Changed brand')
    await user.click(screen.getByRole('tab', { name: 'settings.timeSection' }))
    expect(screen.getByRole('tab', { name: 'settings.timeSection' }).getAttribute('aria-selected')).toBe('true')
    await user.click(screen.getByRole('tab', { name: 'manage.section.branding' }))
    expect((screen.getByPlaceholderText('brand') as HTMLInputElement).value).toBe('Changed brand')
    await user.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/settings', expect.objectContaining({ siteTitle: 'Changed brand', publicUrl: loadedSettings.publicUrl })))
  })

  it('discards edits across sections without writing to the server', async () => {
    const user = userEvent.setup()
    renderPage()
    const title = await screen.findByPlaceholderText('brand')
    await user.clear(title)
    await user.type(title, 'Discard this')
    await user.click(screen.getByRole('tab', { name: 'settings.urlSection' }))
    await user.clear(screen.getByPlaceholderText('https://portal.example.com'))
    await user.type(screen.getByPlaceholderText('https://portal.example.com'), 'https://other.example.com')
    await user.click(screen.getByRole('button', { name: 'manage.discard' }))
    expect((screen.getByPlaceholderText('brand') as HTMLInputElement).value).toBe(loadedSettings.siteTitle)
    expect((screen.getByPlaceholderText('https://portal.example.com') as HTMLInputElement).value).toBe(loadedSettings.publicUrl)
    expect(apiMock.post).not.toHaveBeenCalled()
  })

  it('reveals an invalid field even when its section is hidden at save time', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByPlaceholderText('brand')
    await user.click(screen.getByRole('tab', { name: 'settings.urlSection' }))
    await user.clear(screen.getByPlaceholderText('https://portal.example.com'))
    await user.type(screen.getByPlaceholderText('https://portal.example.com'), 'invalid-url')
    await user.click(screen.getByRole('tab', { name: 'manage.section.branding' }))
    await user.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'settings.publicUrl' })).toBeTruthy())
    expect(apiMock.post).not.toHaveBeenCalled()
  })
})
