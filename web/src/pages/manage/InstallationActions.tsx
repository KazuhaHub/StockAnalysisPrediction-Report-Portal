import { useId, useRef } from 'react'
import { Button, Dropdown, Space, Tag } from 'antd'
import { ArrowUpOutlined, EllipsisOutlined, PauseCircleOutlined, PlayCircleOutlined, SwapOutlined, ToolOutlined, DeleteOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { LifecycleAction, PickMode } from './platformLifecycle'

/** States in which an administrator may switch version, disable, enable or uninstall. A recovery state is handled on the app's page. */
export const LIFECYCLE_STATES = ['installed', 'running', 'active', 'enabled', 'disabled', 'setup_required']
export const RECOVERY_STATES = ['attention', 'recovery_required', 'failed']

export type InstallationActionKey = 'update' | 'switch' | 'disable' | 'enable' | 'repair' | 'uninstall'

export interface InstallationActionFacts {
  /** The installation's control state (an app layout `state`, or a settings `phase`). */
  state?: string
  /** An app in a recovery state can still be uninstalled when its release allows it. */
  uninstallable?: boolean
  /** The server offers a repair (installation settings). */
  repairEligible?: boolean
  /** The installed version is known, which a switch needs to name the current one. */
  versioned?: boolean
  /** A newer compatible release the catalogue has: Update is offered only then, and names it. */
  updateTo?: string
}

/**
 * Which actions an installation offers right now. One rule for the app card and the installation page, so the two
 * never disagree: the lifecycle states allow a switch, an update, disable or enable and uninstall; a recovery state
 * allows only an uninstall its release permits, and a repair the server offers.
 */
export function availableActions({ state = '', uninstallable, repairEligible, versioned = true, updateTo }: InstallationActionFacts): InstallationActionKey[] {
  const keys: InstallationActionKey[] = []
  if (LIFECYCLE_STATES.includes(state)) {
    if (versioned) { if (updateTo) keys.push('update'); keys.push('switch') }
    // A disabled app the server can repair has one way back, and Enable and Repair open the same flow: Repair alone.
    if (state !== 'disabled') keys.push('disable')
    else if (!repairEligible) keys.push('enable')
  }
  if (repairEligible) keys.push('repair')
  if (LIFECYCLE_STATES.includes(state) || (RECOVERY_STATES.includes(state) && uninstallable)) keys.push('uninstall')
  return keys
}

export interface InstallationActionsProps extends InstallationActionFacts {
  /** `card` is a Switch version button and a more menu; `toolbar` lists every action as a button. */
  variant: 'card' | 'toolbar'
  /** This installation's own action is running: its buttons spin. */
  busy: boolean
  /** Another action holds the console (`pending`: an unfinished change that needs the administrator). A control that would be refused stays reachable and says why. */
  blocked?: '' | 'pending' | 'working'
  /** A press on a blocked control answers with the reason instead of going quiet. */
  onRefuse?: () => void
  /** Opens the version choice. `opener` is the control pressed, where focus returns when the flow ends. */
  onPick: (mode: PickMode, opener?: HTMLElement | null) => void
  onLifecycle: (action: LifecycleAction, opener?: HTMLElement | null) => void
}

const ICONS = {
  update: <ArrowUpOutlined aria-hidden="true" />,
  switch: <SwapOutlined aria-hidden="true" />,
  disable: <PauseCircleOutlined aria-hidden="true" />,
  enable: <PlayCircleOutlined aria-hidden="true" />,
  repair: <ToolOutlined aria-hidden="true" />,
  uninstall: <DeleteOutlined aria-hidden="true" />,
}
const LABELS: Record<InstallationActionKey, string> = {
  update: 'appPlatform.update', switch: 'appStore.switchVersion', disable: 'appStore.disable', enable: 'appStore.enable', repair: 'appPlatform.repair', uninstall: 'appStore.uninstall',
}

/**
 * The actions of one installation, shared by the app card and the installation page: the same buttons, the same
 * rules (`availableActions`), the same flows. A press never acts by itself: Update and Switch version open the
 * version step of the lifecycle dialog, and the rest open that action's review.
 */
export default function InstallationActions({ variant, busy, blocked = '', onRefuse, onPick, onLifecycle, ...facts }: InstallationActionsProps) {
  const { t } = useTranslation()
  const more = useRef<HTMLButtonElement>(null)
  const reasonId = useId()
  const keys = availableActions(facts)
  if (!keys.length) return null
  // One rule for every control here: it acts exactly when pressing it can. A blocked control is not `disabled` (that
  // would take it out of the keyboard's reach and hide the reason): it stays focusable, says it is unavailable, names
  // the reason in its description, and a press answers with that reason.
  const idle = !busy && !blocked
  const reason = blocked === 'pending' ? t('appStore.busyPending') : blocked ? t('appStore.busyWorking') : ''
  const inert = blocked ? { 'aria-disabled': true as const, 'aria-describedby': reasonId } : {}
  const run = (key: InstallationActionKey, opener: HTMLElement | null) => {
    if (blocked) { onRefuse?.(); return }
    if (key === 'update') onPick('update', opener)
    else if (key === 'switch') onPick('switch', opener)
    else onLifecycle(key, opener)
  }
  const reasonNote = reason && <span id={reasonId} className="rp-sr-only">{reason}</span>
  const button = (key: InstallationActionKey, extra: { type?: 'primary' | 'default' | 'link'; size?: 'small' } = {}) => (
    <Button key={key} size={extra.size} type={extra.type} danger={key === 'uninstall'} icon={ICONS[key]} {...inert} className={blocked ? 'rp-app-inert' : undefined}
      title={reason || undefined} loading={busy && (key === 'update' || key === 'switch')} disabled={busy}
      onClick={e => { e.stopPropagation(); run(key, e.currentTarget) }}>{key === 'update' && facts.updateTo ? t('appStore.updateTo', { version: facts.updateTo }) : t(LABELS[key])}</Button>
  )
  if (variant === 'toolbar') {
    return <Space wrap className="rp-installation-actions" role="group" aria-label={t('appPlatform.settings.lifecycleActions')}>
      {reasonNote}
      {keys.map(key => button(key, { type: key === 'update' ? 'primary' : 'default' }))}
    </Space>
  }
  // A card: the switch is its one visible button; everything else is behind the more menu.
  const menu = keys.filter(key => key === 'disable' || key === 'enable' || key === 'uninstall').map(key => ({ key, label: t(LABELS[key]), danger: key === 'uninstall', disabled: !idle }))
  return <span className="rp-app-controls">
    {reasonNote}
    {keys.includes('switch') && <Button size="small" icon={ICONS.switch} {...inert} className={['rp-app-switch', blocked ? 'rp-app-inert' : ''].filter(Boolean).join(' ')} title={reason || t('appStore.switchVersion')}
      loading={busy} disabled={busy} onClick={e => { e.stopPropagation(); run('switch', e.currentTarget) }}>{t('appStore.switchVersion')}</Button>}
    {menu.length > 0 && <Dropdown trigger={['click']} menu={{ items: menu, onClick: ({ key, domEvent }) => { domEvent.stopPropagation(); run(key as InstallationActionKey, more.current) } }}>
      <Button ref={more} size="small" type="text" icon={<EllipsisOutlined aria-hidden="true" />} aria-label={t('appStore.more')} onClick={e => e.stopPropagation()} />
    </Dropdown>}
  </span>
}

/** "Update available · 1.1.0": a quiet tag, not a status, so a card or header that carries it keeps its layout. */
export function UpdateAvailableTag({ version }: { version: string }) {
  const { t } = useTranslation()
  return <Tag className="rp-update-tag" icon={<ArrowUpOutlined aria-hidden="true" />}>{t('appStore.updateAvailable', { version })}</Tag>
}
