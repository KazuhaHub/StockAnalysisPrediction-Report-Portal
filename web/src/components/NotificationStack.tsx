import { useEffect, useState, useSyncExternalStore, type CSSProperties, type ReactNode } from 'react'
import { Button, theme } from 'antd'
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CloseOutlined,
  DownloadOutlined,
  ExclamationCircleOutlined,
  SafetyCertificateOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import {
  ASSERTIVE_KINDS,
  MAX_VISIBLE_NOTIFICATIONS,
  attachNotificationStack,
  dismissNotification,
  getNotifications,
  holdNotification,
  subscribeNotifications,
  visibleKeys,
  type NotificationItem,
  type NotifyKind,
} from '../lib/notify'
import { QUEUE_EVENT } from '../lib/shortcuts'
import { formatTaskTime } from '../lib/taskUi'

const ICONS: Record<NotifyKind, ReactNode> = {
  security: <SafetyCertificateOutlined />,
  failure: <CloseCircleOutlined />,
  partial: <ExclamationCircleOutlined />,
  actionable: <DownloadOutlined />,
  success: <CheckCircleOutlined />,
}

function Card({ item, now }: { item: NotificationItem; now: number }) {
  const { t, i18n } = useTranslation()
  const { token } = theme.useToken()
  const [hover, setHover] = useState(false)
  const [focus, setFocus] = useState(false)
  const hot = hover || focus
  const color: Record<NotifyKind, string> = {
    security: token.purple,
    failure: token.colorError,
    partial: token.colorWarning,
    actionable: token.colorPrimary,
    success: token.colorSuccess,
  }
  const assertive = ASSERTIVE_KINDS.has(item.kind)
  const time = formatTaskTime(item.createdAt, i18n?.language || 'en-US', now, t('tasks.justNow'))
  return (
    <div
      className={`rp-notify-card${item.leaving ? ' is-leaving' : ''}`}
      role={assertive ? 'alert' : 'status'}
      aria-live={assertive ? 'assertive' : 'polite'}
      data-kind={item.kind}
      onMouseEnter={() => {
        setHover(true)
        holdNotification(item.key, 'hover', true)
      }}
      onMouseLeave={() => {
        setHover(false)
        holdNotification(item.key, 'hover', false)
      }}
      onFocus={() => {
        setFocus(true)
        holdNotification(item.key, 'focus', true)
      }}
      onBlur={(e) => {
        if (e.currentTarget.contains(e.relatedTarget as Node | null)) return
        setFocus(false)
        holdNotification(item.key, 'focus', false)
      }}
    >
      <button
        type="button"
        className="rp-notify-close"
        aria-label={t('notifications.close')}
        data-visible={hot ? 'true' : 'false'}
        onClick={() => dismissNotification(item.key, 'manual')}
      >
        <CloseOutlined />
      </button>
      <span className="rp-notify-icon" style={{ background: color[item.kind] }} aria-hidden="true">
        {ICONS[item.kind]}
      </span>
      <div className="rp-notify-body">
        <div className="rp-notify-top">
          <span className="rp-notify-title">{item.title}</span>
          {time && <span className="rp-notify-time">{time}</span>}
        </div>
        {item.description && <div className="rp-notify-desc">{item.description}</div>}
        {item.actions.length > 0 && (
          <div className="rp-notify-actions">
            {item.actions.map((a, i) => (
              <Button
                key={i}
                size="small"
                type={a.primary ? 'primary' : 'default'}
                href={a.href}
                onClick={() => {
                  a.onClick?.()
                  if (!a.keepOpen) dismissNotification(item.key, 'action')
                }}
              >
                {a.label}
              </Button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

/** Draws the notifications raised through notify(); mount once in the signed-in shell. */
export default function NotificationStack() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const items = useSyncExternalStore(subscribeNotifications, getNotifications, getNotifications)
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => attachNotificationStack(), [])
  useEffect(() => {
    if (items.length === 0) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [items.length === 0]) // eslint-disable-line react-hooks/exhaustive-deps

  const shown = visibleKeys(items)
  const waiting = items.filter((i) => !i.leaving).length - MAX_VISIBLE_NOTIFICATIONS
  const vars = {
    '--rp-n-bg': `color-mix(in srgb, ${token.colorBgElevated} 78%, transparent)`,
    '--rp-n-solid': token.colorBgElevated,
    '--rp-n-border': token.colorBorderSecondary,
    '--rp-n-text': token.colorText,
    '--rp-n-muted': token.colorTextSecondary,
    '--rp-n-close-bg': token.colorBgElevated,
    '--rp-n-shadow': token.boxShadowSecondary,
  } as CSSProperties
  return (
    <div className="rp-notify-stack" role="region" aria-label={t('notifications.region')} style={vars}>
      {items
        .filter((i) => shown.has(i.key) || i.leaving)
        .map((i) => (
          <Card key={i.key} item={i} now={now} />
        ))}
      {waiting > 0 && (
        <button type="button" className="rp-notify-more" onClick={() => window.dispatchEvent(new Event(QUEUE_EVENT))}>
          {t('notifications.more', { count: waiting })}
        </button>
      )}
    </div>
  )
}
