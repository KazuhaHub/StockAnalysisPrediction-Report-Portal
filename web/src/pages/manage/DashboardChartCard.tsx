import { createContext, useState } from 'react'
import { Card, Space, type CardProps } from 'antd'

export const ChartHeaderTarget = createContext<HTMLElement | null>(null)

/** Charts place their actions in the owning card, including metric charts in summary cards. */
export default function DashboardChartCard({ extra, children, ...props }: CardProps) {
  const [target, setTarget] = useState<HTMLSpanElement | null>(null)
  return <ChartHeaderTarget.Provider value={target}>
    <Card {...props} extra={<Space size={8}>{extra}<span ref={setTarget} className="rp-dashboard-chart-header-action" /></Space>}>
      {children}
    </Card>
  </ChartHeaderTarget.Provider>
}
