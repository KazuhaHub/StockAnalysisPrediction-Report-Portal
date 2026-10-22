/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { theme } from 'antd'
import { readdirSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { forecastPalette, formatProbability, formatSigned } from './forecastPresentation'

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

  it('preserves zero and missing values while formatting signed figures and probabilities', () => {
    expect(formatSigned(undefined, 1)).toBe('—')
    expect(formatSigned(0, 1)).toBe('0.0')
    expect(formatSigned(3.1, 1)).toBe('+3.1')
    expect(formatSigned(-3.1, 1)).toBe('-3.1')
    expect(formatProbability(0)).toBe('<0.001')
    expect(formatProbability(0.041)).toBe('0.041')
    expect(formatProbability(undefined)).toBe('—')
  })

  it('keeps forecast components free of literal colours in both reader and admin locations', () => {
    const dirs = ['src/components/forecast', 'src/pages/manage']
    const files = dirs.flatMap((dir) => readdirSync(resolve(dir))
      .filter((name) => name.endsWith('.tsx') && !name.endsWith('.test.tsx') && (dir.includes('components') || name.startsWith('Forecast')))
      .map((name) => resolve(dir, name)))
    files.push(resolve('src/components/PredictionPanel.tsx'))
    expect(files.length).toBeGreaterThan(0)
    for (const file of files) expect(readFileSync(file, 'utf8'), file).not.toMatch(/#[\da-f]{3,8}\b|rgba?\s*\(/i)
  })
})
