import { useCallback, useEffect, useState } from 'react'
import { Alert, App, Button, Card, Empty, Popconfirm, Space, Spin, Tag, Typography } from 'antd'
import { DesktopOutlined, ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import type { AccountSessionsResponse } from '../api/types'
import { auditTime } from '../lib/auditTime'
import { clientLabel } from '../lib/clientInfo'

export default function AccountSessionsPanel() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [data, setData] = useState<AccountSessionsResponse | null>(null)
  const [failed, setFailed] = useState(false)
  const [busy, setBusy] = useState(false)
  const load = useCallback(async () => {
    try {
      const result = await api.get<AccountSessionsResponse>('/api/me/sessions')
      setData({ ...result, items: result.items ?? [] })
      setFailed(false)
    } catch { setFailed(true) }
  }, [])
  useEffect(() => { void load() }, [load])

  const revoke = async (idOrScope: string, bulk: boolean) => {
    setBusy(true)
    try {
      const result = bulk
        ? await api.post<{ signed_out: boolean }>('/api/me/sessions/revoke', { scope: idOrScope })
        : await api.del<{ signed_out: boolean }>(`/api/me/sessions/${encodeURIComponent(idOrScope)}`)
      if (result.signed_out) {
        window.location.assign('/login')
        return
      }
      message.success(t('sessions.revoked'))
      await load()
    } catch (e) { message.error(errText(e, t)) }
    finally { setBusy(false) }
  }

  const stamp = (value: number) => auditTime(new Date(value * 1000).toISOString(), data?.timezone ?? '').text
  return (
    <Card title={t('sessions.title')} extra={<Button aria-label={t('sessions.refresh')} icon={<ReloadOutlined />} disabled={busy} onClick={() => void load()} />}>
      <Typography.Paragraph type="secondary">{t('sessions.hint')}</Typography.Paragraph>
      <Space wrap style={{ marginBottom: 16 }}>
        <Popconfirm title={t('sessions.othersConfirm')} onConfirm={() => revoke('others', true)}>
          <Button disabled={busy} loading={busy}>{t('sessions.logoutOthers')}</Button>
        </Popconfirm>
        <Popconfirm title={t('sessions.allConfirm')} onConfirm={() => revoke('all', true)}>
          <Button danger disabled={busy}>{t('sessions.logoutAll')}</Button>
        </Popconfirm>
      </Space>
      {failed ? <Alert type="error" showIcon title={t('sessions.loadFailed')} /> : !data ? <Spin /> : !data.items.length ? <Empty /> : (
        <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
          {data.items.map((item) => (
            <div key={item.id} style={{ display: 'flex', justifyContent: 'space-between', flexWrap: 'wrap', gap: 12, padding: '12px 0', borderTop: '1px solid var(--ant-color-border-secondary)' }}>
              <Space orientation="vertical" size={3}>
                <Space wrap>
                  <Typography.Text strong><DesktopOutlined /> {clientLabel(item.client, item.client?.device_type ? t(`loginActivity.deviceType.${item.client.device_type}`) : '') || t('loginActivity.unknown')}</Typography.Text>
                  {item.current && <Tag color="blue">{t('sessions.current')}</Tag>}
                  {item.method && <Tag>{t(`account.loginMethod.${item.method}`)}</Tag>}
                </Space>
                <Typography.Text>{item.ip || t('loginActivity.unknown')}</Typography.Text>
                <Typography.Text type="secondary">{t('sessions.created', { time: stamp(item.created_at) })}</Typography.Text>
                <Typography.Text type="secondary">{t('sessions.lastSeen', { time: stamp(item.last_seen) })}</Typography.Text>
                <Typography.Text type="secondary">{t('sessions.expires', { time: stamp(item.expires_at) })}</Typography.Text>
              </Space>
              <Popconfirm title={t(item.current ? 'sessions.currentConfirm' : 'sessions.revokeConfirm')} onConfirm={() => revoke(item.id, false)}>
                <Button danger disabled={busy}>{t(item.current ? 'sessions.logoutCurrent' : 'sessions.revoke')}</Button>
              </Popconfirm>
            </div>
          ))}
        </Space>
      )}
    </Card>
  )
}
