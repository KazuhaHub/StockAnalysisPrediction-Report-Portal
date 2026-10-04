import { useEffect, useState } from 'react'
import { Alert, App, Button, Empty, InputNumber, Popconfirm, Space, Spin, Tag, Typography, theme } from 'antd'
import { DesktopOutlined, EnvironmentOutlined, GlobalOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import type { LoginActivityResponse } from '../api/types'
import { auditTime } from '../lib/auditTime'
import { formatRegion } from '../lib/geo'
import { clientLabel } from '../lib/clientInfo'

export default function LoginActivityPanel() {
  const { t, i18n } = useTranslation()
  const { token } = theme.useToken()
  const [data, setData] = useState<LoginActivityResponse | null>(null)
  const [failed, setFailed] = useState(false)
  const { message } = App.useApp()
  const [keep, setKeep] = useState<number | null>(100)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    let live = true
    api
      .get<LoginActivityResponse>('/api/me/login-activity?limit=10')
      .then((r) => {
        if (live) { setData(r); setKeep(r.keep ?? 100) }
      })
      .catch(() => {
        if (live) setFailed(true)
      })
    return () => {
      live = false
    }
  }, [])

  if (failed) return <Alert type="error" showIcon title={t('loginActivity.loadFailed')} />
  if (!data) return <div style={{ minHeight: 180, display: 'grid', placeItems: 'center' }}><Spin /></div>

  const saveRetention = async () => {
    if (keep === null || !Number.isInteger(keep) || keep < 1 || keep > 10000) return
    setSaving(true)
    try {
      await api.put('/api/me/login-activity/retention', { keep })
      setData(await api.get<LoginActivityResponse>('/api/me/login-activity?limit=10'))
      message.success(t('loginActivity.retentionSaved'))
    } catch (e) { message.error(errText(e, t)) }
    finally { setSaving(false) }
  }

  return (
    <div style={{ width: '100%' }}>
      <Typography.Paragraph type="secondary">{t('loginActivity.hint')}</Typography.Paragraph>
      <Space wrap style={{ marginBottom: 8 }}>
        <Typography.Text>{t('loginActivity.keep')}</Typography.Text>
        <InputNumber aria-label={t('loginActivity.keep')} min={1} max={10000} precision={0} value={keep} onChange={setKeep} disabled={saving} />
        <Popconfirm title={t('loginActivity.retentionConfirm', { count: keep })} onConfirm={saveRetention}>
          <Button loading={saving} disabled={keep === null || !Number.isInteger(keep) || keep < 1 || keep > 10000 || keep === (data.keep ?? 100)}>{t('common.save')}</Button>
        </Popconfirm>
      </Space>
      <Typography.Paragraph type="secondary">{t('loginActivity.retentionHint')}</Typography.Paragraph>
      {!data.items?.length && <Empty description={t('loginActivity.empty')} />}
      <div role="list" style={{ border: `1px solid ${token.colorBorderSecondary}`, borderRadius: token.borderRadiusLG, overflow: 'hidden' }}>
        {data.items.map((item, index) => {
          const at = auditTime(item.at, data.timezone)
          const place = formatRegion(item.geo, i18n?.resolvedLanguage || i18n?.language || navigator.language)
          const methodKey = `account.loginMethod.${item.method || 'unknown'}`
          const client = clientLabel(item.client, item.client?.device_type ? t(`loginActivity.deviceType.${item.client.device_type}`) : '')
          return (
            <div
              role="listitem"
              key={item.id}
              style={{ padding: '16px 20px', borderTop: index ? `1px solid ${token.colorBorderSecondary}` : undefined }}
            >
              <div style={{ width: '100%', display: 'flex', gap: 16, justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap' }}>
                <Space orientation="vertical" size={3}>
                  <Space wrap>
                    <Typography.Text strong>{at.text}</Typography.Text>
                    {index === 0 && <Tag color="blue">{t('loginActivity.latest')}</Tag>}
                  </Space>
                  {at.local && <Typography.Text type="secondary">{t('loginActivity.localTime', { time: at.local })}</Typography.Text>}
                  <Space wrap size={12}>
                    <Typography.Text><GlobalOutlined /> {item.ip || t('loginActivity.unknown')}</Typography.Text>
                    <Typography.Text type={place ? undefined : 'secondary'}>
                      <EnvironmentOutlined /> {place || t('loginActivity.locationUnknown')}
                    </Typography.Text>
                  </Space>
                  {client && <Typography.Text type="secondary"><DesktopOutlined /> {client}</Typography.Text>}
                </Space>
                <Tag icon={<SafetyCertificateOutlined />}>{t(methodKey)}</Tag>
              </div>
            </div>
          )
        })}
      </div>
      {data.total > data.items.length && (
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          {t('loginActivity.showing', { count: data.items.length, total: data.total })}
        </Typography.Paragraph>
      )}
    </div>
  )
}
