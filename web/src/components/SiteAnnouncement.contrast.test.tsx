import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { ConfigProvider, theme } from 'antd'
import { AnnouncementRow } from './SiteAnnouncement'
import type { AnnouncementLevel } from '../api/types'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

afterEach(cleanup)

// The same theme the app mounts (App.tsx), so the colours measured are the colours a reader gets.
const APP_TOKEN = { colorPrimary: '#1677ff', borderRadius: 8 }

type RGBA = [number, number, number, number]

function parseColour(c: string): RGBA {
  const fn = c.match(/^rgba?\(([^)]+)\)$/)
  if (fn) {
    const [r, g, b, a = 1] = fn[1].split(',').map((v) => Number(v.trim()))
    return [r, g, b, a]
  }
  const hex = c.replace('#', '')
  const full = hex.length === 3 ? hex.split('').map((x) => x + x).join('') : hex
  const alpha = full.length === 8 ? parseInt(full.slice(6, 8), 16) / 255 : 1
  return [parseInt(full.slice(0, 2), 16), parseInt(full.slice(2, 4), 16), parseInt(full.slice(4, 6), 16), alpha]
}

function over(fg: RGBA, bg: RGBA): RGBA {
  const a = fg[3]
  return [fg[0] * a + bg[0] * (1 - a), fg[1] * a + bg[1] * (1 - a), fg[2] * a + bg[2] * (1 - a), 1]
}

function luminance([r, g, b]: RGBA): number {
  const ch = (v: number) => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
  }
  return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b)
}

function contrast(a: RGBA, b: RGBA): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

const LEVELS: AnnouncementLevel[] = ['notice', 'success', 'warning', 'error']

describe('announcement body text contrast', () => {
  for (const [name, algorithm] of [
    ['light', theme.defaultAlgorithm],
    ['dark', theme.darkAlgorithm],
  ] as const) {
    for (const level of LEVELS) {
      it(`meets WCAG AA (4.5:1) for a ${level} announcement in the ${name} theme`, () => {
        const page = parseColour(theme.getDesignToken({ algorithm, token: APP_TOKEN }).colorBgLayout)
        render(
          <ConfigProvider theme={{ algorithm, token: APP_TOKEN }}>
            <AnnouncementRow announcement={{ level, title: 'Title', content: 'Body text', stripDismissible: false }} />
          </ConfigProvider>,
        )
        const body = screen.getByText('Body text')
        const row = body.closest('.rp-announce-item') as HTMLElement
        const bg = over(parseColour(row.style.background), page)
        const fg = over(parseColour(body.style.color), bg)
        expect(contrast(fg, bg)).toBeGreaterThanOrEqual(4.5)
      })
    }
  }
})
