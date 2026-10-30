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
}

// One task in the Tasks drawer: exactly two lines. A status dot, then the name and a muted
// qualifier on the first line; a bar, queue position or result and a short time on the second.
// Actions sit at the row end, muted until the row is hovered or focused (see .rp-task-row).
export default function TaskRow({ tone, name, sub, detail, percent, time, actions }: TaskRowProps) {
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
    <li className="rp-task-row" style={{ listStyle: 'none' }}>
      <span
        aria-hidden="true"
        style={{
          width: 8,
          height: 8,
          borderRadius: '50%',
          flex: 'none',
          background: color[tone],
          boxShadow: tone === 'running' ? `0 0 0 3px ${token.colorPrimaryBorder}` : undefined,
        }}
      />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, minWidth: 0 }}>
          <Typography.Text ellipsis style={{ flex: '0 1 auto', minWidth: 0 }}>
            {name}
          </Typography.Text>
          {sub && <span style={{ flex: '1 1 0', minWidth: 0, overflow: 'hidden' }}>{sub}</span>}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 3, fontSize: 12, color: token.colorTextSecondary, minWidth: 0 }}>
          {percent !== undefined && (
            <Progress percent={percent} size={['100%', 4]} showInfo={false} style={{ flex: 1, maxWidth: 160, margin: 0, lineHeight: 1 }} />
          )}
          <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{detail}</span>
          {time && <span style={{ marginLeft: 'auto', whiteSpace: 'nowrap', flex: 'none' }}>{time}</span>}
        </div>
      </div>
      {actions && <div className="rp-task-row__acts">{actions}</div>}
    </li>
  )
}
