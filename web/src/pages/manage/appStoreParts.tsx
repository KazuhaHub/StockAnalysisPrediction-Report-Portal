import { useState } from 'react'
import { Alert, Button, Card, Modal, Progress, Select, Space, Tag, Tooltip, Typography } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import StatusTag, { type StatusTone } from '../../components/StatusTag'
import { AppDisplayIcon, appDisplayText, publisherLabel, publisherWithId } from '../../lib/appDisplayV2'
import { compareVersions, latestVersion, offeredUpdate } from '../../lib/semver'
import type { PlatformMarketApp } from '../../api/appPlatform'
import { appHref, type AdminApp } from './appsAdminModel'
import { AppIcon, ServiceStatus } from './appsAdminParts'
import type { PlatformOperationRow } from '../../api/appOperations'
import InstallationActions from './InstallationActions'
import InstallationOperationLine from './InstallationOperationLine'
import type { LifecycleAction, PickMode } from './platformLifecycle'
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
export interface InstalledActionProps {
  app: AdminApp
  /** The installation's release can still be uninstalled although the app needs recovery. */
  uninstallable?: boolean
  /** This app's own action is running: its buttons spin. */
  busy: boolean
  /** Another action holds the console (`pending`: an unfinished change that needs the administrator). Every control that would be refused stays reachable and says why. */
  blocked?: '' | 'pending' | 'working'
  /** A press on a control that is blocked: it answers with the reason instead of going quiet. */
  onRefuse?: () => void
  /** Opens the version step of the lifecycle dialog; nothing is read from the catalogue before this is pressed. */
  onPick: (mode: PickMode, opener?: HTMLElement | null) => void
  /** `opener` is the control pressed, where focus returns when the flow ends (a menu item is gone by then). */
  onLifecycle: (action: LifecycleAction, opener?: HTMLElement | null) => void
  /** The newest operation of this installation: a progress line while it runs, a result badge after. */
  operation?: PlatformOperationRow
  onOpenOperation?: (id: string, opener: HTMLElement) => void
}

/** The shared installation actions in their card form (a Switch version button and a more menu), for a platform app. */
export function InstalledControls({ app, uninstallable, busy, blocked = '', onRefuse, onPick, onLifecycle }: InstalledActionProps) {
  if (!app.platform || !app.installationId) return null
  return <InstallationActions variant="card" state={app.state} uninstallable={uninstallable} versioned={!!app.version} busy={busy} blocked={blocked} onRefuse={onRefuse} onPick={onPick} onLifecycle={onLifecycle} />
}

/** An app's newest operation as the progress line or result badge it is shown as, or nothing before it has had one. */
export function InstalledOperation({ operation, onOpenOperation }: Pick<InstalledActionProps, 'operation' | 'onOpenOperation'>) {
  if (!operation || !onOpenOperation) return null
  return <InstallationOperationLine row={operation} onOpen={opener => onOpenOperation(operation.operation_id, opener)} />
}

/** A platform app's state as a status tag, or nothing for a built-in or legacy app. */
export function InstalledState({ app }: { app: AdminApp }) {
  const { t } = useTranslation()
  if (!app.platform) return null
  const state = platformStateTone(app.state)
  return <StatusTag tone={state.tone}>{state.key ? t(state.key) : state.raw}</StatusTag>
}

/**
 * A secondary line that names the publisher. The self-declared name is what shows; the publisher ID, which trust is
 * keyed on, sits in a keyboard-focusable tooltip so an administrator can tell two similar names apart.
 */
export function PublisherLine({ publisherName, publisherId, className, title, children }: { publisherName?: string; publisherId?: string; className?: string; title?: string; children: React.ReactNode }) {
  if (!publisherName || !publisherId) return <Typography.Text type="secondary" className={className} title={title}>{children}</Typography.Text>
  return <Tooltip title={publisherId} trigger={['hover', 'focus']}><Typography.Text type="secondary" className={[className, 'rp-publisher-line'].filter(Boolean).join(' ')} title={title} tabIndex={0}>{children}</Typography.Text></Tooltip>
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
  // The publisher's name and the version. With no verified name the slot stays empty: an id is never a stand-in for a name.
  const secondary = [publisher, app.version].filter(Boolean).join(' · ')
  return (
    <Card hoverable className="rp-app-card" size="small">
      {app.builtin && <span className="rp-app-card-corner">{t('appStore.builtinBadge')}</span>}
      <div className="rp-app-card-row">
        {app.display && app.platform
          ? <span aria-hidden="true" className="rp-app-glyph"><AppDisplayIcon display={app.display} /></span>
          : <AppIcon app={app} size={32} />}
        <div className="rp-app-card-main">
          <div className="rp-app-card-line">
            {/* The link is the card's one way in: its ::after covers the card, so a click anywhere opens the app page while the controls stay separate. */}
            <Link to={appHref(app)} className="rp-app-card-name rp-app-card-link">{name}</Link>
            {secondary && <PublisherLine publisherName={app.publisherName} publisherId={app.publisherId} className="rp-app-card-sub" title={source ? `${secondary} · ${source}` : secondary}>{secondary}</PublisherLine>}
          </div>
          {description && <div className="rp-app-card-desc">{description}</div>}
          <div className="rp-app-card-meta"><InstalledState app={app} /><ServiceStatus app={app} compact /></div>
          <InstalledOperation operation={actions.operation} onOpenOperation={actions.onOpenOperation} />
          {app.platform && !!app.installationId && <div className="rp-app-card-foot"><InstalledControls {...actions} /></div>}
        </div>
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
          {publisherLabel(app.publisher_name) && <PublisherLine publisherName={app.publisher_name} publisherId={app.publisher_id} className="rp-app-card-sub rp-app-card-line">{t('appStore.publishedBy', { publisher: publisherLabel(app.publisher_name) })}</PublisherLine>}
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
          {publisherLabel(app.publisher_name) && <Typography.Text type="secondary" ellipsis>{t('appStore.publishedBy', { publisher: publisherLabel(app.publisher_name) })}</Typography.Text>}
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
          <div>{publisherWithId(app.publisher_name, app.publisher_id)}</div>
          <Typography.Text type="secondary" className="rp-market-detail-note">{t('appStore.signatureCheck')}</Typography.Text>
        </aside>
      </div>}
    </Modal>
  )
}

