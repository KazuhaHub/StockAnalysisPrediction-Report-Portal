import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Input, Space, Typography } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { clickable } from '../lib/clickable'
import {
  isHostedCaptchaProvider,
  loadCaptchaProvider,
  type HostedCaptchaProvider,
} from '../lib/captchaProvider'

// The captcha field on a public form.
//
// It renders NOTHING until the server says a captcha is required for this caller, which is what
// makes the after-failures mode work: an ordinary visitor on a quiet portal never sees it, and it
// appears the moment the server starts asking. The parent re-arms it after a refusal by bumping
// `refresh`, because a challenge is consumed on use and a stale one would fail forever.
//
// Hosted providers are loaded only when the server requires a challenge. Their public SDK produces
// a short-lived token; the parent sends it in JSON and the server verifies it with the secret. A
// provider outage fails closed, while the built-in image provider remains network-independent.

export interface CaptchaValue {
  captcha_id?: string
  captcha_answer?: string
  captcha_token?: string
}

interface CaptchaState {
  required: boolean
  provider: string
  site_key?: string
  captcha_id?: string
  image?: string
}

function HostedCaptcha({
  provider,
  siteKey,
  context,
  refresh,
  onChange,
}: {
  provider: HostedCaptchaProvider
  siteKey: string
  context: 'login' | 'forgot' | 'register'
  refresh: number
  onChange: (v: CaptchaValue) => void
}) {
  const { t } = useTranslation()
  const container = useRef<HTMLDivElement>(null)
  const changeRef = useRef(onChange)
  const [attempt, setAttempt] = useState(0)
  const [failed, setFailed] = useState(false)
  changeRef.current = onChange

  useEffect(() => {
    const element = container.current
    if (!element || !siteKey) return
    let disposed = false
    let widgetId: string | number | undefined
    let widgetAPI: Awaited<ReturnType<typeof loadCaptchaProvider>> | undefined
    changeRef.current({})
    setFailed(false)

    loadCaptchaProvider(provider)
      .then((api) => {
        if (disposed) return
        widgetAPI = api
        const clear = () => {
          if (!disposed) changeRef.current({})
        }
        widgetId = api.render(element, {
          sitekey: siteKey,
          theme: document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light',
          ...(provider === 'turnstile' ? { action: context } : {}),
          callback: (token: string) => {
            if (!disposed) {
              setFailed(false)
              changeRef.current({ captcha_token: token })
            }
          },
          'expired-callback': clear,
          'error-callback': () => {
            clear()
            if (!disposed) setFailed(true)
          },
        })
      })
      .catch(() => {
        if (!disposed) setFailed(true)
      })

    return () => {
      disposed = true
      if (widgetAPI && widgetId !== undefined) {
        try {
          if (widgetAPI.remove) widgetAPI.remove(widgetId)
          else widgetAPI.reset(widgetId)
        } catch {
          // An expired or failed widget may already have removed itself.
        }
      }
    }
  }, [attempt, context, provider, refresh, siteKey])

  if (!siteKey) {
    return <Typography.Text type="danger">{t('captcha.misconfigured')}</Typography.Text>
  }

  return (
    <div style={{ marginBottom: 12 }}>
      <div key={`${provider}-${refresh}-${attempt}`} ref={container} />
      {failed && (
        <Space orientation="vertical" size="small">
          <Typography.Text type="danger">{t('captcha.loadFailed')}</Typography.Text>
          <Button size="small" onClick={() => setAttempt((value) => value + 1)}>
            {t('captcha.retry')}
          </Button>
        </Space>
      )}
    </div>
  )
}

export default function CaptchaField({
  context,
  account,
  refresh,
  value,
  onChange,
}: {
  context: 'login' | 'forgot' | 'register'
  account?: string
  refresh: number
  value: CaptchaValue
  onChange: (v: CaptchaValue) => void
}) {
  const { t } = useTranslation()
  const [state, setState] = useState<CaptchaState | null>(null)

  const load = useCallback(() => {
    const q = account ? `&account=${encodeURIComponent(account)}` : ''
    api
      .get<CaptchaState>(`/api/captcha?ctx=${context}${q}`)
      .then((r) => {
        setState(r)
        onChange(r.provider === 'image' ? { captcha_id: r.captcha_id, captcha_answer: '' } : {})
      })
      .catch(() => setState(null))
    // onChange is a fresh closure on every parent render; depending on it would refetch in a loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [context, account, refresh])
  useEffect(() => {
    load()
    window.addEventListener('rp:connection-restored', load)
    return () => window.removeEventListener('rp:connection-restored', load)
  }, [load])

  if (!state?.required) return null

  if (isHostedCaptchaProvider(state.provider)) {
    return (
      <HostedCaptcha
        provider={state.provider}
        siteKey={state.site_key?.trim() ?? ''}
        context={context}
        refresh={refresh}
        onChange={onChange}
      />
    )
  }

  if (state.provider !== 'image') {
    return <Typography.Text type="danger">{t('captcha.misconfigured')}</Typography.Text>
  }

  return (
    <div className="rp-captcha-field">
      <Input
        value={value.captcha_answer ?? ''}
        onChange={(e) => onChange({ captcha_id: state.captcha_id, captcha_answer: e.target.value })}
        placeholder={t('captcha.placeholder')}
        inputMode="numeric"
        autoComplete="off"
        maxLength={8}
      />
      {state.image && (
        <img
          src={state.image}
          alt={t('captcha.alt')}
          {...clickable(load, t('captcha.refresh'))}
          className="rp-captcha-image"
          title={t('captcha.refresh')}
        />
      )}
      <Button type="text" icon={<ReloadOutlined />} onClick={load} aria-label={t('captcha.refresh')} title={t('captcha.refresh')} />
    </div>
  )
}
