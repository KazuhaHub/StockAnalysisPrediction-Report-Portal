import { useState } from 'react'
import { Alert, App, Button, Card, Input, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api, ApiError, errText } from '../../api/client'
import type { ForecastChannelResult, ForecastSettings } from '../../api/types'

// The alert channels (P41). Readiness item 5 is "a test alert reached BOTH channels in the last 30
// days", so this card has to make both facts visible: whether a channel is configured at all, and
// what its last delivery did.
//
// The Slack URL is write-only. The field starts empty even when one is stored — the placeholder says
// so — and a blank Save does not clear it. Clearing is an explicit button, because a form that cleared
// the secret when an admin only meant to edit the recipients would silently stop the alerts.
export default function ForecastAlerts({
  settings,
  onChanged,
}: {
  settings: ForecastSettings
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const alerts = settings.alerts
  const [email, setEmail] = useState(alerts.email_to)
  const [slack, setSlack] = useState('')
  const [busy, setBusy] = useState(false)
  const [testing, setTesting] = useState(false)
  const [results, setResults] = useState<Record<string, ForecastChannelResult> | null>(null)

  const configured = alerts.email_to.trim() !== '' || alerts.has_slack

  const post = async (body: Record<string, unknown>, success: string) => {
    setBusy(true)
    try {
      await api.post('/api/admin/forecast', body)
      message.success(success)
      onChanged()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  const save = () => {
    const body: Record<string, unknown> = {}
    if (email.trim() !== alerts.email_to) body.alert_email_to = email.trim()
    if (slack.trim() !== '') body.alert_slack_url = slack.trim()
    if (Object.keys(body).length === 0) return
    void post(body, t('common.saved')).then(() => setSlack(''))
  }

  const clearSlack = () => {
    void post({ alert_slack_url: '' }, t('common.saved'))
  }

  const sendTest = async () => {
    setTesting(true)
    setResults(null)
    try {
      const r = await api.post<{ ok: boolean; results: Record<string, ForecastChannelResult> }>(
        '/api/admin/forecast/alert-test',
        {},
      )
      setResults(r.results)
      message.success(t('forecast.alert.testOK'))
      onChanged()
    } catch (e) {
      if (e instanceof ApiError) {
        const data = e.data as { results?: Record<string, ForecastChannelResult> } | undefined
        if (data?.results) setResults(data.results)
      }
      message.error(errText(e, t))
    } finally {
      setTesting(false)
    }
  }

  const lastRow = (channel: 'email' | 'slack') => {
    const last = results?.[channel] ?? alerts.last?.[channel]
    if (!last) return <Typography.Text type="secondary">{t('forecast.alert.never')}</Typography.Text>
    return (
      <Space size={6}>
        <Typography.Text type={last.ok ? 'success' : 'danger'}>
          {last.ok ? '✓' : '✗'}
        </Typography.Text>
        {last.at && <Typography.Text type="secondary">{dayjs(last.at).format('YYYY-MM-DD HH:mm')}</Typography.Text>}
        {last.error && <Typography.Text type="danger">{last.error}</Typography.Text>}
      </Space>
    )
  }

  return (
    <Card title={t('forecast.alert.title')}>
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Text type="secondary">{t('forecast.alert.hint')}</Typography.Text>
        {!configured && <Alert type="warning" showIcon title={t('forecast.alert.none')} />}

        <div>
          <Typography.Text>{t('forecast.alert.email')}</Typography.Text>
          <Input
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder={t('forecast.alert.emailPlaceholder')}
            aria-label={t('forecast.alert.email')}
          />
        </div>

        <div>
          <Typography.Text>{t('forecast.alert.slack')}</Typography.Text>
          <Space.Compact style={{ width: '100%' }}>
            <Input.Password
              value={slack}
              onChange={(e) => setSlack(e.target.value)}
              placeholder={alerts.has_slack ? t('forecast.alert.slackSet') : t('forecast.alert.slackPlaceholder')}
              aria-label={t('forecast.alert.slack')}
            />
            {alerts.has_slack && (
              <Button onClick={clearSlack} disabled={busy}>
                {t('forecast.alert.slackClear')}
              </Button>
            )}
          </Space.Compact>
        </div>

        <Space wrap>
          <Button onClick={save} loading={busy} disabled={busy || testing}>
            {t('forecast.alert.save')}
          </Button>
          <Button onClick={sendTest} loading={testing} disabled={busy || testing}>
            {t('forecast.alert.test')}
          </Button>
          {alerts.test_ok_at ? (
            <Typography.Text type="secondary">
              {t('forecast.alert.verifiedAt', { at: dayjs(alerts.test_ok_at).format('YYYY-MM-DD HH:mm') })}
            </Typography.Text>
          ) : (
            <Typography.Text type="secondary">{t('forecast.alert.notVerified')}</Typography.Text>
          )}
        </Space>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <Typography.Text strong>{t('forecast.alert.last')}</Typography.Text>
          <Typography.Text>
            {t('forecast.alert.channel.email')}: {lastRow('email')}
          </Typography.Text>
          <Typography.Text>
            {t('forecast.alert.channel.slack')}: {lastRow('slack')}
          </Typography.Text>
        </div>
      </Space>
    </Card>
  )
}
