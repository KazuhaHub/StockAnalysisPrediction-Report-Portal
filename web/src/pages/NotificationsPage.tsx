import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Empty, Modal, Space, Switch, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { notificationInbox, type InboxItem } from '../api/notifications'
import { errText } from '../api/client'
import LoadGate from '../components/LoadGate'
import { INBOX_CHANGED } from '../components/NotificationBell'
import { startVisiblePoll } from '../lib/visiblePoll'
import { loadFailure } from '../lib/loadFailure'

export default function NotificationsPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [items, setItems] = useState<InboxItem[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState('')
  const [next, setNext] = useState('')
  const [busy, setBusy] = useState(false)
  const [preferences, setPreferences] = useState<Record<string, boolean>>()
  const [snapshot, setSnapshot] = useState<{ token: string; count: number }>()
  const load = async (more = false) => {
    try {
      const result = await notificationInbox.list(more ? next : undefined)
      setItems(old => more ? [...old, ...result.items] : result.items)
      setNext(result.next_cursor)
      setLoaded(true)
      setError('')
    } catch (e) { const text = loadFailure(e, t); if (text !== null) setError(text) }
  }
  useEffect(() => {
    let alive = true
    const stop = startVisiblePoll(async () => {
      try { const result = await notificationInbox.list(); if (alive) { setItems(result.items); setNext(result.next_cursor); setLoaded(true); setError('') } } catch (e) { const text = loadFailure(e, t); if (alive && text !== null) setError(text) }
    }, 30000)
    return () => { alive = false; stop() }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const run = async (task: () => Promise<unknown>, refresh = true) => {
    setBusy(true)
    try { await task(); if (refresh) { await load(); window.dispatchEvent(new Event(INBOX_CHANGED)) } } catch (e) { message.error(errText(e, t)) } finally { setBusy(false) }
  }
  return <LoadGate loading={!loaded} error={error} onRetry={() => load()}>
    <Space orientation="vertical" size="large" style={{ width: '100%', maxWidth: 1000, marginInline: 'auto' }}>
      <Space wrap><Typography.Title level={3} style={{ margin: 0 }}>{t('inbox.title')}</Typography.Title><Button disabled={busy} onClick={() => run(() => load(), false)}>{t('common.refresh')}</Button><Button disabled={busy} onClick={() => run(async () => setSnapshot(await notificationInbox.snapshot()), false)}>{t('inbox.readAll')}</Button><Button disabled={busy} onClick={() => run(async () => setPreferences((await notificationInbox.preferences()).preferences), false)}>{t('inbox.preferences')}</Button></Space>
      {!items.length && <Empty description={t('inbox.empty')} />}
      {items.map(item => <Card key={item.id} styles={{ header: { paddingBlock: 12 }, title: { whiteSpace: 'normal', overflowWrap: 'anywhere' } }} title={<div>{!item.read_at && <Tag color="blue">{t('inbox.unread')}</Tag>}{item.subject}</div>}>
        <Typography.Paragraph type="secondary">{item.occurred_at}</Typography.Paragraph>
        <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{item.text}</Typography.Paragraph>
        <Space>{!item.read_at && <Button disabled={busy} onClick={() => run(() => notificationInbox.update(item.id, 'read'))}>{t('inbox.read')}</Button>}<Button disabled={busy} onClick={() => run(() => notificationInbox.update(item.id, 'dismiss'))}>{t('inbox.dismiss')}</Button></Space>
      </Card>)}
      {next && <Button disabled={busy} onClick={() => run(() => load(true), false)}>{t('notify.more')}</Button>}
    </Space>
    <Modal open={!!snapshot} title={t('inbox.readAll')} onCancel={() => setSnapshot(undefined)} confirmLoading={busy} onOk={() => snapshot && run(async () => { await notificationInbox.readAll(snapshot.token); setSnapshot(undefined) })}>
      <Typography.Paragraph>{t('inbox.snapshotHelp', { count: snapshot?.count })}</Typography.Paragraph>
    </Modal>
    <Modal open={!!preferences} title={t('inbox.preferences')} onCancel={() => setPreferences(undefined)} confirmLoading={busy} onOk={() => preferences && run(async () => { await notificationInbox.savePreferences(preferences); setPreferences(undefined) })}>
      <Alert type="info" title={t('inbox.preferenceHelp')} />
      <Space orientation="vertical" style={{ width: '100%', marginTop: 16 }}>{Object.entries(preferences || {}).map(([kind, enabled]) => <Space key={kind}><Switch checked={enabled} aria-label={t(`webhook.event.${kind}`, kind)} onChange={value => setPreferences(old => ({ ...old, [kind]: value }))} /><span>{t(`webhook.event.${kind}`, kind)}</span></Space>)}</Space>
    </Modal>
  </LoadGate>
}
