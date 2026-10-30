import { Badge, Breadcrumb, theme } from 'antd'
import { AppstoreOutlined, ClockCircleOutlined, LineChartOutlined, PlayCircleOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { appPath, serviceStatus, type AdminApp } from './appsAdminModel'

// The glyphs the apps hub and the header use for each built-in app.
const BUILTIN_GLYPHS: Record<string, React.ReactNode> = {
  batch: <PlayCircleOutlined />,
  recurring: <ClockCircleOutlined />,
  quotes: <LineChartOutlined />,
}

/** The icon the manifest declares for an installed app; a built-in app has its hub glyph, else a generic one. */
export function AppIcon({ app, size = 36 }: { app: AdminApp; size?: number }) {
  const { token } = theme.useToken()
  return (
    <span
      aria-hidden="true"
      style={{
        width: size,
        height: size,
        borderRadius: Math.round(size / 4),
        flex: 'none',
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        fontSize: Math.round(size / 2),
        background: app.builtin ? token.colorPrimaryBg : token.colorFillSecondary,
        color: app.builtin ? token.colorPrimary : token.colorTextSecondary,
      }}
    >
      {app.icon || (app.builtinDef && BUILTIN_GLYPHS[app.builtinDef.key]) || <AppstoreOutlined />}
    </span>
  )
}

/** A status dot and the words for it: everyone, some OUs (k of n), or nobody. */
export function ServiceStatus({ app }: { app: AdminApp }) {
  const { t } = useTranslation()
  const s = serviceStatus(app.access)
  if (s.kind === 'all') return <Badge status="success" text={t('apps.status.all')} />
  if (s.kind === 'none') return <Badge status="default" text={t('apps.status.none')} />
  return <Badge status="warning" text={t('apps.status.some', { open: s.open, total: s.total })} />
}

/** Apps › name › (page). The ancestors are links; the last item is the current page. */
export function AppsBreadcrumb({ app, name, page }: { app?: AdminApp; name?: string; page?: string }) {
  const { t } = useTranslation()
  const items: { title: React.ReactNode }[] = [{ title: app ? <Link to="/manage/apps">{t('apps.title')}</Link> : t('apps.title') }]
  if (app && name) items.push({ title: page ? <Link to={appPath(app.key)}>{name}</Link> : name })
  if (page) items.push({ title: page })
  return <Breadcrumb aria-label={t('apps.breadcrumb')} items={items} style={{ marginBottom: 16 }} />
}
