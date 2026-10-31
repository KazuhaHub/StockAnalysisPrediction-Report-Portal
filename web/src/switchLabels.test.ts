import { describe, expect, it } from 'vitest'

// Switches carry no text inside the track. The setting's label and hint beside it already say what it
// does, and words inside the knob's track ("Allowed" / "Not allowed") widen one switch past every other
// and read as a second, competing label.

// Vite's raw glob rather than node:fs, as in antdStaticApi.test.ts.
const sources = import.meta.glob('./**/*.{ts,tsx}', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

const productionSources = Object.entries(sources).filter(([p]) => !/\.test\.tsx?$/.test(p))

describe('switches have no inner text', () => {
  it('has source files to check', () => {
    expect(productionSources.length).toBeGreaterThan(50)
  })

  it('no Switch sets checkedChildren or unCheckedChildren', () => {
    const offenders = productionSources
      // As a JSX prop or an object key, so a comment naming the props does not trip it.
      .filter(([, src]) => /\b(checkedChildren|unCheckedChildren)\s*[=:]/.test(src))
      .map(([file]) => file)
    expect(offenders, 'label the setting beside the switch instead').toEqual([])
  })
})
