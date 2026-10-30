import { useRef, useState } from 'react'
import { App, Button, Drawer, Dropdown, Empty, Input, Space, Table, Tag, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { CloudDownloadOutlined, DownOutlined, PlusOutlined, ShopOutlined, UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { AppMarketEntry, AppMarketResp, AppPreviewResp } from '../../api/types'
import ScopePermissionModal from '../../components/ScopePermissionModal'

// A pending install awaiting the admin's permission confirmation. `run` performs the
// actual install once the scopes are approved.
interface PendingInstall {
  appName?: string
  scopes: string[]
  run: () => Promise<void>
}

// Post a "bundle" multipart form to the install endpoint. The api client only speaks JSON, so use a
// raw fetch (the same-origin session cookie carries auth). preview=1 parses without persisting so the
// permission prompt can be shown first.
async function postBundle(file: File, preview: boolean) {
  const fd = new FormData()
  fd.append('bundle', file)
  const url = preview ? '/api/admin/apps/install?preview=1' : '/api/admin/apps/install'
  const res = await fetch(url, { method: 'POST', body: fd, credentials: 'same-origin' })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || String(res.status))
  return data as AppPreviewResp
}

const scopeTags = (s?: string[]) => (s && s.length ? s.map((x) => <Tag key={x}>{x}</Tag>) : <Tag>—</Tag>)

// The two ways to add an app, behind one button: upload a package, or install from the app market
// (a GitHub-hosted index, ADR 0003 phase 2). Both go through the same permission review before anything
// is installed; `onInstalled` lets the page reload its list.
export default function AppInstall({ onInstalled }: { onInstalled: () => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const fileInput = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)
  const [pending, setPending] = useState<PendingInstall | null>(null)
  const [confirming, setConfirming] = useState(false)

  const [marketOpen, setMarketOpen] = useState(false)
  const [market, setMarket] = useState<AppMarketEntry[] | null>(null)
  const [marketLoading, setMarketLoading] = useState(false)
  // Where the market reads its index. Blank means the built-in one.
  const [indexUrl, setIndexUrl] = useState('')
  const [savingIndex, setSavingIndex] = useState(false)

  const fetchMarket = async () => {
    setMarketLoading(true)
    try {
      const r = await api.get<AppMarketResp>('/api/admin/apps/market')
      setMarket(r.apps || [])
      setIndexUrl(r.index_url || '')
    } catch (e) {
      message.error(t('apps.marketError', { error: e instanceof Error ? e.message : String(e) }))
    } finally {
      setMarketLoading(false)
    }
  }

  // Manual upload: preview the bundle to learn its scopes, then queue a confirmed install.
  const upload = async (file: File) => {
    setUploading(true)
    try {
      const { app } = await postBundle(file, true)
      setPending({
        appName: app.name,
        scopes: app.scopes || [],
        run: async () => {
          await postBundle(file, false)
          message.success(t('apps.msgInstalled'))
          onInstalled()
          if (market) void fetchMarket()
        },
      })
    } catch (e) {
      message.error(t('apps.msgInstallFailed', { error: e instanceof Error ? e.message : String(e) }))
    } finally {
      setUploading(false)
    }
  }

  // Bind the confirmation to these declarations; the server also checks the ZIP.
  const marketInstall = (entry: AppMarketEntry) => {
    const reviewed = { id: entry.id, version: entry.version, scopes: [...(entry.scopes || [])] }
    setPending({
      appName: entry.name,
      scopes: entry.scopes || [],
      run: async () => {
        await api.post('/api/admin/apps/market/install', reviewed)
        message.success(t('apps.msgInstalled'))
        onInstalled()
        void fetchMarket()
      },
    })
  }

  const confirmInstall = async () => {
    if (!pending) return
    setConfirming(true)
    try {
      await pending.run()
      setPending(null)
    } catch (e) {
      message.error(t('apps.msgInstallFailed', { error: e instanceof Error ? e.message : String(e) }))
    } finally {
      setConfirming(false)
    }
  }

  const marketCols: ColumnsType<AppMarketEntry> = [
    {
      title: t('common.name'),
      render: (_: unknown, e: AppMarketEntry) => (
        <Space orientation="vertical" size={0}>
          <Space>
            {e.icon && <span>{e.icon}</span>}
            <span>{e.name}</span>
            {e.version && <Typography.Text type="secondary">v{e.version}</Typography.Text>}
          </Space>
          {e.description && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {e.description}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    { title: t('apps.scopes'), dataIndex: 'scopes', width: 160, render: scopeTags },
    {
      title: t('batch.col.actions'),
      width: 120,
      render: (_: unknown, e: AppMarketEntry) =>
        e.installed ? (
          <Tag color="green">{t('apps.installedTag')}</Tag>
        ) : (
          <Button size="small" type="primary" icon={<CloudDownloadOutlined />} onClick={() => marketInstall(e)}>
            {t('apps.marketInstall')}
          </Button>
        ),
    },
  ]

  return (
    <>
      <Dropdown
        trigger={['click']}
        menu={{
          items: [
            { key: 'upload', icon: <UploadOutlined aria-hidden="true" />, label: t('apps.upload'), disabled: uploading },
            { key: 'market', icon: <ShopOutlined aria-hidden="true" />, label: t('apps.fromMarket') },
          ],
          onClick: ({ key }) => {
            if (key === 'upload') fileInput.current?.click()
            else setMarketOpen(true)
          },
        }}
      >
        <Button type="primary" icon={<PlusOutlined aria-hidden="true" />} loading={uploading}>
          {t('apps.install')}
          <DownOutlined aria-hidden="true" />
        </Button>
      </Dropdown>
      {/* Outside the menu on purpose: the menu unmounts when it closes, and the file picker needs its input to outlive that. */}
      <input
        ref={fileInput}
        type="file"
        accept=".zip"
        hidden
        data-testid="apps-upload-input"
        aria-label={t('apps.upload')}
        onChange={(e) => {
          const file = e.target.files?.[0]
          e.target.value = ''
          if (file) void upload(file)
        }}
      />

      <Drawer open={marketOpen} onClose={() => setMarketOpen(false)} title={t('apps.market')} size={720}>
        <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('apps.marketHint')}</Typography.Text>
          <Space.Compact style={{ width: '100%' }}>
            <Input value={indexUrl} onChange={(e) => setIndexUrl(e.target.value)} placeholder={t('apps.marketIndexHint')} aria-label={t('apps.marketIndex')} />
            <Button
              loading={savingIndex}
              onClick={() => {
                setSavingIndex(true)
                api
                  .post<{ index_url: string }>('/api/admin/apps/market/index', { index_url: indexUrl })
                  .then((r) => {
                    setIndexUrl(r.index_url || '')
                    message.success(t('common.saved'))
                    if (market) void fetchMarket()
                  })
                  .catch((e) => message.error(errText(e, t)))
                  .finally(() => setSavingIndex(false))
              }}
            >
              {t('common.save')}
            </Button>
          </Space.Compact>
          <div>
            <Button icon={<CloudDownloadOutlined />} loading={marketLoading} onClick={fetchMarket}>
              {t('apps.marketFetch')}
            </Button>
          </div>
          {market === null ? null : market.length === 0 ? (
            <Empty description={t('apps.marketEmpty')} />
          ) : (
            <Table rowKey="id" size="small" dataSource={market} columns={marketCols} pagination={{ pageSize: 15, hideOnSinglePage: true, showSizeChanger: false }} scroll={{ x: 'max-content' }} />
          )}
        </Space>
      </Drawer>

      <ScopePermissionModal
        open={!!pending}
        appName={pending?.appName}
        scopes={pending?.scopes || []}
        confirmLoading={confirming}
        onConfirm={confirmInstall}
        onCancel={() => setPending(null)}
      />
    </>
  )
}
