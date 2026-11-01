import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Tabs that do not fit their row scroll, with a fade at the side that has more, everywhere: antd's
// own swipe and wheel scrolling, not its "…" overflow menu, which hid tabs behind a click (the SSO
// page's policy tabs). The settings workspace tab row does the same (SettingsWorkspace).
// Read from disk, as mobileSingleScroll.test.tsx does: a ?raw import of a stylesheet is empty under vitest.
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'index.css'), 'utf8')

const rules = css.replace(/\/\*[\s\S]*?\*\//g, '')

describe('tab rows that overflow', () => {
  it('never shows antd’s "…" overflow menu', () => {
    expect(rules).toMatch(/\.ant-tabs-nav-operations\s*\{[^}]*display:\s*none/)
  })

  it('fades the side that has more tabs', () => {
    expect(rules).toMatch(/\.ant-tabs-nav-wrap-ping-left[^{]*\{[^}]*mask-image/)
    expect(rules).toMatch(/\.ant-tabs-nav-wrap-ping-right[^{]*\{[^}]*mask-image/)
  })
})
