import { useEffect, useId, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { Progress, Typography, theme } from 'antd'
import { useHoverNone } from '../lib/useHoverNone'

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
  /** The row's accessible name: the task and its status. */
  label?: string
  /** The row was clicked or tapped anywhere (an action excepted), or activated from the keyboard. */
  onOpen?: () => void
}

// One task in the Tasks drawer: exactly two lines. A status dot, then the name and a muted qualifier
// on the first line; a bar, queue position or result on the second. The time is flush with the row's
// right edge and shares one cell with the actions: hovering or focusing the row (where hover exists),
// slides the actions in over the time (see .rp-task-row__end); where nothing hovers they stay away, since
// the row itself opens the task's detail, which has them. Clicking or tapping the row anywhere opens it.
export default function TaskRow({ tone, name, sub, detail, percent, time, actions, pinned, label, onOpen }: TaskRowProps) {
  const { token } = theme.useToken()
  const uid = useId()
  const noHover = useHoverNone()
  // The whole row is the pointer target, the time cell included; the actions (and anything portalled out of
  // them, such as a confirmation popover, whose events still bubble through React) keep their own clicks.
  // A native listener, because the keyboard path is the row's main button: a bare onClick on this <li>
  // would be a second, mouse-only control.
  const rowRef = useRef<HTMLLIElement>(null)
  const openRef = useRef(onOpen)
  openRef.current = onOpen
  useEffect(() => {
    const row = rowRef.current
    if (!row) return
    const onClick = (e: MouseEvent) => {
      if ((e.target as Element).closest('.rp-task-row__acts')) return
      openRef.current?.()
    }
    row.addEventListener('click', onClick)
    return () => row.removeEventListener('click', onClick)
  }, [])
  const openKey = (e: KeyboardEvent) => {
    if (e.target !== e.currentTarget) return
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onOpen?.()
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
  const cls = ['rp-task-row', pinned && 'is-active'].filter(Boolean).join(' ')
  return (
    <li ref={rowRef} className={cls} style={{ listStyle: 'none' }}>
      <div
        className="rp-task-row__main"
        role="button"
        tabIndex={0}
        aria-label={label}
        aria-describedby={`${uid}-detail ${uid}-time`}
        onKeyDown={openKey}
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
          <div id={`${uid}-detail`} style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 2, fontSize: 12, color: token.colorTextSecondary, minWidth: 0 }}>
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
            <span id={`${uid}-time`} className="rp-task-row__time" style={{ color: token.colorTextSecondary }}>
              {time}
            </span>
          )}
          {actions && (
            <div className="rp-task-row__acts" inert={noHover && !pinned}>
              {actions}
            </div>
          )}
        </div>
      )}
    </li>
  )
}
