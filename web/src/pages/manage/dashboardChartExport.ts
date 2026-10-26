import { toCSV } from '../../lib/csv'

// A spreadsheet must treat external category names as text, including formula-like names.
function textCell(value: string) {
  return /^\s*[=+\-@]/u.test(value) || /^[\t\r\n]/u.test(value) ? `'${value}` : value
}
export function chartDataCSV(headers: string[], rows: {name: string; value: number}[], total?: number): string {
  return '\ufeff' + toCSV(headers, rows.map(row => [textCell(row.name), row.value,
    ...(total === undefined ? [] : [`${total ? (row.value / total * 100).toFixed(1) : '0.0'}%`]),
  ]))
}
