import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Button, Card, Col, Empty, Row, Space, Spin, Tag, Typography, theme } from 'antd'
import { AppstoreOutlined, ClockCircleOutlined, PlayCircleOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import { usePrefs } from '../prefs'
import { AppDisplayIcon, appDisplayText } from '../lib/appDisplayV2'
import { useAuth } from '../auth'
import { BUILTIN_APPS } from '../lib/builtinApps'
import type { AppsResp, AppSummary } from '../api/types'
import { clickable } from '../lib/clickable'
import { useAppDirectory } from '../lib/appDirectoryV2'

// The apps hub: a grid of user-facing apps. Built-in apps are compiled-in cards (each gated by a
// permission; the registry lives in lib/builtinApps so the entry-button picker and shortcut router
// agree with it); installed iframe apps (ADR 0003) are downloaded at runtime and appear here for
// every user. Empty by default — the "app center" fills as you install apps.
const BUILTIN_ICONS: Record<string, ReactNode> = {
  batch: <PlayCircleOutlined />,
  recurring: <ClockCircleOutlined />,
}

function AppCard({ icon, title, desc, tag, onClick, disabled = false }: { icon: ReactNode; title: string; desc?: string; tag?: string; onClick: () => void; disabled?: boolean }) {
  const { token } = theme.useToken()
  return (
    <Col xs={24} sm={12} lg={8}>
      <Card hoverable={!disabled} {...(disabled ? { role: 'button', tabIndex: 0, 'aria-disabled': true, 'aria-label': title } : clickable(onClick, title))} style={{ height: '100%', opacity: disabled ? 0.65 : 1 }}>
        <Space align="start" size={16}>
          <span style={{ fontSize: 28, color: token.colorPrimary, lineHeight: 1 }}>{icon}</span>
          <div>
            <Space size={8} align="center">
              <Typography.Text strong style={{ fontSize: 16 }}>
                {title}
              </Typography.Text>
              {tag && <Tag color="blue">{tag}</Tag>}
            </Space>
            {desc && (
              <Typography.Paragraph type="secondary" style={{ margin: '4px 0 0' }}>
                {desc}
              </Typography.Paragraph>
            )}
          </div>
        </Space>
      </Card>
    </Col>
  )
}

export default function AppsHub() {
  const { t } = useTranslation()
  const { can, user } = useAuth()
  const { lang } = usePrefs()
  const directory = useAppDirectory(user)
  const navigate = useNavigate()
  const [apps, setApps] = useState<AppSummary[]>([])
  // An account without the built-in apps' permission sees nothing but the installed list, so
  // "No apps available" before /api/apps answers is the whole page making a claim for the server
  // — and the old `.catch(() => setApps([]))` made a failed request wear that same face for good.
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return api
      .get<AppsResp>('/api/apps')
      .then((r) => setApps(r.apps || []))
      .catch((e) => setLoadErr(errText(e, t)))
      .finally(() => setLoading(false))
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // '' perm = everyone (matches shortcutPerm's registry contract; can('') is false, so guard on it).
  const builtins = BUILTIN_APPS.filter((a) => !a.perm || can(a.perm))
  const isEmpty = builtins.length === 0 && apps.length === 0 && directory.items.length === 0

  return (
    <Space orientation="vertical" size={16} style={{ width: '100%' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>
        {t('nav.apps')}
      </Typography.Title>
      {/* The built-in cards are compiled in and owe /api/apps nothing, so they are never hidden
          behind its answer — a failed installed-apps call says so above them and leaves them
          usable. Only the "no apps available" claim waits, since only the server can make it. */}
      {loadErr && (
        <Alert
          type="warning"
          showIcon
          title={t('common.loadFailedContent')}
          description={loadErr}
          action={<Button size="small" onClick={load}>{t('common.retry')}</Button>}
        />
      )}
      {directory.failed && (
        <Alert type="warning" showIcon title={t('apps.v2ListUnavailable')}
          action={<Button size="small" onClick={() => directory.load()}>{t('common.retry')}</Button>} />
      )}
      {(loading || directory.loading) && isEmpty ? (
        <div style={{ display: 'grid', justifyItems: 'center', gap: 12, minHeight: 200, alignContent: 'center' }}>
          <Spin size="large" />
          <Typography.Text type="secondary">{t('common.loading')}</Typography.Text>
        </div>
      ) : isEmpty && !loading && !directory.loading && !loadErr && !directory.failed ? (
        <Empty description={t('apps.empty')} />
      ) : (
        <Row gutter={[16, 16]}>
          {builtins.map((a) => (
            <AppCard
              key={a.key}
              icon={BUILTIN_ICONS[a.key] ?? <AppstoreOutlined />}
              title={t(a.titleKey)}
              desc={t(a.descKey)}
              tag={t('apps.builtin')}
              onClick={() => navigate(a.to)}
            />
          ))}
          {directory.items.map(a => {
            const copy = appDisplayText(a.applicationId, a.display, lang)
            return (
            <AppCard key={`v2:${a.installationId}`} icon={<AppDisplayIcon display={a.display} />} title={copy.name} desc={copy.description}
              tag={t(`apps.v2State.${a.state}`)} disabled={!a.openable}
              onClick={() => navigate(`/apps/v2/${a.installationId}`)} />
            )
          })}
          {apps.map((a) => (
            <AppCard
              key={a.id}
              icon={a.icon ? <span style={{ fontSize: 28, lineHeight: 1 }}>{a.icon}</span> : <AppstoreOutlined />}
              title={a.name}
              desc={a.version ? `v${a.version}` : undefined}
              onClick={() => navigate(`/apps/x/${a.id}`)}
            />
          ))}
        </Row>
      )}
      {directory.nextCursor && !directory.failed && (
        <Button onClick={() => directory.load(directory.nextCursor)} loading={directory.loading}>{t('apps.v2LoadMore')}</Button>
      )}
    </Space>
  )
}
