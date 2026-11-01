import type { CSSProperties, ReactNode } from 'react'
import { Tag } from 'antd'
import {
  CheckCircleFilled,
  ClockCircleOutlined,
  CloseCircleFilled,
  ExclamationCircleFilled,
  MinusCircleOutlined,
  SyncOutlined,
} from '@ant-design/icons'

// A status, said by colour AND icon AND words, never by colour alone: a colour-blind reader, a grey
// screenshot and a printed page all still tell a failure from a success [Carbon status indicators].
// Statuses map to a tone where they are defined (e.g. jobStatusTone in lib/batchUi); a guard test
// (statusTags.test.ts) keeps status colours on antd's plain Tag out of the pages.
export const STATUS_TONES = ['success', 'error', 'warning', 'processing', 'waiting', 'neutral'] as const
export type StatusTone = (typeof STATUS_TONES)[number]

const COLOR: Record<StatusTone, string> = {
  success: 'success',
  error: 'error',
  warning: 'warning',
  processing: 'processing',
  waiting: 'default',
  neutral: 'default',
}

const ICON: Record<StatusTone, ReactNode> = {
  success: <CheckCircleFilled />,
  error: <CloseCircleFilled />,
  warning: <ExclamationCircleFilled />,
  processing: <SyncOutlined spin />,
  waiting: <ClockCircleOutlined />,
  neutral: <MinusCircleOutlined />,
}

export default function StatusTag({
  tone,
  children,
  title,
  style,
}: {
  tone: StatusTone
  children: ReactNode
  title?: string
  style?: CSSProperties
}) {
  return (
    <Tag color={COLOR[tone]} icon={ICON[tone]} title={title} style={style}>
      {children}
    </Tag>
  )
}
