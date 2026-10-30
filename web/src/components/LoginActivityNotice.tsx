import { useCallback, useEffect, useRef } from 'react'
import { DesktopOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import type { LoginActivityResponse } from '../api/types'
import { auditTime } from '../lib/auditTime'
import { formatRegion } from '../lib/geo'
import { clientLabel } from '../lib/clientInfo'
import { notify } from '../lib/notify'
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
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()

  // Bumped whenever the poll stops (unmount, or a new identity or language builds a new check), so a
  // response that was in flight then cannot raise a card for a reader who is no longer here.
  const generation = useRef(0)
  const check = useCallback(async () => {
    const started = generation.current
    let data: LoginActivityResponse
    try {
      data = await api.get<LoginActivityResponse>('/api/me/login-activity?limit=10')
    } catch {
      return
    }
    if (started !== generation.current) return
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
    notify({
      key: `login-activity:${user}`,
      kind: 'security',
      title:
        seen === 0
          ? t('loginActivity.recentNotice')
          : unseen > 1
            ? t('loginActivity.newNoticeCount', { count: unseen })
            : t('loginActivity.newNotice'),
      description: (
        <>
          <div>{[when, place].filter(Boolean).join(' · ')}</div>
          {client && <div><DesktopOutlined /> {client}</div>}
          {ip && <div>{t('loginActivity.ip')}: {ip}</div>}
        </>
      ),
      actions: [{ label: t('loginActivity.view'), primary: true, onClick: () => navigate('/account/login-activity') }],
    })
  }, [i18n?.language, i18n?.resolvedLanguage, navigate, t, user])

  useEffect(() => {
    const stop = startVisiblePoll(check, POLL_MS)
    return () => {
      generation.current++
      stop()
    }
  }, [check])
  return null
}
