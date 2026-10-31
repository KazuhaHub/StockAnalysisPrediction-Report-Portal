import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Button, Card, Col, Empty, Row, Space, Spin, Tag, Typography, theme } from 'antd'
import { AppstoreOutlined, ClockCircleOutlined, PlayCircleOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import { usePrefs } from '../prefs'
import { AppDisplayIcon, appDisplayText } from '../lib/appDisplayV2'
import { useAuth } from '../auth'
import { BUILTIN_APPS, BUILTIN_PIN_PREFIX, type BuiltinApp } from '../lib/builtinApps'
import type { AppsResp, AppSummary } from '../api/types'
import { clickable } from '../lib/clickable'
import { useAppDirectory } from '../lib/appDirectoryV2'
import { useLoadFailure } from '../lib/useOnReconnect'
import { settleOnAnswer } from '../lib/loadFailure'

// The apps hub: a grid of user-facing apps. Built-in apps are compiled-in cards (each gated by a
// permission; the registry lives in lib/builtinApps so the entry-button picker and shortcut router
// agree with it); installed iframe apps (ADR 0003) are downloaded at runtime and appear here for
// every user the administrator has not closed them for. What is shown, and in which order, is the
// server's answer (ADR 0048). Empty by default — the "app center" fills as you install apps.
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

type HubEntry = { kind: 'builtin'; app: BuiltinApp } | { kind: 'installed'; app: AppSummary }

const registryKeys = () => BUILTIN_APPS.map((a) => BUILTIN_PIN_PREFIX + a.key)

export default function AppsHub() {
  const { t } = useTranslation()
  const { failure, catchLoad, retryKey } = useLoadFailure()
  const { can, user } = useAuth()
  const { lang } = usePrefs()
  const directory = useAppDirectory(user)
  const navigate = useNavigate()
  const [resp, setResp] = useState<AppsResp | null>(null)
  // A failed request must not wear the face of "no apps available": the page would be making a claim
  // for the server. It says so instead, and falls back to the compiled-in cards, which owe the list
  // nothing but their own permission.
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return api
      .get<AppsResp>('/api/apps')
      .then((r) => setResp(r))
      .then(...settleOnAnswer(() => setLoading(false)))
      .catch(catchLoad(setLoadErr))
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [retryKey])

  // One sequence for both kinds. An older server sends no `order`: the compiled-in apps first, then
  // the installed ones, which is how the hub always read. '' perm = everyone (matches shortcutPerm's
  // registry contract; can('') is false, so guard on it).
  const installed = resp?.apps ?? []
  const keys = resp ? (resp.order ?? [...registryKeys(), ...installed.map((a) => a.id)]) : registryKeys()
  const entries: HubEntry[] = []
  for (const key of keys) {
    if (key.startsWith(BUILTIN_PIN_PREFIX)) {
      const app = BUILTIN_APPS.find((a) => a.key === key.slice(BUILTIN_PIN_PREFIX.length))
      if (app && (!app.perm || can(app.perm))) entries.push({ kind: 'builtin', app })
    } else {
      const app = installed.find((a) => a.id === key)
      if (app) entries.push({ kind: 'installed', app })
    }
  }
  const isEmpty = entries.length === 0 && directory.items.length === 0

  return (
    <Space orientation="vertical" size={16} style={{ width: '100%' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>
        {t('nav.apps')}
      </Typography.Title>
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
          {entries.map((e) =>
            e.kind === 'builtin' ? (
              <AppCard
                key={BUILTIN_PIN_PREFIX + e.app.key}
                icon={BUILTIN_ICONS[e.app.key] ?? <AppstoreOutlined />}
                title={t(e.app.titleKey)}
                desc={t(e.app.descKey)}
                tag={t('apps.builtin')}
                onClick={() => navigate(e.app.to)}
              />
            ) : (
              <AppCard
                key={e.app.id}
                icon={e.app.icon ? <span style={{ fontSize: 28, lineHeight: 1 }}>{e.app.icon}</span> : <AppstoreOutlined />}
                title={e.app.name}
                desc={e.app.version ? `v${e.app.version}` : undefined}
                onClick={() => navigate(`/apps/x/${e.app.id}`)}
              />
            ),
          )}
          {directory.items.map((a) => {
            const copy = appDisplayText(a.applicationId, a.display, lang)
            return (
            <AppCard key={`v2:${a.installationId}`} icon={<AppDisplayIcon display={a.display} />} title={copy.name} desc={copy.description}
              tag={t(`apps.v2State.${a.state}`)} disabled={!a.openable}
              onClick={() => navigate(`/apps/v2/${a.installationId}`)} />
            )
          })}
        </Row>
      )}
      {directory.nextCursor && !directory.failed && (
        <Button onClick={() => directory.load(directory.nextCursor)} loading={directory.loading}>{t('apps.v2LoadMore')}</Button>
      )}
    </Space>
  )
}
