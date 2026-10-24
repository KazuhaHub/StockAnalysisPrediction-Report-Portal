/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { theme } from 'antd'
import { readdirSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { coveragePalette, forecastLinkColor, forecastPalette, forecastPressedPalette, forecastWarningText, formatProbability, formatSigned } from './forecastPresentation'

function luminance(hex: string) {
  const channels = hex.replace('#', '').match(/../g)!.map((v) => parseInt(v, 16) / 255)
    .map((v) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)
  return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722
}

// A colour token as [r, g, b] over a background: antd's text colours are translucent black or white.
function over(colour: string, background: string): [number, number, number] {
  const solid = (value: string): [number, number, number] =>
    value.startsWith('#')
      ? (value.replace('#', '').match(/../g)!.map((v) => parseInt(v, 16)) as [number, number, number])
      : (value.match(/[\d.]+/g)!.slice(0, 3).map(Number) as [number, number, number])
  const alpha = colour.startsWith('rgba') ? Number(colour.match(/[\d.]+/g)![3]) : 1
  const fg = solid(colour)
  const bg = solid(background)
  return [0, 1, 2].map((i) => fg[i] * alpha + bg[i] * (1 - alpha)) as [number, number, number]
}
const contrast = (a: [number, number, number], b: [number, number, number]) => {
  const lum = ([r, g, bl]: [number, number, number]) =>
    [r, g, bl].map((v) => v / 255).map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
      .reduce((sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i], 0)
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

describe('forecast presentation', () => {
  // The drawer's field names and the page's quiet notes use the secondary text colour. antd's default
  // for a description label is its tertiary colour, which measures 3.36:1 on white.
  it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('keeps the secondary text colour above AA contrast on the surface of each theme', (algorithm) => {
    const token = theme.getDesignToken({ algorithm, token: { colorPrimary: '#1677ff' } })
    for (const surface of [token.colorBgContainer, token.colorBgElevated]) {
      expect(contrast(over(token.colorTextSecondary, surface), over(surface, surface))).toBeGreaterThanOrEqual(4.5)
    }
    // And the colour this replaces really is under it, so the test would notice a regression to it.
    expect(contrast(over(token.colorTextTertiary, token.colorBgElevated), over(token.colorBgElevated, token.colorBgElevated))).toBeLessThan(4.5)
  })

  it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('keeps significant text above AA contrast on its actual tint', (algorithm) => {
    const token = theme.getDesignToken({ algorithm, token: { colorPrimary: '#1677ff' } })
    const palette = forecastPalette(token)
    for (const tone of [palette.positive, palette.negative]) {
      const a = luminance(tone.foreground)
      const b = luminance(tone.background)
      expect((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)).toBeGreaterThanOrEqual(4.5)
    }
  })

  it.each([
    [theme.defaultAlgorithm, '#ffffff'],
    [theme.darkAlgorithm, '#141414'],
  ])('keeps the page\'s own links above AA contrast on the surface of each theme', (algorithm, surface) => {
    const token = theme.getDesignToken({ algorithm, token: { colorPrimary: '#1677ff' } })
    const a = luminance(forecastLinkColor(token))
    const b = luminance(surface)
    expect((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)).toBeGreaterThanOrEqual(4.5)
  })

  // The quick range in force is drawn on antd's primary tint. The link colour clears AA on the plain
  // surfaces, but on the dark theme's tint it measured 3.62:1, so the label takes the body text colour
  // and the link colour is kept for the border and the check, which only have to reach 3:1 (WCAG 1.4.11).
  it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('keeps the selected quick range legible on its own tint in each theme', (algorithm) => {
    const token = theme.getDesignToken({ algorithm, token: { colorPrimary: '#1677ff' } })
    const pressed = forecastPressedPalette(token)
    const tint = over(pressed.background, token.colorBgContainer)
    expect(contrast(over(pressed.text, pressed.background), tint)).toBeGreaterThanOrEqual(4.5)
    expect(contrast(over(pressed.icon, pressed.background), tint)).toBeGreaterThanOrEqual(3)
    expect(contrast(over(pressed.border, token.colorBgContainer), over(token.colorBgContainer, token.colorBgContainer))).toBeGreaterThanOrEqual(3)
  })

  it('keeps the case it replaced failing: the link colour on the dark theme\'s tint is under AA', () => {
    const token = theme.getDesignToken({ algorithm: theme.darkAlgorithm, token: { colorPrimary: '#1677ff' } })
    const tint = over(token.colorPrimaryBg, token.colorBgContainer)
    expect(contrast(over(forecastLinkColor(token), token.colorPrimaryBg), tint)).toBeLessThan(4.5)
  })

  // The warning chip and the cap hint sit on a warning tint (light) or the dark surface's tint.
  it.each([
    [theme.defaultAlgorithm, '#fffbe6'],
    [theme.darkAlgorithm, '#2b2111'],
  ])('keeps warning text above AA contrast on the warning tint of each theme', (algorithm, tint) => {
    const token = theme.getDesignToken({ algorithm, token: { colorPrimary: '#1677ff' } })
    const a = luminance(forecastWarningText(token))
    const b = luminance(tint)
    expect((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)).toBeGreaterThanOrEqual(4.5)
  })

  // In the default theme antd's info colour IS the primary colour, so a bar that drew "counted" in the
  // primary and "repeat run" in the info colour drew them identically. The segments must differ in
  // both themes, including for a custom seed where info and primary coincide.
  it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('gives every coverage segment its own colour in each theme', (algorithm) => {
    const token = theme.getDesignToken({ algorithm, token: { colorPrimary: '#1677ff' } })
    expect(token.colorInfo).toBe(token.colorPrimary)
    const palette = coveragePalette(token)
    const colours = [palette.member, palette.duplicate, palette.missing, palette.other]
    expect(new Set(colours).size).toBe(colours.length)
    // Distinct strings are not distinct to the eye: the repeat-run segment sits next to the counted
    // one, and two steps of one blue measured 1.47:1 in the dark theme. It takes another hue, at least
    // 3:1 (WCAG 1.4.11) from the counted segment and from the surface it is drawn on.
    const surface = token.colorBgContainer
    const colourOf = (value: string) => over(value, surface)
    expect(contrast(colourOf(palette.member), colourOf(palette.duplicate))).toBeGreaterThanOrEqual(3)
    expect(contrast(colourOf(palette.duplicate), colourOf(surface))).toBeGreaterThanOrEqual(3)
    expect(contrast(colourOf(palette.member), colourOf(surface))).toBeGreaterThanOrEqual(3)
    // Its other neighbour is the missing segment: another hue, and not a near-twin in lightness either.
    expect(contrast(colourOf(palette.duplicate), colourOf(palette.missing))).toBeGreaterThanOrEqual(1.5)
    const blues = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10].map((step) => token[`blue${step}` as keyof typeof token])
    expect(blues).not.toContain(palette.duplicate)
    // The ledger-start segment is told apart by pattern as well as colour.
    expect(palette.preLive).toContain('repeating-linear-gradient')
    expect(palette.preLive).not.toContain(palette.other)
  })

  it('preserves zero and missing values while formatting signed figures and probabilities', () => {
    expect(formatSigned(undefined, 1)).toBe('—')
    expect(formatSigned(0, 1)).toBe('0.0')
    expect(formatSigned(3.1, 1)).toBe('+3.1')
    expect(formatSigned(-3.1, 1)).toBe('-3.1')
    expect(formatProbability(0)).toBe('<0.001')
    expect(formatProbability(0.041)).toBe('0.041')
    expect(formatProbability(undefined)).toBe('—')
  })

  it('keeps forecast components free of literal colours in the reader page and in both component locations', () => {
    const dirs = ['src/components/forecast', 'src/pages/manage']
    const files = dirs.flatMap((dir) => readdirSync(resolve(dir))
      .filter((name) => name.endsWith('.tsx') && !name.endsWith('.test.tsx') && (dir.includes('components') || name.startsWith('Forecast')))
      .map((name) => resolve(dir, name)))
    files.push(resolve('src/components/PredictionPanel.tsx'), resolve('src/pages/ForecastRecordPage.tsx'))
    expect(files.length).toBeGreaterThan(0)
    for (const file of files) expect(readFileSync(file, 'utf8'), file).not.toMatch(/#[\da-f]{3,8}\b|rgba?\s*\(/i)
  })
})
