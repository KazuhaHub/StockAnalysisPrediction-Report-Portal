import { useState } from 'react'
import { Alert, Badge, Button, Card, Dropdown, Modal, Select, Space, Tag, Typography } from 'antd'
import { EllipsisOutlined, SwapOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import StatusTag, { type StatusTone } from '../../components/StatusTag'
import { AppDisplayIcon, appDisplayText } from '../../lib/appDisplayV2'
import { clickable } from '../../lib/clickable'
import { compareVersions, latestVersion, offeredUpdate } from '../../lib/semver'
import type { PlatformMarketApp } from '../../api/appPlatform'
import { appPath, type AdminApp } from './appsAdminModel'
import { AppIcon, ServiceStatus } from './appsAdminParts'
import type { LifecycleAction } from './platformLifecycle'

/** A platform installation's control state as a status: the words are ours for the states the server defines. */
export function platformStateTone(state: string | undefined): { tone: StatusTone; key?: string; raw?: string } {
  switch (state) {
    case 'installed': case 'running': case 'active': case 'enabled': return { tone: 'success', key: 'appStore.state.enabled' }
    case 'disabled': return { tone: 'neutral', key: 'appStore.state.disabled' }
    case 'pending': case 'installing': return { tone: 'processing', key: 'appStore.state.installing' }
    case 'setup_required': return { tone: 'warning', key: 'appStore.state.setupRequired' }
    case 'attention': case 'recovery_required': case 'failed': return { tone: 'error', key: 'appStore.state.recovery' }
    default: return { tone: 'neutral', raw: state }
  }
}
/** States in which an administrator may disable, enable or uninstall from the list. A recovery state is handled on the app page. */
export const RECOVERY_STATES = ['attention', 'recovery_required', 'failed']
export const LIFECYCLE_STATES = ['installed', 'running', 'active', 'enabled', 'disabled', 'setup_required']

export interface InstalledCardProps {
  app: AdminApp
  name: string
  description: string
  publisher?: string
  source?: string
  /** The catalogue's entry for this app, when the catalogue on show lists it. */
  catalogue?: PlatformMarketApp
  /** The installation's release can still be uninstalled although the app needs recovery. */
  uninstallable?: boolean
  busy: boolean
  onSwitchVersion: (version: string) => void
  onLifecycle: (action: LifecycleAction) => void
  onViewInCatalogue: () => void
}

/** One installed app, the way Dify lists an installed plugin: wide, with version, state and a menu. */
export function InstalledCard({ app, name, description, publisher, source, catalogue, uninstallable, busy, onSwitchVersion, onLifecycle, onViewInCatalogue }: InstalledCardProps) {
  const { t } = useTranslation()
  // Only a newer stable version is an update for a stable install (a prerelease never is), as with Docker's latest.
  const newest = app.platform && app.version && catalogue ? offeredUpdate(app.version, catalogue.versions.map(v => v.version)) : undefined
  const updatable = !!newest
  const state = app.platform ? platformStateTone(app.state) : null
  const lifecycleOK = app.platform && !!app.installationId && LIFECYCLE_STATES.includes(app.state ?? '')
  const versionLabel = app.version ? `v${app.version}` : ''
  const versionItems = catalogue ? [{
    type: 'group' as const,
    label: t('appStore.switchVersion'),
    children: catalogue.versions.map(v => ({
      key: v.version,
      disabled: v.version === app.version,
      label: <Space size={8}><span>v{v.version}</span>{v.version === app.version && <Tag>{t('appStore.current')}</Tag>}</Space>,
    })),
  }] : []
  const recoveryUninstall = !lifecycleOK && app.platform && !!app.installationId && RECOVERY_STATES.includes(app.state ?? '') && !!uninstallable
  const lifecycleItems = [
    ...(recoveryUninstall ? [{ key: 'uninstall', label: t('appStore.uninstall'), danger: true }] : []),
    ...(lifecycleOK ? [
      app.state === 'disabled' ? { key: 'enable', label: t('appStore.enable') } : { key: 'disable', label: t('appStore.disable') },
      { key: 'uninstall', label: t('appStore.uninstall'), danger: true },
    ] : []),
    ...(app.platform ? [{ key: 'view', label: t('appStore.viewInCatalogue'), disabled: !catalogue }] : []),
  ]
  const footerLeft = [publisher, app.appId ?? (app.builtin ? app.key.replace(/^builtin:/, '') : app.key)].filter(Boolean).join(' / ')
  return (
    <Card hoverable className="rp-app-card" size="small">
      {app.builtin && <span className="rp-app-card-corner">{t('appStore.builtinBadge')}</span>}
      <div className="rp-app-card-head">
        {catalogue?.display && app.platform
          ? <span aria-hidden="true" className="rp-app-glyph"><AppDisplayIcon display={catalogue.display} /></span>
          : <AppIcon app={app} />}
        <div className="rp-app-card-main">
          <div className="rp-app-card-title">
            {/* The link is the card's one way in: its ::after covers the card (stretched link), so a click anywhere opens the app page while the controls inside stay separate. */}
            <Link to={appPath(app.key)} className="rp-app-card-name rp-app-card-link">{name}</Link>
            {app.version && (versionItems.length
              ? <Badge dot={updatable}>
                  <Dropdown trigger={['click']} menu={{ items: versionItems, onClick: ({ key, domEvent }) => { domEvent.stopPropagation(); onSwitchVersion(key) } }} disabled={busy}>
                    <Button size="small" icon={<SwapOutlined aria-hidden="true" />} aria-label={t('appStore.switchVersion')} loading={busy} onClick={e => e.stopPropagation()}>{versionLabel}</Button>
                  </Dropdown>
                </Badge>
              : <Tag className="rp-app-version">{versionLabel}</Tag>)}
            {state && <StatusTag tone={state.tone}>{state.key ? t(state.key) : state.raw}</StatusTag>}
          </div>
          {description && <Typography.Paragraph type="secondary" ellipsis={{ rows: 1 }} className="rp-app-card-desc">{description}</Typography.Paragraph>}
          <div className="rp-app-card-status">
            <ServiceStatus app={app} />
            {updatable && newest && <Button size="small" type="primary" ghost loading={busy} disabled={busy} onClick={e => { e.stopPropagation(); onSwitchVersion(newest) }}>{t('appStore.updateTo', { version: newest })}</Button>}
          </div>
        </div>
        {lifecycleItems.length > 0 && (
          <Dropdown trigger={['click']} menu={{ items: lifecycleItems, onClick: ({ key, domEvent }) => { domEvent.stopPropagation(); if (key === 'view') onViewInCatalogue(); else onLifecycle(key as LifecycleAction) } }} disabled={busy}>
            <Button type="text" icon={<EllipsisOutlined aria-hidden="true" />} aria-label={t('appStore.more')} onClick={e => e.stopPropagation()} />
          </Dropdown>
        )}
      </div>
      <div className="rp-app-card-foot">
        <Typography.Text type="secondary" ellipsis>{footerLeft}</Typography.Text>
        {source && <Typography.Text type="secondary" className="rp-app-card-source">{source}</Typography.Text>}
      </div>
    </Card>
  )
}

export function MarketCard({ app, name, description, onOpen }: { app: PlatformMarketApp; name: string; description: string; onOpen: () => void }) {
  const { t } = useTranslation()
  return (
    <Card hoverable className="rp-app-card rp-market-card" size="small" {...clickable(onOpen, name)}>
      <div className="rp-app-card-head">
        <span aria-hidden="true" className="rp-app-glyph"><AppDisplayIcon display={app.display} /></span>
        <div className="rp-app-card-main">
          <Typography.Text strong ellipsis className="rp-app-card-name">{name}</Typography.Text>
          <Typography.Text type="secondary" ellipsis className="rp-app-card-publisher">{t('appStore.publishedBy', { publisher: app.publisher_id })}</Typography.Text>
        </div>
      </div>
      <Typography.Paragraph type="secondary" ellipsis={{ rows: 2 }} className="rp-market-card-desc">{description || ' '}</Typography.Paragraph>
    </Card>
  )
}

export interface MarketDetailProps {
  app: PlatformMarketApp | null
  /** The installed app this catalogue entry is, if any: the button then switches version instead of installing. */
  installed?: { version?: string }
  lang: string
  /** The version being downloaded for this app, if any. */
  stagingVersion: string | null
  /** Another download or upload is running. */
  blocked: boolean
  error: string
  onClose: () => void
  onInstall: (version: string) => void
  onOpenTrust: () => void
}

/** The catalogue detail of one app: version choice and Install on top, description and facts below. */
export function MarketDetailModal({ app, installed, lang, stagingVersion, blocked, error, onClose, onInstall, onOpenTrust }: MarketDetailProps) {
  const { t } = useTranslation()
  const [chosen, setChosen] = useState('')
  const latest = app ? latestVersion(app.versions.map(v => v.version)) ?? '' : ''
  const version = app?.versions.some(v => v.version === chosen) ? chosen : latest
  const isCurrent = !!installed && !!installed.version && version === installed.version
  const actionLabel = !installed ? t('appStore.installAction') : installed.version && compareVersions(version, installed.version) > 0 ? t('appStore.updateTo', { version }) : t('appStore.switchTo')
  const copy = app ? appDisplayText(app.app_id, app.display, lang) : { name: '', description: '' }
  const trusted = !!app?.publisher_trusted
  const staging = stagingVersion !== null
  return (
    <Modal open={!!app} onCancel={onClose} footer={null} width={760} destroyOnHidden afterOpenChange={open => { if (!open) setChosen('') }}
      title={app && <div className="rp-market-detail-head">
        <span aria-hidden="true" className="rp-app-glyph rp-app-glyph--large"><AppDisplayIcon display={app.display} /></span>
        <div className="rp-market-detail-title">
          <Typography.Text strong ellipsis>{copy.name}</Typography.Text>
          <Typography.Text type="secondary" ellipsis>{t('appStore.publishedBy', { publisher: app.publisher_id })}</Typography.Text>
        </div>
        <Space wrap className="rp-market-detail-actions">
          <Select size="middle" aria-label={t('appStore.versions')} value={version} disabled={staging} onChange={setChosen} popupMatchSelectWidth={false}
            options={app.versions.map(v => ({ value: v.version, label: <Space size={6}>{v.version}{v.version === latest && <Tag>{t('appStore.latest')}</Tag>}</Space> }))} style={{ minWidth: 150 }} />
          {isCurrent
            ? <Typography.Text type="secondary">{t('appStore.alreadyInstalled')}</Typography.Text>
            : <Button type="primary" loading={staging} disabled={!trusted || blocked} onClick={() => onInstall(version)}>
                {staging ? t('appStore.staging') : actionLabel}
              </Button>}
        </Space>
      </div>}>
      {app && <div className="rp-market-detail-body">
        <div>
          {!trusted && <Space size={4} wrap className="rp-market-detail-untrusted">
            <Typography.Text type="secondary">{t('appStore.untrusted')}</Typography.Text>
            <Button type="link" size="small" onClick={onOpenTrust}>{t('appStore.openTrust')}</Button>
          </Space>}
          {error && <Alert type="error" title={error} />}
          <Typography.Paragraph>{copy.description || t('appStore.noDescription')}</Typography.Paragraph>
        </div>
        <aside className="rp-market-detail-side">
          <Typography.Text strong>{t('appStore.versions')}</Typography.Text>
          <ul className="rp-market-versions">
            {app.versions.map(v => <li key={v.version}>{v.version}{v.version === latest && <> <Tag>{t('appStore.latest')}</Tag></>}{installed?.version === v.version && <> <Tag>{t('appStore.current')}</Tag></>}</li>)}
          </ul>
          <Typography.Text strong>{t('appPlatform.publisher')}</Typography.Text>
          <div>{app.publisher_id}</div>
          <Typography.Text type="secondary" className="rp-market-detail-note">{t('appStore.signatureCheck')}</Typography.Text>
        </aside>
      </div>}
    </Modal>
  )
}

