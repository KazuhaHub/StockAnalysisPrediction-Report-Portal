import { describe, expect, it } from 'vitest'
import { theme } from 'antd'
import { portalTheme } from './portal'

type RGBA = [number, number, number, number]

function parseColour(colour: string): RGBA {
  const rgb = colour.match(/^rgba?\(([^)]+)\)$/)
  if (rgb) {
    const [r, g, b, a = 1] = rgb[1].split(',').map((value) => Number(value.trim()))
    return [r, g, b, a]
  }
  const hex = colour.replace('#', '')
  const full = hex.length === 3 ? hex.split('').map((value) => value + value).join('') : hex
  const alpha = full.length === 8 ? parseInt(full.slice(6, 8), 16) / 255 : 1
  return [parseInt(full.slice(0, 2), 16), parseInt(full.slice(2, 4), 16), parseInt(full.slice(4, 6), 16), alpha]
}

function over(foreground: RGBA, background: RGBA): RGBA {
  const alpha = foreground[3]
  return [
    foreground[0] * alpha + background[0] * (1 - alpha),
    foreground[1] * alpha + background[1] * (1 - alpha),
    foreground[2] * alpha + background[2] * (1 - alpha),
    1,
  ]
}

function luminance([r, g, b]: RGBA): number {
  const channel = (value: number) => {
    const scaled = value / 255
    return scaled <= 0.03928 ? scaled / 12.92 : Math.pow((scaled + 0.055) / 1.055, 2.4)
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

function contrast(foreground: RGBA, background: RGBA): number {
  const [lighter, darker] = [luminance(foreground), luminance(background)].sort((a, b) => b - a)
  return (lighter + 0.05) / (darker + 0.05)
}

describe.each([
  ['light', false],
  ['dark', true],
] as const)('%s portal text tokens', (_name, dark) => {
  it.each(['colorTextSecondary', 'colorTextDescription', 'colorTextTertiary', 'colorTextPlaceholder'] as const)(
    '%s meets WCAG AA against the container',
    (name) => {
      const token = theme.getDesignToken(portalTheme(dark))
      const container = parseColour(token.colorBgContainer)
      const background = over(
        parseColour(name === 'colorTextPlaceholder' ? token.colorBgContainerDisabled : token.colorBgContainer),
        container,
      )
      const foreground = over(parseColour(token[name]), background)
      expect(contrast(foreground, background)).toBeGreaterThanOrEqual(4.5)
    },
  )
})
