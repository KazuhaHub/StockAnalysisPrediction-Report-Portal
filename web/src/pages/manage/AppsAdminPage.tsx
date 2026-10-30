import { useMemo, useState } from 'react'
import { Button, Card, Input, Space, Table, Typography, theme } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { OrderedListOutlined, RightOutlined } from '@ant-design/icons'
import { Link, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import LoadGate from '../../components/LoadGate'
import AppInstall from './AppInstall'
import AppReorderModal from './AppReorderModal'
import { appPath, useAppLabels, useAppsCatalog, type AdminApp } from './appsAdminModel'
import { AppIcon, ServiceStatus } from './appsAdminParts'

// The Apps list: every built-in and installed app with where it is switched on, in the administrator's
// order (ADR 0048). Opening one leads to its detail, and from there to its service status by OU. Order
// and installation are occasional jobs, so they live behind the two buttons above the table.
export default function AppsAdminPage() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const labels = useAppLabels()
  const { apps, loading, error, reload } = useAppsCatalog()
  const [search, setSearch] = useState('')
  const [reordering, setReordering] = useState(false)

  // The muted line under a name: what a built-in app is for, or which build of an installed app this is.
  const subtitle = (a: AdminApp) => (a.builtin ? labels.description(a) : [a.version && `v${a.version}`, a.key].filter(Boolean).join(' · '))
  const shown = useMemo(() => {
    const q = search.trim().toLocaleLowerCase()
    if (!q) return apps
    return apps.filter((a) => `${labels.title(a)} ${a.key} ${subtitle(a)}`.toLocaleLowerCase().includes(q))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apps, search, t])

  const columns: ColumnsType<AdminApp> = [
    {
      title: t('apps.col.app'),
      render: (_: unknown, a: AdminApp) => (
        <Space size={12}>
          <AppIcon app={a} />
          <div style={{ minWidth: 0 }}>
            {/* A real link: it is what a keyboard reaches, while the whole row is the mouse target. */}
            <Link to={appPath(a.key)} onClick={(e) => e.stopPropagation()}>
              {labels.title(a)}
            </Link>
            <div style={{ fontSize: 12, color: token.colorTextSecondary }}>{subtitle(a)}</div>
          </div>
        </Space>
      ),
    },
    { title: t('apps.col.status'), width: 260, render: (_: unknown, a: AdminApp) => <ServiceStatus app={a} /> },
    {
      title: t('apps.col.type'),
      width: 120,
      className: 'rp-apps-col-type',
      render: (_: unknown, a: AdminApp) => (a.builtin ? t('apps.type.builtin') : t('apps.type.installed')),
    },
    { title: '', width: 40, align: 'right', render: () => <RightOutlined aria-hidden="true" style={{ color: token.colorTextTertiary }} /> },
  ]

  return (
    <LoadGate loading={loading} error={error} onRetry={reload}>
      <Space orientation="vertical" size={16} style={{ width: '100%' }}>
        <Space wrap style={{ width: '100%', justifyContent: 'flex-end' }}>
          <Button icon={<OrderedListOutlined aria-hidden="true" />} onClick={() => setReordering(true)} disabled={apps.length < 2}>
            {t('apps.reorder')}
          </Button>
          <AppInstall onInstalled={reload} />
        </Space>
        <Card styles={{ body: { padding: 0 } }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 16px', borderBottom: `1px solid ${token.colorBorderSecondary}` }}>
            <Input.Search
              allowClear
              style={{ maxWidth: 320 }}
              aria-label={t('apps.search')}
              placeholder={t('apps.search')}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <Typography.Text type="secondary" style={{ marginInlineStart: 'auto' }}>
              {t('apps.count', { count: shown.length })}
            </Typography.Text>
          </div>
          <Table
            rowKey="key"
            dataSource={shown}
            columns={columns}
            pagination={false}
            locale={{ emptyText: t('apps.empty') }}
            onRow={(a) => ({ onClick: () => navigate(appPath(a.key)), style: { cursor: 'pointer' } })}
          />
        </Card>
      </Space>
      <AppReorderModal open={reordering} apps={apps} onClose={() => setReordering(false)} onSaved={reload} />
    </LoadGate>
  )
}
