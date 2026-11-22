import { useState } from 'react'
import { Alert, Button, Card, Dropdown, Modal, Progress, Select, Space, Tag, Typography } from 'antd'
import { ArrowUpOutlined, EllipsisOutlined, SwapOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import StatusTag, { type StatusTone } from '../../components/StatusTag'
import { AppDisplayIcon, appDisplayText } from '../../lib/appDisplayV2'
import { compareVersions, latestVersion, offeredUpdate } from '../../lib/semver'
import type { PlatformMarketApp } from '../../api/appPlatform'
import { appPath, type AdminApp } from './appsAdminModel'
import { AppIcon, ServiceStatus } from './appsAdminParts'
import type { LifecycleAction } from './platformLifecycle'
import type { Staging } from './platformMarket'

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

export interface InstalledActionProps {
  app: AdminApp
  /** The catalogue's entry for this app, when the catalogue on show lists it. */
  catalogue?: PlatformMarketApp
  /** The installation's release can still be uninstalled although the app needs recovery. */
  uninstallable?: boolean
  busy: boolean
  onSwitchVersion: (version: string) => void
  onLifecycle: (action: LifecycleAction) => void
  onViewInCatalogue: () => void
}

/** The version an installed platform app would be updated to, by the prerelease policy; undefined when none. */
export function updateOffer(app: AdminApp, catalogue?: PlatformMarketApp): string | undefined {
  return app.platform && app.version && catalogue ? offeredUpdate(app.version, catalogue.versions.map(v => v.version)) : undefined
}

/** Small icon buttons for an installed app: update, switch version, and the more menu. Used by the card and the list. */
export function InstalledControls({ app, catalogue, uninstallable, busy, onSwitchVersion, onLifecycle, onViewInCatalogue }: InstalledActionProps) {
  const { t } = useTranslation()
  const newest = updateOffer(app, catalogue)
  const lifecycleOK = app.platform && !!app.installationId && LIFECYCLE_STATES.includes(app.state ?? '')
  const recoveryUninstall = !lifecycleOK && app.platform && !!app.installationId && RECOVERY_STATES.includes(app.state ?? '') && !!uninstallable
  const versionItems = catalogue && app.version ? [{
    type: 'group' as const,
    label: t('appStore.switchVersion'),
    children: catalogue.versions.map(v => ({
      key: v.version,
      disabled: v.version === app.version,
      label: <Space size={8}><span>{v.version}</span>{v.version === app.version && <Tag>{t('appStore.current')}</Tag>}</Space>,
    })),
  }] : []
  const lifecycleItems = [
    ...(recoveryUninstall ? [{ key: 'uninstall', label: t('appStore.uninstall'), danger: true }] : []),
    ...(lifecycleOK ? [
      app.state === 'disabled' ? { key: 'enable', label: t('appStore.enable') } : { key: 'disable', label: t('appStore.disable') },
      { key: 'uninstall', label: t('appStore.uninstall'), danger: true },
    ] : []),
    ...(app.platform ? [{ key: 'view', label: t('appStore.viewInCatalogue'), disabled: !catalogue }] : []),
  ]
  if (!newest && !versionItems.length && !lifecycleItems.length) return null
  const updateLabel = newest ? t('appStore.updateTo', { version: newest }) : ''
  return (
    <span className="rp-app-controls">
      {newest && <Button size="small" type="text" className="rp-app-update" icon={<ArrowUpOutlined aria-hidden="true" />} aria-label={updateLabel} title={updateLabel} loading={busy} disabled={busy} onClick={e => { e.stopPropagation(); onSwitchVersion(newest) }} />}
      {versionItems.length > 0 && (
        <Dropdown trigger={['click']} menu={{ items: versionItems, onClick: ({ key, domEvent }) => { domEvent.stopPropagation(); onSwitchVersion(key) } }} disabled={busy}>
          <Button size="small" type="text" icon={<SwapOutlined aria-hidden="true" />} aria-label={t('appStore.switchVersion')} title={t('appStore.switchVersion')} onClick={e => e.stopPropagation()} />
        </Dropdown>
      )}
      {lifecycleItems.length > 0 && (
        <Dropdown trigger={['click']} menu={{ items: lifecycleItems, onClick: ({ key, domEvent }) => { domEvent.stopPropagation(); if (key === 'view') onViewInCatalogue(); else onLifecycle(key as LifecycleAction) } }} disabled={busy}>
          <Button size="small" type="text" icon={<EllipsisOutlined aria-hidden="true" />} aria-label={t('appStore.more')} title={t('appStore.more')} onClick={e => e.stopPropagation()} />
        </Dropdown>
      )}
    </span>
  )
}

/** A platform app's state as a status tag, or nothing for a built-in or legacy app. */
export function InstalledState({ app }: { app: AdminApp }) {
  const { t } = useTranslation()
  if (!app.platform) return null
  const state = platformStateTone(app.state)
  return <StatusTag tone={state.tone}>{state.key ? t(state.key) : state.raw}</StatusTag>
}

export interface InstalledCardProps extends InstalledActionProps {
  name: string
  description: string
  publisher?: string
  source?: string
}

/**
 * One installed app at Dify's density: icon, a line of name and "publisher · version", a line of description, a
 * tiny line of status. Small icon buttons sit top right. The name's link covers the whole card (stretched link).
 */
export function InstalledCard({ name, description, publisher, source, ...actions }: InstalledCardProps) {
  const { t } = useTranslation()
  const { app } = actions
  const secondary = [publisher ?? app.appId ?? (app.builtin ? undefined : app.key), app.version].filter(Boolean).join(' · ')
  return (
    <Card hoverable className="rp-app-card" size="small">
      {app.builtin && <span className="rp-app-card-corner">{t('appStore.builtinBadge')}</span>}
      <div className="rp-app-card-row">
        {actions.catalogue?.display && app.platform
          ? <span aria-hidden="true" className="rp-app-glyph"><AppDisplayIcon display={actions.catalogue.display} /></span>
          : <AppIcon app={app} size={32} />}
        <div className="rp-app-card-main">
          <div className="rp-app-card-line">
            {/* The link is the card's one way in: its ::after covers the card, so a click anywhere opens the app page while the controls stay separate. */}
            <Link to={appPath(app.key)} className="rp-app-card-name rp-app-card-link">{name}</Link>
            {secondary && <Typography.Text type="secondary" className="rp-app-card-sub" title={source ? `${secondary} · ${source}` : secondary}>{secondary}</Typography.Text>}
          </div>
          {description && <div className="rp-app-card-desc">{description}</div>}
          <div className="rp-app-card-meta"><InstalledState app={app} /><ServiceStatus app={app} /></div>
        </div>
        <InstalledControls {...actions} />
      </div>
    </Card>
  )
}

const MiB = 1024 * 1024
/** How far a download is, as a whole percent below 100, once the server has said how big the package is. */
export function stagingPercent(staging: Staging): number | undefined {
  if (!staging?.bytesTotal || staging.bytesDone === undefined) return undefined
  return Math.min(99, Math.floor(staging.bytesDone * 100 / staging.bytesTotal))
}
/** The words for a download in progress: the MiB counts once the size is known, else "downloading and verifying". */
export function stagingText(staging: Staging, t: TFunction): string {
  if (!staging?.bytesTotal || staging.bytesDone === undefined) return t('appStore.staging')
  return t('appStore.downloadProgress', { done: Math.floor(staging.bytesDone / MiB), total: Math.ceil(staging.bytesTotal / MiB) })
}

/**
 * One catalogue app at the same size: icon, name, publisher, description, and a small Install button top right.
 * The name is a real button whose ::after covers the card, so a click anywhere else opens the details.
 */
export function MarketCard({ app, name, description, staging, blocked, error, onOpen, onInstall }: { app: PlatformMarketApp; name: string; description: string; staging: Staging; blocked: boolean; error?: string; onOpen: () => void; onInstall: () => void }) {
  const { t } = useTranslation()
  const installing = !!staging
  const percent = stagingPercent(staging)
  return (
    <Card hoverable className="rp-app-card rp-market-card" size="small">
      <div className="rp-app-card-row">
        <span aria-hidden="true" className="rp-app-glyph"><AppDisplayIcon display={app.display} /></span>
        <div className="rp-app-card-main">
          <div className="rp-app-card-line">
            <Button type="text" className="rp-app-card-name rp-app-card-link" onClick={onOpen}>{name}</Button>
          </div>
          <Typography.Text type="secondary" className="rp-app-card-sub rp-app-card-line">{t('appStore.publishedBy', { publisher: app.publisher_id })}</Typography.Text>
          {error
            ? <Typography.Text type="danger" className="rp-app-card-desc" title={error} role="alert">{error}</Typography.Text>
            : <div className="rp-app-card-desc">{description || '\u00a0'}</div>}
        </div>
        <span className="rp-app-controls">
          <Button size="small" type="primary" loading={installing} disabled={!app.publisher_trusted || blocked} title={installing ? stagingText(staging, t) : app.publisher_trusted ? undefined : t('appStore.untrusted')} onClick={onInstall}>{percent !== undefined ? `${percent}%` : t('appStore.installAction')}</Button>
        </span>
      </div>
    </Card>
  )
}

export interface MarketDetailProps {
  app: PlatformMarketApp | null
  /** The installed app this catalogue entry is, if any: the button then switches version instead of installing. */
  installed?: { version?: string }
  lang: string
  /** The download running for this app, if any. */
  staging: Staging
  /** Another download or upload is running. */
  blocked: boolean
  error: string
  onClose: () => void
  onInstall: (version: string) => void
  onOpenTrust: () => void
}

/** The catalogue detail of one app: version choice and Install on top, description and facts below. */
export function MarketDetailModal({ app, installed, lang, staging: download, blocked, error, onClose, onInstall, onOpenTrust }: MarketDetailProps) {
  const { t } = useTranslation()
  const [chosen, setChosen] = useState('')
  const latest = app ? latestVersion(app.versions.map(v => v.version)) ?? '' : ''
  // For an installed app the dialog opens on the version it would be updated to, or else on the installed one.
  const offered = app && installed?.version ? offeredUpdate(installed.version, app.versions.map(v => v.version)) : undefined
  const fallback = offered ?? (installed?.version && app?.versions.some(v => v.version === installed.version) ? installed.version : latest)
  const version = app?.versions.some(v => v.version === chosen) ? chosen : fallback
  const isCurrent = !!installed && !!installed.version && version === installed.version
  const actionLabel = !installed ? t('appStore.installAction') : installed.version && compareVersions(version, installed.version) > 0 ? t('appStore.updateTo', { version }) : t('appStore.switchTo')
  const copy = app ? appDisplayText(app.app_id, app.display, lang) : { name: '', description: '' }
  const trusted = !!app?.publisher_trusted
  const staging = !!download
  const percent = stagingPercent(download)
  return (
    <Modal open={!!app} onCancel={onClose} footer={null} width={760} destroyOnHidden afterOpenChange={open => { if (!open) setChosen('') }}
      title={app && <div className="rp-market-detail-head">
        <span aria-hidden="true" className="rp-app-glyph rp-app-glyph--large"><AppDisplayIcon display={app.display} /></span>
        <div className="rp-market-detail-title">
          <Typography.Text strong ellipsis>{copy.name}</Typography.Text>
          <Typography.Text type="secondary" ellipsis>{t('appStore.publishedBy', { publisher: app.publisher_id })}</Typography.Text>
        </div>
        <Space wrap className="rp-market-detail-actions">
          <Select size="middle" aria-label={t('appStore.versions')} value={download?.version ?? version} disabled={staging} onChange={setChosen} popupMatchSelectWidth={false}
            options={app.versions.map(v => ({ value: v.version, label: <Space size={6}>{v.version}{v.version === latest && <Tag>{t('appStore.latest')}</Tag>}</Space> }))} style={{ minWidth: 150 }} />
          {isCurrent
            ? <Typography.Text type="secondary">{t('appStore.alreadyInstalled')}</Typography.Text>
            : <Button type="primary" loading={staging} disabled={!trusted || blocked} onClick={() => onInstall(version)}>
                {staging ? stagingText(download, t) : actionLabel}
              </Button>}
        </Space>
        {percent !== undefined && <Progress className="rp-market-detail-progress" percent={percent} size="small" aria-label={stagingText(download, t)} />}
        {error && <Alert className="rp-market-detail-error" type="error" showIcon title={error} />}
      </div>}>
      {app && <div className="rp-market-detail-body">
        <div>
          {!trusted && <Space size={4} wrap className="rp-market-detail-untrusted">
            <Typography.Text type="secondary">{t('appStore.untrusted')}</Typography.Text>
            <Button type="link" size="small" onClick={onOpenTrust}>{t('appStore.openTrust')}</Button>
          </Space>}
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

