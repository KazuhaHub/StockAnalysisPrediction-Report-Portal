import { useMemo, useState, type ReactNode } from 'react'
import { Alert, App, Button, Card, Result, Space, Tag, Typography, theme } from 'antd'
import { RightOutlined } from '@ant-design/icons'
import { useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import AppBackendModal from '../../components/AppBackendModal'
import { SCOPE_LABEL } from '../../components/ScopePermissionModal'
import LoadGate from '../../components/LoadGate'
import { clickable } from '../../lib/clickable'
import { appPath, useAppLabels, useAppsCatalog, type AdminApp } from './appsAdminModel'
import { AppIcon, AppsBreadcrumb, ServiceStatus } from './appsAdminParts'

// One section of the detail page: a title, a line saying what it controls, its current value and a
// chevron. With `onOpen` the whole card is the control; without it the card only reports a value.
function Section({
  title,
  hint,
  value,
  onOpen,
  danger,
}: {
  title: string
  hint: string
  value?: ReactNode
  onOpen?: () => void
  danger?: boolean
}) {
  const { token } = theme.useToken()
  return (
    <Card hoverable={!!onOpen} styles={{ body: { padding: '16px 20px' } }} {...(onOpen ? clickable(onOpen) : {})}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
        <div style={{ flex: '1 1 220px', minWidth: 0 }}>
          <Typography.Text strong style={{ fontSize: 15, color: danger ? token.colorError : undefined }}>
            {title}
          </Typography.Text>
          <div style={{ fontSize: 13, color: token.colorTextSecondary }}>{hint}</div>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, color: token.colorTextSecondary, minWidth: 0, flexWrap: 'wrap' }}>
          {value}
          {onOpen && <RightOutlined aria-hidden="true" />}
        </div>
      </div>
    </Card>
  )
}

export default function AppDetailPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const key = useParams().key ?? ''
  const { apps, loading, error, reload } = useAppsCatalog()
  const app = apps.find((a) => a.key === key)
  return (
    <LoadGate loading={loading} error={error} onRetry={reload}>
      {app ? (
        <AppDetail app={app} />
      ) : (
        <Result status="404" title={t('apps.notFound')} extra={<Button onClick={() => navigate('/manage/apps')}>{t('apps.backToList')}</Button>} />
      )}
    </LoadGate>
  )
}

function AppDetail({ app }: { app: AdminApp }) {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const labels = useAppLabels()
  const [backendOpen, setBackendOpen] = useState(false)
  const name = labels.title(app)
  const description = labels.description(app)
  const permLabel = (perm: string) => (perm ? t(`apps.perm.${perm}`) : t('apps.perm.none'))
  // A stable object: the backend dialog resets its form whenever the app it is given changes, and a
  // fresh literal on every render would do that on any unrelated re-render of this page.
  const { key: appKey, icon: appIcon, version: appVersion } = app
  const backendApp = useMemo(
    () => (backendOpen ? { id: appKey, name, icon: appIcon, version: appVersion } : null),
    [backendOpen, appKey, name, appIcon, appVersion],
  )

  const uninstall = () =>
    modal.confirm({
      title: t('apps.section.uninstall.confirmTitle', { name }),
      content: t('apps.section.uninstall.confirmBody'),
      okText: t('apps.section.uninstall.confirmOk'),
      okButtonProps: { danger: true },
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await api.del(`/api/admin/apps/${encodeURIComponent(app.key)}`)
          message.success(t('apps.section.uninstall.done'))
          navigate('/manage/apps')
        } catch (e) {
          message.error(errText(e, t))
        }
      },
    })

  const facts: [string, ReactNode][] = [[t('apps.detail.type'), app.builtin ? t('apps.type.builtin') : t('apps.type.installed')]]
  if (app.version) facts.push([t('apps.version'), app.version])
  if (app.builtinDef) {
    facts.push([t('apps.detail.route'), app.builtinDef.to], [t('apps.detail.permission'), permLabel(app.builtinDef.perm)])
  } else if (app.entry) {
    facts.push([t('apps.detail.entry'), app.entry])
  }

  return (
    <>
      {!app.detailKnown && <Alert type="warning" showIcon title={t('apps.detail.installedFailed')} style={{ marginBottom: 16 }} />}
      <AppsBreadcrumb app={app} name={name} />
      <div className="rp-app-detail">
        <Card styles={{ body: { padding: 20 } }}>
          <div style={{ display: 'flex', gap: 14, alignItems: 'center', marginBottom: 16 }}>
            <AppIcon app={app} size={56} />
            <div style={{ minWidth: 0 }}>
              <Typography.Title level={4} style={{ margin: 0 }}>
                {name}
              </Typography.Title>
              <Typography.Text type="secondary">{app.builtin ? t('apps.type.builtin') : t('apps.type.installed')}</Typography.Text>
            </div>
          </div>
          {description && <Typography.Paragraph type="secondary">{description}</Typography.Paragraph>}
          <dl style={{ display: 'grid', gridTemplateColumns: 'max-content minmax(0, 1fr)', gap: '6px 12px', margin: 0, fontSize: 13 }}>
            {facts.map(([label, value]) => (
              <div key={label} style={{ display: 'contents' }}>
                <dt style={{ color: token.colorTextSecondary }}>{label}</dt>
                <dd style={{ margin: 0, wordBreak: 'break-all' }}>{value}</dd>
              </div>
            ))}
          </dl>
        </Card>
        <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          <Section
            title={t('apps.section.status.title')}
            hint={t('apps.section.status.hint')}
            value={<ServiceStatus app={app} />}
            onOpen={() => navigate(`${appPath(app.key)}/access`)}
          />
          <Section
            title={t('apps.section.scopes.title')}
            hint={app.builtin ? t('apps.section.scopes.hintBuiltin') : t('apps.section.scopes.hint')}
            value={
              app.builtinDef ? (
                <Typography.Text>{permLabel(app.builtinDef.perm)}</Typography.Text>
              ) : !app.detailKnown ? (
                '—'
              ) : app.scopes.length ? (
                <Space size={4} wrap>
                  {app.scopes.map((s) => (
                    <Tag key={s} style={{ margin: 0 }}>
                      {SCOPE_LABEL[s] ? t(SCOPE_LABEL[s]) : s}
                    </Tag>
                  ))}
                </Space>
              ) : (
                t('apps.permNone')
              )
            }
          />
          {!app.builtin && (
            <>
              <Section
                title={t('apps.section.credentials.title')}
                hint={t('apps.section.credentials.hint')}
                value={t('apps.section.credentials.value')}
                onOpen={() => navigate(`${appPath(app.key)}/credentials`)}
              />
              <Section title={t('apps.section.backend.title')} hint={t('apps.section.backend.hint')} value={t('apps.section.backend.value')} onOpen={() => setBackendOpen(true)} />
              <Section title={t('apps.section.uninstall.title')} hint={t('apps.section.uninstall.hint')} danger onOpen={uninstall} />
            </>
          )}
        </Space>
      </div>
      <AppBackendModal app={backendApp} onClose={() => setBackendOpen(false)} />
    </>
  )
}
