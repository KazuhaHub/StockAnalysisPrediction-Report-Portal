/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { theme } from 'antd'
import { readdirSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { coveragePalette, forecastLinkColor, forecastPalette, forecastWarningText, formatProbability, formatSigned } from './forecastPresentation'

function luminance(hex: string) {
  const channels = hex.replace('#', '').match(/../g)!.map((v) => parseInt(v, 16) / 255)
    .map((v) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)
  return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722
}

describe('forecast presentation', () => {
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
