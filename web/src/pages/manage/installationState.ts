import type { Settings } from './AppPlatformSettingsPage'
import type { StatusTone } from '../../components/StatusTag'

// What an installation's settings say about it: whether it is serving, and the control state the app cards use.

export type RunState = 'running' | 'stopped' | 'disabled' | 'recovery' | 'pending' | 'setup'

/** Whether the app is serving: running, stopped (its backend is not), disabled, in recovery, or still being set up. */
export function runState(settings: Pick<Settings, 'phase' | 'desired_state' | 'backend'>): RunState {
  if (settings.desired_state === 'disabled' || settings.phase === 'disabled') return 'disabled'
  if (['recovery_required', 'quarantined', 'review_required'].includes(settings.phase)) return 'recovery'
  if (settings.phase === 'pending') return 'pending'
  if (settings.phase === 'setup_required') return 'setup'
  if (settings.backend.required && !(settings.backend.registered && settings.backend.enabled)) return 'stopped'
  return 'running'
}
export const RUN_TONE: Record<RunState, StatusTone> = { running: 'success', stopped: 'warning', disabled: 'neutral', recovery: 'error', pending: 'processing', setup: 'warning' }

/** The control state the app cards use (`AdminApp.state`), from an installation's settings, so card and page offer the same actions. */
export function controlState(settings: Pick<Settings, 'phase' | 'desired_state'>): string {
  if (settings.desired_state === 'disabled' || settings.phase === 'disabled') return 'disabled'
  if (settings.phase === 'ready') return 'installed'
  if (settings.phase === 'pending') return 'pending'
  if (settings.phase === 'setup_required') return 'setup_required'
  return 'attention'
}

