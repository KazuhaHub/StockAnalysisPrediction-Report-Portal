import { ADMIN_PAGES } from './adminNavigation'
export const dashboardBlockKind = (id: string) => id.split('~')[0]
export function newDashboardInstanceID(kind: string) {
  // getRandomValues also works for self-hosted HTTP origins without randomUUID support.
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6] & 15) | 64
  bytes[8] = (bytes[8] & 63) | 128
  const hex = Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
  return `${kind}~${hex.slice(0,8)}-${hex.slice(8,12)}-${hex.slice(12,16)}-${hex.slice(16,20)}-${hex.slice(20)}`
}
export interface DashboardBlock { display?: 'numbers' | 'bar' | 'column'; id: string; visible: boolean; width: 1 | 2 | 3 | 6; metrics?: string[]; chart?: DashboardChart }
export type DashboardChartType = 'line' | 'column' | 'bar' | 'donut'
export const dashboardChartTypes = (id: string): DashboardChartType[] => dashboardBlockKind(id) === 'trend' || dashboardBlockKind(id) === 'report-bars' ? ['line', 'column'] : ['donut', 'bar']
export const dashboardChartType = (id: string, chart?: DashboardChart): DashboardChartType => chart?.type ?? (dashboardBlockKind(id) === 'trend' ? 'line' : dashboardBlockKind(id) === 'report-bars' ? 'column' : 'donut')
export interface DashboardChart { type?: DashboardChartType; range: 7 | 30 | 90 | 365; interval: 'auto' | 'day' | 'week' | 'month'; showValues: boolean; cumulative: boolean; topN: 5 | 10 | 15 }
export const defaultDashboardChart = (): DashboardChart => ({ range: 7, interval: 'auto', showValues: true, cumulative: false, topN: 5 })
export const isDashboardChart = (id: string) => ['trend', 'kinds', 'report-bars', 'report-donut'].includes(dashboardBlockKind(id))
export interface DashboardLayout { blocks: DashboardBlock[]; revision: string }
export const DASHBOARD_BLOCKS: { id: string; label: string; width: 1 | 2 | 3 | 6; optional?: boolean }[] = [
  { id: 'reports', label: 'manage.dashboard.reports', width: 2 },
  { id: 'queue', label: 'queue.title', width: 2 },
  { id: 'forecast', label: 'nav.forecast', width: 2 },
  { id: 'trend', label: 'manage.dashboard.reportTrend', width: 3 },
  { id: 'kinds', label: 'manage.dashboard.reportKinds', width: 3 },
  { id: 'report-bars', label: 'manage.dashboard.reportBars', width: 3, optional: true },
  { id: 'report-donut', label: 'manage.dashboard.reportDonut', width: 3, optional: true },
  { id: 'shortcuts', label: 'manage.dashboard.shortcuts', width: 6 },
  { id: 'directory', label: 'manage.dashboard.directory', width: 6 },
]
export const defaultDashboardBlocks = (): DashboardBlock[] => DASHBOARD_BLOCKS.map(({ id, width, optional }) => ({ id, width, visible: !optional }))

export const dashboardCatalog = () => [
  ...DASHBOARD_BLOCKS,
  ...ADMIN_PAGES.map((page) => ({ id: `shortcut:${page.key}`, label: page.label, width: 1 as const })),
]
export const dashboardBlockLabel = (id: string) => dashboardCatalog().find((block) => block.id === dashboardBlockKind(id))?.label ?? 'manage.dashboard.unavailableBlock'

export const dashboardBlockCategory = (id: string) => ['reports','queue','forecast'].includes(id) ? 'metrics' : ['trend','kinds','report-bars','report-donut'].includes(id) ? 'charts' : 'features'
