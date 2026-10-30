import type { KeyboardEvent, ReactNode } from 'react'
import { Progress, Typography, theme } from 'antd'
import { useCoarsePointer } from '../lib/useCoarsePointer'

export type TaskTone = 'running' | 'waiting' | 'ok' | 'warn' | 'fail' | 'idle'

export interface TaskRowProps {
  tone: TaskTone
  name: ReactNode
  /** Muted text beside the name: a run's inputs. */
  sub?: ReactNode
  /** Second line: a progress bar and count, a queue position, or a result summary. */
  detail: ReactNode
  /** A 0-100 bar before the detail text; only a running task has one. */
  percent?: number
  time?: string
  actions?: ReactNode
  /** Keeps the actions on screen, e.g. while a confirmation from one of them is open. */
  pinned?: boolean
  /** Touch only: whether a tap has revealed the actions. Controlled by the list, so one row is open at a time. */
  expanded?: boolean
  /** Touch only: the row was tapped, or activated from the keyboard. */
  onToggle?: () => void
}

// One task in the Tasks drawer: exactly two lines. A status dot, then the name and a muted qualifier
// on the first line; a bar, queue position or result on the second. The time is flush with the row's
// right edge and shares one cell with the actions: hovering or focusing the row (or tapping it on a
// touch device) slides the actions in over the time (see .rp-task-row__end).
export default function TaskRow({ tone, name, sub, detail, percent, time, actions, pinned, expanded, onToggle }: TaskRowProps) {
  const { token } = theme.useToken()
  const coarse = useCoarsePointer()
  const open = coarse && !!expanded
  const toggleKey = (e: KeyboardEvent) => {
    if (e.target !== e.currentTarget) return
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onToggle?.()
    }
  }
  const color: Record<TaskTone, string> = {
    running: token.colorPrimary,
    waiting: token.colorWarning,
    ok: token.colorSuccess,
    warn: token.colorWarning,
    fail: token.colorError,
    idle: token.colorTextQuaternary,
  }
  const cls = ['rp-task-row', pinned && 'is-active', open && 'is-open'].filter(Boolean).join(' ')
  return (
    <li className={cls} style={{ listStyle: 'none' }}>
      <div
        className="rp-task-row__main"
        {...(coarse
          ? { role: 'button', tabIndex: 0, 'aria-expanded': open, onClick: onToggle, onKeyDown: toggleKey }
          : {})}
      >
        <span
          aria-hidden="true"
          className="rp-task-row__dot"
          style={{
            background: color[tone],
            boxShadow: tone === 'running' ? `0 0 0 3px ${token.colorPrimaryBorder}` : undefined,
          }}
        />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, minWidth: 0 }}>
            <Typography.Text ellipsis style={{ flex: '0 1 auto', minWidth: 0 }}>
              {name}
            </Typography.Text>
            {sub && <span style={{ flex: '1 1 0', minWidth: 0, overflow: 'hidden', fontSize: 12 }}>{sub}</span>}
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 2, fontSize: 12, color: token.colorTextSecondary, minWidth: 0 }}>
            {percent !== undefined && (
              <Progress percent={percent} size={['100%', 4]} showInfo={false} style={{ flex: 1, maxWidth: 160, margin: 0, lineHeight: 1 }} />
            )}
            <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{detail}</span>
          </div>
        </div>
      </div>
      {(time || actions) && (
        <div className="rp-task-row__end">
          {time && (
            <span className="rp-task-row__time" style={{ color: token.colorTextSecondary }}>
              {time}
            </span>
          )}
          {actions && (
            <div className="rp-task-row__acts" inert={coarse && !open && !pinned}>
              {actions}
            </div>
          )}
        </div>
      )}
    </li>
  )
}
