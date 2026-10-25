export type HostedCaptchaProvider = 'turnstile' | 'recaptcha' | 'hcaptcha'

export interface CaptchaWidgetAPI {
  render: (container: HTMLElement, options: Record<string, unknown>) => string | number
  reset: (widgetId?: string | number) => void
  remove?: (widgetId: string | number) => void
}

const providerConfig: Record<HostedCaptchaProvider, { callback: string; global: string; src: string }> = {
  turnstile: {
    callback: '__rpTurnstileReady',
    global: 'turnstile',
    src: 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit',
  },
  recaptcha: {
    callback: '__rpRecaptchaReady',
    global: 'grecaptcha',
    src: 'https://www.google.com/recaptcha/api.js?render=explicit',
  },
  hcaptcha: {
    callback: '__rpHCaptchaReady',
    global: 'hcaptcha',
    // Do not let hCaptcha install its reCAPTCHA compatibility global. An administrator can switch
    // providers without a page reload, so window.grecaptcha must always mean Google's SDK.
    src: 'https://js.hcaptcha.com/1/api.js?render=explicit&recaptchacompat=off',
  },
}

const loads = new Map<HostedCaptchaProvider, Promise<CaptchaWidgetAPI>>()

export function isHostedCaptchaProvider(provider: string): provider is HostedCaptchaProvider {
  return provider === 'turnstile' || provider === 'recaptcha' || provider === 'hcaptcha'
}

// Load each provider SDK at most once. The provider callback, rather than script.onload, is the
// readiness signal: reCAPTCHA and hCaptcha may still be initializing when the script load ends.
export function loadCaptchaProvider(provider: HostedCaptchaProvider): Promise<CaptchaWidgetAPI> {
  const existing = loads.get(provider)
  if (existing) return existing

  const cfg = providerConfig[provider]
  const promise = new Promise<CaptchaWidgetAPI>((resolve, reject) => {
    const globals = window as unknown as Record<string, unknown>
    globals[cfg.callback] = () => {
      const api = globals[cfg.global] as CaptchaWidgetAPI | undefined
      delete globals[cfg.callback]
      if (!api?.render || !api.reset) {
        loads.delete(provider)
        reject(new Error(`${provider} SDK loaded without its widget API`))
        return
      }
      resolve(api)
    }

    const url = new URL(cfg.src)
    url.searchParams.set('onload', cfg.callback)
    const script = document.createElement('script')
    script.src = url.toString()
    script.async = true
    script.defer = true
    script.dataset.captchaProvider = provider
    script.onerror = () => {
      delete globals[cfg.callback]
      script.remove()
      loads.delete(provider)
      reject(new Error(`could not load ${provider} SDK`))
    }
    document.head.appendChild(script)
  })
  loads.set(provider, promise)
  return promise
}
