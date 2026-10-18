import { useCallback, useEffect, useState } from 'react'
import { App, Button, Space, Typography } from 'antd'
import { DesktopOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import type { LoginActivityResponse } from '../api/types'
import { auditTime } from '../lib/auditTime'
import { formatRegion } from '../lib/geo'
import { clientLabel } from '../lib/clientInfo'
import { startVisiblePoll } from '../lib/visiblePoll'

const POLL_MS = 5 * 60 * 1000
const MOBILE_QUERY = '(max-width: 575px)'

export function maskLoginIP(ip: string): string {
  if (!ip) return ''
  const parts = ip.split('.')
  if (parts.length === 4) return `${parts[0]}.${parts[1]}.${parts[2]}.•••`
  const groups = ip.split(':').filter(Boolean)
  return groups.length > 2 ? `${groups.slice(0, 3).join(':')}:•••` : ip
}

export default function LoginActivityNotice({ user }: { user: string }) {
  const { t, i18n } = useTranslation()
  const { notification } = App.useApp()
  const navigate = useNavigate()
  const [mobile, setMobile] = useState(() => {
    try {
      return window.matchMedia(MOBILE_QUERY).matches
    } catch {
      return false
    }
  })

  useEffect(() => {
    let media: MediaQueryList
    try {
      media = window.matchMedia(MOBILE_QUERY)
    } catch {
      return
    }
    const onChange = () => setMobile(media.matches)
    media.addEventListener?.('change', onChange)
    return () => media.removeEventListener?.('change', onChange)
  }, [])

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
    const place = formatRegion(latest.geo, i18n?.resolvedLanguage || i18n?.language || navigator.language)
    const when = auditTime(latest.at, data.timezone).text
    const ip = maskLoginIP(latest.ip)
    const client = clientLabel(
      latest.client,
      latest.client?.device_type ? t(`loginActivity.deviceType.${latest.client.device_type}`) : '',
    )
    notification.open({
      key: `login-activity:${user}`,
      className: 'rp-login-activity-notice',
      placement: mobile ? 'top' : 'topRight',
      duration: 0,
      style: {
        marginTop: 'calc(var(--rp-header-h, 64px) + env(safe-area-inset-top, 0px) + 12px)',
      },
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
          {client && <Typography.Text type="secondary"><DesktopOutlined /> {client}</Typography.Text>}
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
  }, [i18n?.language, i18n?.resolvedLanguage, mobile, navigate, notification, t, user])

  useEffect(() => startVisiblePoll(check, POLL_MS), [check])
  return null
}
