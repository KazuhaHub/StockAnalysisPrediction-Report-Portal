import type { ReactNode } from 'react'
import { Tag, theme } from 'antd'
import { forecastWarningText } from '../../lib/forecastPresentation'

// A warning-toned tag that can be read. antd's own warning tag measures 1.83:1 on its tint in the
// light theme; this one takes Orange 8 for its text, which clears AA in both themes.
export default function WarnTag({ children }: { children: ReactNode }) {
  const { token } = theme.useToken()
  return (
    <Tag
      style={{
        margin: 0,
        whiteSpace: 'normal',
        color: forecastWarningText(token),
        background: token.colorWarningBg,
        borderColor: token.colorWarningBorder,
      }}
    >
      {children}
    </Tag>
  )
}
