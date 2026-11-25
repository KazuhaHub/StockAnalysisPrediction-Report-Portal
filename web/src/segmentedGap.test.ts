import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, it } from 'vitest'

// Hovering the option beside the selected one used to paint a grey block flush against the thumb, so
// the two read as one pill (the stock page's report-type strip). One global rule keeps every
// segmented control's options apart.
const root = dirname(fileURLToPath(import.meta.url))
const rules = readFileSync(join(root, 'index.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')

it('spaces the options of every segmented control', () => {
  const m = rules.match(/(^|\})\s*\.ant-segmented \.ant-segmented-group\s*\{([^}]*)\}/)
  expect(m, 'global .ant-segmented .ant-segmented-group rule').toBeTruthy()
  const gap = m![2].match(/gap:\s*(\d+)px/)
  expect(gap).toBeTruthy()
  expect(Number(gap![1])).toBeGreaterThanOrEqual(2)
})
