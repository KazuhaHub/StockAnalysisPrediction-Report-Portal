import { useEffect, useState } from 'react'
import { Badge, Button, Tooltip } from 'antd'
import { BellOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { notificationInbox } from '../api/notifications'
import { startVisiblePoll } from '../lib/visiblePoll'

export const INBOX_CHANGED = 'rp:inbox-changed'

export default function NotificationBell({ user }: { user: string }) {
  const { t } = useTranslation()
  const [count, setCount] = useState<number>()
  useEffect(() => {
    let alive = true
    setCount(undefined)
    const refresh = async () => {
      try { const result = await notificationInbox.unread(); if (alive) setCount(result.unread) } catch { if (alive) setCount(undefined) }
    }
    const stop = startVisiblePoll(refresh, 30000)
    const onChange = () => { if (document.visibilityState === 'visible') void refresh() }
    window.addEventListener(INBOX_CHANGED, onChange)
    return () => { alive = false; stop(); window.removeEventListener(INBOX_CHANGED, onChange) }
  }, [user])
  return <Tooltip title={t('inbox.title')}><Link to="/notifications"><Badge count={count} overflowCount={99}><Button type="text" shape="circle" className="rp-header-icon" aria-label={t('inbox.title')} icon={<BellOutlined />} /></Badge></Link></Tooltip>
}
