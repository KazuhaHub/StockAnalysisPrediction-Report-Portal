import { useEffect, useRef, useState } from 'react'
import { Badge, Button, Empty, Popover, Tooltip, Typography, theme } from 'antd'
import { BellOutlined, CheckOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { notificationInbox, type InboxItem } from '../api/notifications'
import { errText } from '../api/client'
import { startVisiblePoll } from '../lib/visiblePoll'
import { loadFailure } from '../lib/loadFailure'
import { notifyError } from '../lib/notify'
import { formatTaskTime } from '../lib/taskUi'
import LoadGate from './LoadGate'

export const INBOX_CHANGED = 'rp:inbox-changed'
/** How many of the latest notifications the bell's preview shows; the rest are on /notifications. */
export const PREVIEW_SIZE = 5

// The header bell. A click opens a preview of the latest notifications in place (a popover, as in
// GitHub, Google Workspace and the Cloudflare dashboard), with mark-as-read and a link to the full
// page, so reading a notification does not take the user away from what they were doing.
export default function NotificationBell({ user }: { user: string }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const [count, setCount] = useState<number>()
  const [open, setOpen] = useState(false)
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
  return (
    <Popover
      trigger="click"
      placement="bottomRight"
      // On a phone the bell is not at the window's edge (the account menu is to its right), so a
      // right-aligned panel would run off the left side: shift it back into the window instead.
      align={{ overflow: { shiftX: true, adjustY: true } }}
      open={open}
      onOpenChange={setOpen}
      destroyOnHidden
      classNames={{ root: 'rp-inbox-popover' }}
      styles={{ container: { padding: 0, borderRadius: token.borderRadiusLG } }}
      content={<InboxPreview onClose={() => setOpen(false)} />}
    >
      {/* A plain title, like the apps launcher: a Tooltip nested in the Popover's trigger swallowed
          the click that reopens the panel while the pointer still rests on the bell. */}
      <Badge count={count} overflowCount={99}>
        <Button type="text" className="rp-header-icon" aria-label={t('inbox.title')} title={t('inbox.title')} aria-haspopup="dialog" aria-expanded={open} icon={<BellOutlined />} />
      </Badge>
    </Popover>
  )
}

function InboxPreview({ onClose }: { onClose: () => void }) {
  const { t, i18n } = useTranslation()
  const lang = i18n?.language || 'en-US'
  const [items, setItems] = useState<InboxItem[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  // Unread across the whole inbox, not just the rows shown: older unread items still need the action.
  const [unread, setUnread] = useState(0)
  const alive = useRef(true)
  const load = async () => {
    try {
      const result = await notificationInbox.list()
      if (!alive.current) return
      setItems(result.items.slice(0, PREVIEW_SIZE))
      setUnread(result.unread)
      setError('')
    } catch (e) {
      const text = loadFailure(e, t)
      if (alive.current && text !== null) setError(text)
    }
  }
  useEffect(() => {
    alive.current = true
    void load()
    return () => { alive.current = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const run = async (task: () => Promise<unknown>) => {
    setBusy(true)
    try { await task(); await load(); window.dispatchEvent(new Event(INBOX_CHANGED)) } catch (e) { notifyError(errText(e, t)) } finally { if (alive.current) setBusy(false) }
  }
  return (
    <div className="rp-inbox-preview" role="dialog" aria-label={t('inbox.title')}>
      <div className="rp-inbox-preview__head">
        <Typography.Text strong>{t('inbox.title')}</Typography.Text>
        {unread > 0 && (
          <Button type="link" size="small" disabled={busy} onClick={() => run(async () => { const { token } = await notificationInbox.snapshot(); await notificationInbox.readAll(token) })}>
            {t('inbox.readAll')}
          </Button>
        )}
      </div>
      <LoadGate loading={items === null} error={error} onRetry={() => { setError(''); void load() }} minHeight={160}>
        {items && items.length === 0 ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('inbox.empty')} />
        ) : (
          <ul className="rp-inbox-preview__list">
            {items?.map(item => (
              <li key={item.id} className={item.read_at ? 'rp-inbox-preview__item' : 'rp-inbox-preview__item rp-inbox-preview__item--unread'}>
                <span className="rp-inbox-preview__dot" aria-label={item.read_at ? undefined : t('inbox.unread')} role={item.read_at ? undefined : 'img'} />
                <div className="rp-inbox-preview__body">
                  <div className="rp-inbox-preview__subject">{item.subject}</div>
                  {item.text && <div className="rp-inbox-preview__text">{item.text}</div>}
                  <Typography.Text type="secondary" className="rp-inbox-preview__time">{formatTaskTime(item.occurred_at, lang)}</Typography.Text>
                </div>
                {!item.read_at && (
                  <Tooltip title={t('inbox.read')}>
                    <Button type="text" size="small" icon={<CheckOutlined />} aria-label={t('inbox.read')} disabled={busy} onClick={() => run(() => notificationInbox.update(item.id, 'read'))} />
                  </Tooltip>
                )}
              </li>
            ))}
          </ul>
        )}
      </LoadGate>
      <div className="rp-inbox-preview__foot">
        <Link to="/notifications" onClick={onClose}>{t('inbox.viewAll')}</Link>
      </div>
    </div>
  )
}
