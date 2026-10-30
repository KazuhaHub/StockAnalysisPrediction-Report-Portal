import type { ReactNode } from 'react'
import { Progress, Typography, theme } from 'antd'

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
}

// One task in the Tasks drawer: exactly two lines. A status dot, then the name, a muted qualifier
// and the time on the first line; a bar, queue position or result on the second. Actions sit in
// their own column at the row end, hidden until the row is hovered or focused (see .rp-task-row).
export default function TaskRow({ tone, name, sub, detail, percent, time, actions, pinned }: TaskRowProps) {
  const { token } = theme.useToken()
  const color: Record<TaskTone, string> = {
    running: token.colorPrimary,
    waiting: token.colorWarning,
    ok: token.colorSuccess,
    warn: token.colorWarning,
    fail: token.colorError,
    idle: token.colorTextQuaternary,
  }
  return (
    <li className={pinned ? 'rp-task-row is-active' : 'rp-task-row'} style={{ listStyle: 'none' }}>
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
          {time && (
            <span style={{ marginLeft: 'auto', whiteSpace: 'nowrap', flex: 'none', fontSize: 12, color: token.colorTextSecondary }}>{time}</span>
          )}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 2, fontSize: 12, color: token.colorTextSecondary, minWidth: 0 }}>
          {percent !== undefined && (
            <Progress percent={percent} size={['100%', 4]} showInfo={false} style={{ flex: 1, maxWidth: 160, margin: 0, lineHeight: 1 }} />
          )}
          <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{detail}</span>
        </div>
      </div>
      {actions && <div className="rp-task-row__acts">{actions}</div>}
    </li>
  )
}
