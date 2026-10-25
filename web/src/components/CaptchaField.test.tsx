import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import CaptchaField, { type CaptchaValue } from './CaptchaField'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

type WidgetOptions = {
  callback: (token: string) => void
  'expired-callback': () => void
  'error-callback': () => void
}

afterEach(() => {
  cleanup()
  apiMock.get.mockReset()
})

describe('CaptchaField hosted providers', () => {
  for (const tc of [
    { provider: 'turnstile', host: 'challenges.cloudflare.com', global: 'turnstile' },
    { provider: 'recaptcha', host: 'www.google.com', global: 'grecaptcha' },
    { provider: 'hcaptcha', host: 'js.hcaptcha.com', global: 'hcaptcha' },
  ] as const) {
    it(`renders ${tc.provider} and returns its token`, async () => {
      apiMock.get.mockResolvedValue({ required: true, provider: tc.provider, site_key: 'public-key' })
      const onChange = vi.fn<(value: CaptchaValue) => void>()
      const renderWidget = vi.fn((_container: HTMLElement, _options: WidgetOptions) => 17)

      render(<CaptchaField context="login" refresh={0} value={{}} onChange={onChange} />)

      const script = await waitFor(() => {
        const element = [...document.scripts].find((item) => new URL(item.src).host === tc.host)
        expect(element).toBeTruthy()
        return element!
      })
      const readyName = new URL(script.src).searchParams.get('onload')
      expect(readyName).toBeTruthy()
      Object.defineProperty(window, tc.global, {
        configurable: true,
        value: { render: renderWidget, reset: vi.fn(), remove: vi.fn() },
      })
      ;(window as unknown as Record<string, () => void>)[readyName!]()

      await waitFor(() => expect(renderWidget).toHaveBeenCalled())
      const options = renderWidget.mock.calls[0][1]
      expect(options).toEqual(expect.objectContaining({ sitekey: 'public-key' }))
      options.callback('provider-token')
      expect(onChange).toHaveBeenLastCalledWith({ captcha_token: 'provider-token' })

      options['expired-callback']()
      expect(onChange).toHaveBeenLastCalledWith({})
    })
  }

  it('does not load a hosted script when the public site key is missing', async () => {
    apiMock.get.mockResolvedValue({ required: true, provider: 'turnstile', site_key: '' })
    render(<CaptchaField context="register" refresh={0} value={{}} onChange={vi.fn()} />)

    expect(await screen.findByText('captcha.misconfigured')).toBeTruthy()
  })
})
