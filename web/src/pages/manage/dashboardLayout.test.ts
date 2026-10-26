import { expect, it } from 'vitest'
import { newDashboardInstanceID, dashboardBlockKind, dashboardBlockLabel, dashboardChartType, dashboardChartTypes, isDashboardChart } from './dashboardLayout'

it.each(['reports','queue','forecast','trend','kinds','report-bars','report-donut','shortcuts','directory','shortcut:types'])('resolves the %s source for repeated instances', kind => {
  const id = `${kind}~123e4567-e89b-42d3-a456-426614174000`
  expect(dashboardBlockKind(id)).toBe(kind)
  expect(dashboardBlockLabel(id)).toBe(dashboardBlockLabel(kind))
  expect(isDashboardChart(id)).toBe(isDashboardChart(kind))
  expect(dashboardChartType(id)).toBe(dashboardChartType(kind))
  expect(dashboardChartTypes(id)).toEqual(dashboardChartTypes(kind))
})

it('creates distinct instance identities accepted by the stored layout contract', () => {
  const ids = Array.from({length:20}, () => newDashboardInstanceID('queue'))
  expect(new Set(ids).size).toBe(20)
  for (const id of ids) expect(id).toMatch(/^queue~[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
})
