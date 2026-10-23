import type { CSSProperties, ReactNode } from 'react'
import { Typography, theme } from 'antd'

// Secondary text that stays readable. antd's `type="secondary"` is 45% black, which measures 3.35:1
// on white and 4.52:1 on the dark surface; for the labels, notes and the disclaimer on this page that
// is too faint to rely on. The secondary text colour (65%) measures 6.98:1 and 8.19:1.
export default function Quiet({ id, style, children }: { id?: string; style?: CSSProperties; children: ReactNode }) {
  const { token } = theme.useToken()
  return (
    <Typography.Text id={id} style={{ color: token.colorTextSecondary, ...style }}>
      {children}
    </Typography.Text>
  )
}
