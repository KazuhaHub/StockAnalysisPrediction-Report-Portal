import { useEffect, useState } from 'react'
import { Button, Card, Form, Input, Result, Space, Typography, theme, Spin } from 'antd'
import { QuestionCircleOutlined, UserOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errText } from '../api/client'
import { loadFailure } from '../lib/loadFailure'
import CaptchaField, { type CaptchaValue } from '../components/CaptchaField'

// Requesting a password-reset link.
//
// A page, not a dialog, and for the same reason registration is one: it is a form with a captcha,
// a failure state and a result state, and a modal has nowhere to put those without covering the
// thing behind it. It is also linkable — an admin can send someone straight here.

export default function ForgotPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { token } = theme.useToken()
  const [account, setAccount] = useState('')
  const [captcha, setCaptcha] = useState<CaptchaValue>({})
  const [captchaRound, setCaptchaRound] = useState(0)
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState(false)
  const [err, setErr] = useState('')
  const [mode, setMode] = useState<string | null>(null)
  useEffect(() => {
    const load = () => api.get<{ mode: string }>('/api/password/config').then(r => { setMode(r.mode ?? 'email'); setErr('') }).catch(e => { const text = loadFailure(e, t); if (text !== null) setErr(text) })
    void load()
    window.addEventListener('rp:connection-restored', load)
    return () => window.removeEventListener('rp:connection-restored', load)
  }, [t])

  const submit = async () => {
    if (!account.trim()) { setErr(t('login.forgotRequired')); return }
    if (mode !== 'email' || busy) return
    setBusy(true)
    setErr('')
    try {
      const response = await api.post<{ ok: boolean }>('/api/password/forgot', { account: account.trim(), ...captcha })
      if (response?.ok !== true) throw new ApiError(502, 'Unexpected response', 'server_unavailable')
      setSent(true)
    } catch (e) {
      setErr(errText(e, t))
      if (e instanceof ApiError && e.code === 'captcha_failed') setCaptchaRound(n => n + 1)
    } finally {
      setBusy(false)
    }
  }

  const shell = (children: React.ReactNode) => (
    <div
      style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', padding: 24, background: token.colorBgLayout }}
    >
      <Card style={{ width: 420, maxWidth: '100%' }}>
        <Space orientation="vertical" size={16} style={{ width: '100%' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <QuestionCircleOutlined style={{ fontSize: 24, display: 'flex', flexShrink: 0, lineHeight: 1 }} />
            <Typography.Title level={4} style={{ margin: 0, lineHeight: 1.25 }}>
              {t('login.forgotTitle')}
            </Typography.Title>
          </div>
          {children}
        </Space>
      </Card>
    </div>
  )

  if (!mode) return shell(err ? <Typography.Text role="alert" type="danger">{err}</Typography.Text> : <Spin />)
  if (mode !== 'email') return shell(<Result status="info" subTitle={t(mode === 'contact' ? 'login.recoveryContact' : 'login.recoveryHidden')}
    extra={<Button onClick={() => navigate('/login')}>{t('register.backToLogin')}</Button>} />)

  if (sent) {
    return shell(
      <Result
        status="success"
        subTitle={t('login.forgotSent')}
        extra={
          <Button type="primary" onClick={() => navigate('/login')}>
            {t('register.backToLogin')}
          </Button>
        }
      />,
    )
  }

  return shell(
    <Form layout="vertical" onFinish={submit} requiredMark={false}>
      <Typography.Text type="secondary">{t('login.forgotHint')}</Typography.Text>
      <Form.Item name="account" rules={[{ required: true, whitespace: true, message: t('login.forgotRequired') }]} style={{ marginTop: 12, marginBottom: 12 }}>
        <Input
          size="large"
          autoFocus
          prefix={<UserOutlined />}
          placeholder={t('login.forgotAccount')}
          value={account}
          onChange={(e) => setAccount(e.target.value)}
        />
      </Form.Item>
      {/* The form whose abuse is an outbound-mail flood at somebody else's inbox — the one the
          captcha toggle names. It went years without one on screen. */}
      <CaptchaField context="forgot" account={account} refresh={captchaRound} value={captcha} onChange={setCaptcha} />
      {err && (
        <Typography.Text role="alert" type="danger" style={{ display: 'block', marginBottom: 12 }}>
          {err}
        </Typography.Text>
      )}
      <Button type="primary" size="large" htmlType="submit" block loading={busy}>
        {t('login.forgotSend')}
      </Button>
      {/* Set off from the primary action rather than tucked under its edge: a link touching the
          button it undoes is a misclick waiting to happen, and on the reset form the misclick
          throws away a captcha the person has just typed. */}
      <Button type="link" size="small" block style={{ marginTop: 12 }} onClick={() => navigate('/login')}>
        {t('register.backToLogin')}
      </Button>
    </Form>,
  )
}
