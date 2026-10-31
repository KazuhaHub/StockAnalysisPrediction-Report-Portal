import { useEffect, useMemo, useRef, useState } from 'react'
import { Alert, App, Button, Card, ConfigProvider, Drawer, Empty, Grid, Input, InputNumber, Popconfirm, Space, Spin, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { EyeOutlined, ReloadOutlined, StopOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import type { LoadOpts } from '../../lib/loadFailure'
import { formatReportTime } from '../../lib/datetime'
import { startVisiblePoll } from '../../lib/visiblePoll'
import Markdown from '../../components/Markdown'
import LoadGate from '../../components/LoadGate'
import type { ChatTurn } from '../../api/types'
import CompactNumberInput from '../../components/CompactNumberInput'
import SettingsWorkspace, { SettingsPanel } from './SettingsWorkspace'
import SettingsField from './SettingsField'
import SettingsSaveBar from './SettingsSaveBar'

// Assistant admin (docs/adr/0012-interactive-chat.md). Two independent controls:
//  1. A concurrency ceiling on in-flight chat turns — chat is interactive, so it does NOT
//     go through the batch run queue (that queue defers slow report runs; a chat turn can't
//     wait). This is a simple load-shedding cap, separate from the run budget. 0 = unlimited.
//  2. A live view of the turns in progress right now (who / which assistant / since when).
type ChatLiveTurn = {
  id: number
  user: string
  target_id: number
  target: string
  conv_id: number
  title: string
  started_at: string
}
type ChatLive = { turns: ChatLiveTurn[]; max_concurrent: number; stream: boolean; turn_timeout_minutes: number }

// One row in the admin conversation-oversight list; messages are fetched on demand (Dify holds them).
type AdminConv = { id: number; created_by: string; target: string; title: string; updated_at: string; started: boolean }

// elapsed renders a short "12s" / "3m 4s" since an ISO instant.
function elapsed(startedAt: string): string {
  const secs = Math.max(0, Math.floor((Date.now() - new Date(startedAt).getTime()) / 1000))
  if (secs < 60) return `${secs}s`
  return `${Math.floor(secs / 60)}m ${secs % 60}s`
}

export default function ChatAdminPage() {
  const { t } = useTranslation()
  const { catchLoad, retryKey } = useLoadFailure()
  const { message } = App.useApp()
  const [limit, setLimit] = useState(0)
  const [stream, setStream] = useState(true)
  const [turnTimeout, setTurnTimeout] = useState(20)
  const values = { max_concurrent: limit, stream, turn_timeout_minutes: turnTimeout }
  const [saved, setSaved] = useState<typeof values | null>(null)
  const [saving, setSaving] = useState(false)
  const [section, setSection] = useState('settings')
  const dirty = saved !== null && JSON.stringify(values) !== JSON.stringify(saved)
  const [turns, setTurns] = useState<ChatLiveTurn[]>([])
  // The three numbers above are useState defaults, and Save posts all three — so an admin who
  // arrives on a slow link could write "0 concurrent, streaming on, 20 minutes" over the real
  // configuration without ever having seen it. Nothing renders until the answer is in.
  const [liveLoaded, setLiveLoaded] = useState(false)
  const [cfgErr, setCfgErr] = useState('')
  const [convsLoaded, setConvsLoaded] = useState(false)
  const [convsErr, setConvsErr] = useState('')
  const [auto, setAuto] = useState(true)
  const seeded = useRef(false)
  const fullWidth = !Grid.useBreakpoint().md
  const [convs, setConvs] = useState<AdminConv[]>([])
  const [convSearch, setConvSearch] = useState('')
  const [viewConv, setViewConv] = useState<AdminConv | null>(null)
  const [viewTurns, setViewTurns] = useState<ChatTurn[]>([])
  const [viewLoading, setViewLoading] = useState(false)

  const load = (opts?: LoadOpts) =>
    api
      .get<ChatLive>('/api/admin/chat/live')
      .then((r) => {
        setTurns(r.turns || [])
        setLiveLoaded(true)
        setCfgErr('')
        // Seed the ceiling input once, so a running poll never clobbers an in-progress edit.
        if (!seeded.current) {
          setLimit(r.max_concurrent ?? 0)
          setStream(r.stream !== false)
          setTurnTimeout(r.turn_timeout_minutes ?? 20)
          setSaved({ max_concurrent: r.max_concurrent ?? 0, stream: r.stream !== false, turn_timeout_minutes: r.turn_timeout_minutes ?? 20 })
          seeded.current = true
        }
      })
      // Only the first poll has anything to report; after that the numbers on screen are the
      // server's, and a dropped poll must not replace them.
      .catch(catchLoad(setCfgErr, opts))
  const loadConvs = () => {
    setConvsErr('')
    return api
      .get<{ conversations: AdminConv[] }>('/api/admin/chat/conversations')
      .then((r) => {
        setConvs(r.conversations || [])
        setConvsLoaded(true)
      })
      .catch(catchLoad(setConvsErr))
  }
  useEffect(() => {
    loadConvs()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [retryKey])
  useEffect(() => {
    if (!auto) return
    return startVisiblePoll(load, 3000)
  }, [auto])

  const save = async () => {
    setSaving(true)
    try {
      await api.post('/api/admin/chat/config', values)
      setSaved(values)
      message.success(t('common.saved'))
    } catch (e) { message.error(errText(e, t)) } finally { setSaving(false) }
  }

  // Open one user's conversation read-only: pull its messages from Dify (keyed by the owner, so the
  // admin sees exactly what that user saw). A conversation with no first turn yet has nothing to show.
  const openConv = (c: AdminConv) => {
    setViewConv(c)
    setViewTurns([])
    if (!c.started) return
    setViewLoading(true)
    api
      .get<{ turns: ChatTurn[] }>(`/api/admin/chat/conversations/${c.id}/messages`)
      .then((r) => setViewTurns(r.turns || []))
      .catch((e) => message.error(errText(e, t)))
      .finally(() => setViewLoading(false))
  }
  const filteredConvs = useMemo(() => {
    const q = convSearch.trim().toLowerCase()
    return q ? convs.filter((c) => `${c.created_by} ${c.title} ${c.target}`.toLowerCase().includes(q)) : convs
  }, [convs, convSearch])

  // Stop any in-flight turn from the live view: cancels the Dify stream + best-effort stops the
  // run server-side (runs as the turn's own end-user, not the admin).
  const stop = async (id: number) => {
    try {
      await api.post(`/api/admin/chat/stop/${id}`)
      message.success(t('chatAdmin.stopped'))
      load({ quiet: true })
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const columns = [
    { title: t('chatAdmin.colUser'), dataIndex: 'user', key: 'user' },
    { title: t('chatAdmin.colTarget'), dataIndex: 'target', key: 'target' },
    {
      title: t('chatAdmin.colConversation'),
      dataIndex: 'title',
      key: 'title',
      render: (title: string) => title || <Typography.Text type="secondary">{t('chat.untitled')}</Typography.Text>,
    },
    {
      title: t('chatAdmin.colStarted'),
      dataIndex: 'started_at',
      key: 'started_at',
      render: (v: string) => (
        <Tooltip title={formatReportTime(v, true)}>
          <span>{elapsed(v)}</span>
        </Tooltip>
      ),
    },
    {
      title: '',
      key: 'actions',
      width: 48,
      render: (_: unknown, r: ChatLiveTurn) => (
        <Popconfirm title={t('chatAdmin.stopConfirm')} onConfirm={() => stop(r.id)} okButtonProps={{ danger: true }}>
          <Button size="small" type="text" danger icon={<StopOutlined />} title={t('chatAdmin.stop')} />
        </Popconfirm>
      ),
    },
  ]

  return (
    <>
    <SettingsWorkspace active={section} onChange={setSection} sections={[
      { key: 'settings', label: t('chatAdmin.limitCard') }, { key: 'live', label: t('chatAdmin.liveCard') },
      { key: 'history', label: t('chatAdmin.convsCard') },
    ]}>
      <SettingsPanel section="settings">
      <Card title={t('chatAdmin.limitCard')}>
        <LoadGate loading={!liveLoaded && !cfgErr} error={liveLoaded ? undefined : cfgErr} onRetry={load} minHeight={180}>
        <ConfigProvider componentDisabled={saving}>
          <SettingsField label={t('chatAdmin.limit')} hint={t('chatAdmin.limitHint')}>
            <InputNumber min={0} max={100} value={limit} onChange={(v) => setLimit(v ?? 0)} />
          </SettingsField>
          <SettingsField label={t('chatAdmin.stream')} hint={t('chatAdmin.streamHint')}>
            <Switch checked={stream} onChange={setStream} />
          </SettingsField>
          <SettingsField label={t('chatAdmin.turnTimeout')} hint={t('chatAdmin.turnTimeoutHint')}>
            <CompactNumberInput min={1} max={120} value={turnTimeout} onChange={(v) => setTurnTimeout(v ?? 20)} after={t('chatAdmin.minutes')} />
          </SettingsField>
        </ConfigProvider>
        <SettingsSaveBar dirty={dirty} saving={saving} onSave={save} onDiscard={() => {
          if (saved) { setLimit(saved.max_concurrent); setStream(saved.stream); setTurnTimeout(saved.turn_timeout_minutes) }
        }} />
        </LoadGate>
      </Card>
      </SettingsPanel>
      <SettingsPanel section="live">
      <Card
        title={
          <Space wrap>
            {t('chatAdmin.liveCard')}
            {/* No count until the first poll: "0 running" is a claim, and this tag is the only
                place on the page that makes it in one glance. */}
            {liveLoaded && <Tag color={turns.length ? 'processing' : 'default'}>{t('chatAdmin.liveCount', { n: turns.length })}</Tag>}
          </Space>
        }
        extra={
          <Space>
            <Switch size="small" checked={auto} onChange={setAuto} />
            <Typography.Text type="secondary">{t('queue.autoRefresh')}</Typography.Text>
            <Button size="small" icon={<ReloadOutlined />} onClick={() => void load()}>
              {t('nav.refresh')}
            </Button>
          </Space>
        }
      >
        {cfgErr && <Alert type="error" showIcon title={cfgErr} style={{ marginBottom: 12 }} />}
        <Table
          size="small"
          rowKey="id"
          columns={columns}
          dataSource={turns}
          pagination={false}
          scroll={{ x: 560 }}
          loading={!liveLoaded && !cfgErr}
          locale={{
            emptyText: (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={liveLoaded || !cfgErr ? t('chatAdmin.none') : t('common.loadFailedContent')} />
            ),
          }}
        />
      </Card>
      </SettingsPanel>
      <SettingsPanel section="history">
      <Card
        title={t('chatAdmin.convsCard')}
        extra={
          <Space wrap>
            <Input.Search allowClear placeholder={t('chatAdmin.searchConv')} value={convSearch} onChange={(e) => setConvSearch(e.target.value)} style={{ width: 200 }} />
            <Button size="small" icon={<ReloadOutlined />} onClick={loadConvs}>
              {t('nav.refresh')}
            </Button>
          </Space>
        }
      >
        <Typography.Text type="secondary" style={{ display: 'block', marginBottom: 12 }}>
          {t('chatAdmin.convsHint')}
        </Typography.Text>
        <Table
          size="small"
          rowKey="id"
          dataSource={filteredConvs}
          pagination={{ pageSize: 15, size: 'small' }}
          scroll={{ x: 560 }}
          loading={!convsLoaded && !convsErr}
          locale={{
            emptyText: convsErr ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('common.loadFailedContent')} />
            ) : (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('chatAdmin.convEmpty')} />
            ),
          }}
          columns={[
            { title: t('chatAdmin.colUser'), dataIndex: 'created_by', width: 140 },
            { title: t('chatAdmin.colTarget'), dataIndex: 'target', width: 150 },
            {
              title: t('chatAdmin.colConversation'),
              dataIndex: 'title',
              render: (title: string, c: AdminConv) =>
                title || <Typography.Text type="secondary">{c.started ? t('chat.untitled') : t('chatAdmin.notStarted')}</Typography.Text>,
            },
            {
              title: t('chatAdmin.colUpdated'),
              dataIndex: 'updated_at',
              width: 168,
              render: (v: string) => <span style={{ fontSize: 12 }}>{formatReportTime(v, true)}</span>,
            },
            {
              title: '',
              key: 'view',
              width: 48,
              render: (_: unknown, c: AdminConv) => (
                <Button size="small" type="text" icon={<EyeOutlined />} onClick={() => openConv(c)} title={t('chatAdmin.view')} />
              ),
            },
          ]}
        />
      </Card>
      </SettingsPanel>
    </SettingsWorkspace>
      <Drawer
        open={viewConv != null}
        onClose={() => setViewConv(null)}
        size={fullWidth ? '100%' : 640}
        destroyOnHidden
        title={
          viewConv ? (
            <Space size={8} wrap>
              <Tag>{viewConv.created_by}</Tag>
              <Typography.Text>{viewConv.title || t('chat.untitled')}</Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {viewConv.target}
              </Typography.Text>
            </Space>
          ) : (
            ''
          )
        }
      >
        {viewLoading ? (
          <div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}>
            <Spin />
          </div>
        ) : viewConv && !viewConv.started ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('chatAdmin.notStarted')} />
        ) : viewTurns.length === 0 ? (
          <Empty description={t('chatAdmin.convEmpty')} />
        ) : (
          <Space orientation="vertical" size={16} style={{ width: '100%' }}>
            {viewTurns.map((turn, i) => (
              <div key={i}>
                {turn.query && (
                  <div
                    style={{
                      marginBottom: 8,
                      padding: '8px 12px',
                      borderRadius: 'var(--ant-border-radius)',
                      whiteSpace: 'pre-wrap',
                      background: 'var(--ant-color-fill-secondary)',
                    }}
                  >
                    {turn.query}
                  </div>
                )}
                {turn.answer && <Markdown md={turn.answer} />}
              </div>
            ))}
          </Space>
        )}
      </Drawer>
    </>
  )
}
