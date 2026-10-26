import { expect, it } from 'vitest'
import { parseCSV } from '../../lib/csv'
import { chartDataCSV } from './dashboardChartExport'

it('exports every supplied row with UTF-8 BOM and accurate shares', () => {
  const rows = Array.from({length:365},(_,i) => ({name:`2026-${i}`,value:1}))
  const csv = chartDataCSV(['Date','Count','Share'],rows,365)
  expect(csv.startsWith('\ufeff')).toBe(true)
  const parsed = parseCSV(csv.slice(1))
  expect(parsed).toHaveLength(366)
  expect(parsed[365]).toEqual(['2026-364','1','0.3%'])
})
it('preserves external names as literal text and quotes delimiters', () => {
  const csv = chartDataCSV(['Category','Count'],[{name:' =SUM(1,2)',value:3},{name:'研究,"A"\r\nB',value:4}])
  expect(parseCSV(csv.slice(1)).slice(1)).toEqual([["' =SUM(1,2)",'3'],['研究,"A"\r\nB','4']])
})
