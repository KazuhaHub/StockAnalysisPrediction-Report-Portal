import { useCallback, useEffect } from 'react'
import { App, Button, Space, Typography } from 'antd'
import { SafetyCertificateOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import type { LoginActivityResponse } from '../api/types'
import { auditTime } from '../lib/auditTime'
import { formatRegion } from '../lib/geo'
import { startVisiblePoll } from '../lib/visiblePoll'

const POLL_MS = 5 * 60 * 1000

export function maskLoginIP(ip: string): string {
  if (!ip) return ''
  const parts = ip.split('.')
  if (parts.length === 4) return `${parts[0]}.${parts[1]}.${parts[2]}.•••`
  const groups = ip.split(':').filter(Boolean)
  return groups.length > 2 ? `${groups.slice(0, 3).join(':')}:•••` : ip
}

export default function LoginActivityNotice({ user }: { user: string }) {
  const { t } = useTranslation()
  const { notification } = App.useApp()
  const navigate = useNavigate()

  const check = useCallback(async () => {
    let data: LoginActivityResponse
    try {
      data = await api.get<LoginActivityResponse>('/api/me/login-activity?limit=10')
    } catch {
      return
    }
    const latest = data.items?.[0]
    if (!latest || !Number.isFinite(latest.id)) return

    const storageKey = `rp:login-activity-seen:${user}`
    let seen = 0
    try {
      seen = Number.parseInt(localStorage.getItem(storageKey) || '0', 10) || 0
    } catch {
      // Storage can be disabled. The notice still works for this page view; it may repeat later.
    }
    if (latest.id <= seen) return

    const unseen = seen > 0 ? data.items.filter((item) => item.id > seen).length : 0
    try {
      localStorage.setItem(storageKey, String(latest.id))
    } catch {
      // See the read above: persistence is an optimization, never a security boundary.
    }
    const place = formatRegion(latest.geo)
    const when = auditTime(latest.at, data.timezone).text
    const ip = maskLoginIP(latest.ip)
    notification.open({
      key: `login-activity:${user}`,
      placement: 'topRight',
      duration: 8,
      icon: <SafetyCertificateOutlined style={{ color: '#1677ff' }} />,
      message:
        seen === 0
          ? t('loginActivity.recentNotice')
          : unseen > 1
            ? t('loginActivity.newNoticeCount', { count: unseen })
            : t('loginActivity.newNotice'),
      description: (
        <Space orientation="vertical" size={4}>
          <Typography.Text>{[when, place].filter(Boolean).join(' · ')}</Typography.Text>
          {ip && <Typography.Text type="secondary">{t('loginActivity.ip')}: {ip}</Typography.Text>}
          <Button
            type="link"
            size="small"
            style={{ paddingInline: 0 }}
            onClick={() => {
              notification.destroy(`login-activity:${user}`)
              navigate('/account/login-activity')
            }}
          >
            {t('loginActivity.view')}
          </Button>
        </Space>
      ),
    })
  }, [navigate, notification, t, user])

  useEffect(() => startVisiblePoll(check, POLL_MS), [check])
  return null
}
