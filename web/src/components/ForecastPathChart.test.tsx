import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ConfigProvider, theme } from 'antd'
import type { ForecastPath } from '../lib/forecastPath'
import ForecastPathChart from './ForecastPathChart'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, values?: Record<string, string>) => (values ? `${k} ${JSON.stringify(values)}` : k),
  }),
}))

const bar = (d: string, c: number) => ({ d, o: c, h: c, l: c, c, v: 1 })

const path: ForecastPath = {
  from: '2026-09-18',
  to: '2027-09-17',
  end: '2027-09-17',
  s0: 120000,
  scenarios: [
    { label: 'bear', fen: 95000 },
    { label: 'base', fen: 125000 },
    { label: 'bull', fen: 150000 },
  ],
  terminal: 130000,
  actual: [bar('2026-09-18', 120100), bar('2026-09-25', 123300), bar('2026-10-23', 118900)],
  checkpoints: [
    { checkpoint: 'd5', d: '2026-09-25', fen: 123400, direction: 'agree' },
    { checkpoint: 'd20', d: '2026-10-23', fen: 118800, direction: 'disagree' },
  ],
  today: '2026-10-24',
  min: 90000,
  max: 155000,
}

const num = (el: Element, attr: string) => Number(el.getAttribute(attr))

describe('ForecastPathChart', () => {
  it('is an image with a summary a screen reader can read', () => {
    render(<ForecastPathChart path={path} />)
    const img = screen.getByRole('img')
    expect(img.getAttribute('aria-label')).toContain('forecast.path.aria')
    expect(img.getAttribute('aria-label')).toContain('2026-09-18')
    expect(img.getAttribute('aria-label')).toContain('2027-09-17')
    expect(img.getAttribute('aria-label')).toContain('1,200.00')
  })

  it('runs every scenario line from the starting close to its horizon price', () => {
    render(<ForecastPathChart path={path} />)
    const lines = screen.getAllByTestId('forecast-path-scenario')
    expect(lines.map((l) => l.getAttribute('data-label'))).toEqual(['bear', 'base', 'bull'])
    const starts = new Set(lines.map((l) => `${l.getAttribute('x1')},${l.getAttribute('y1')}`))
    expect(starts.size).toBe(1)
    const [bear, base, bull] = lines
    // Higher prices sit higher on screen, so a smaller y.
    expect(num(bull, 'y2')).toBeLessThan(num(base, 'y2'))
    expect(num(base, 'y2')).toBeLessThan(num(bear, 'y2'))
    for (const l of lines) expect(num(l, 'x2')).toBeGreaterThan(num(l, 'x1'))
    expect(new Set(lines.map((l) => l.getAttribute('x2'))).size).toBe(1)
  })

  it('draws the expected terminal value as a dashed line from the same start', () => {
    render(<ForecastPathChart path={path} />)
    const terminal = screen.getByTestId('forecast-path-terminal')
    const bull = screen.getAllByTestId('forecast-path-scenario')[2]
    expect(terminal.getAttribute('stroke-dasharray')).toBeTruthy()
    expect(terminal.getAttribute('x1')).toBe(bull.getAttribute('x1'))
    expect(terminal.getAttribute('y1')).toBe(bull.getAttribute('y1'))
  })

  it('draws the actual closes as one line, left to right from the start date', () => {
    render(<ForecastPathChart path={path} />)
    const actual = screen.getByTestId('forecast-path-actual')
    const points = (actual.getAttribute('points') || '').trim().split(/\s+/).map((p) => p.split(',').map(Number))
    expect(points).toHaveLength(3)
    expect(points[0][0]).toBe(num(screen.getAllByTestId('forecast-path-scenario')[0], 'x1'))
    expect(points[1][0]).toBeGreaterThan(points[0][0])
    expect(points[2][0]).toBeGreaterThan(points[1][0])
  })

  it('labels round prices on a scale beside the lines', () => {
    render(<ForecastPathChart path={path} />)
    expect(screen.getAllByTestId('forecast-path-tick').map((el) => el.textContent)).toEqual(['1,000.00', '1,200.00', '1,400.00'])
  })

  it('keeps a checkpoint settled after the horizon inside the frame, with the scenarios ending at the horizon', () => {
    const late: ForecastPath = {
      ...path,
      end: '2027-09-27',
      actual: [...path.actual, bar('2027-09-27', 131000)],
      checkpoints: [...path.checkpoints, { checkpoint: 'h12', d: '2027-09-27', fen: 131000, direction: 'agree' }],
      today: undefined,
    }
    render(<ForecastPathChart path={late} />)
    const right = 600 - 6
    const xs = [
      ...screen.getAllByTestId('forecast-path-checkpoint').map((c) => num(c, 'cx')),
      ...(screen.getByTestId('forecast-path-actual').getAttribute('points') || '').trim().split(/\s+/).map((p) => Number(p.split(',')[0])),
    ]
    for (const x of xs) expect(x).toBeLessThanOrEqual(right)
    expect(Math.max(...xs)).toBe(right)
    const scenarioEnd = num(screen.getAllByTestId('forecast-path-scenario')[0], 'x2')
    expect(scenarioEnd).toBeLessThan(right)
  })

  it('draws no actual line until there are two closes to join', () => {
    render(<ForecastPathChart path={{ ...path, actual: path.actual.slice(0, 1) }} />)
    expect(screen.queryByTestId('forecast-path-actual')).toBeNull()
  })

  it('marks each settled checkpoint with its outcome and a readable title', () => {
    render(<ForecastPathChart path={path} />)
    const dots = screen.getAllByTestId('forecast-path-checkpoint')
    expect(dots.map((d) => d.getAttribute('data-direction'))).toEqual(['agree', 'disagree'])
    expect(dots[0].querySelector('title')?.textContent).toBe('forecast.cp.d5 · 2026-09-25 · 1,234.00 · forecast.direction.agree')
  })

  it('shows where today is, and only when today is inside the window', () => {
    const { unmount } = render(<ForecastPathChart path={path} />)
    expect(screen.getByTestId('forecast-path-today')).toBeTruthy()
    unmount()
    render(<ForecastPathChart path={{ ...path, today: undefined }} />)
    expect(screen.queryByTestId('forecast-path-today')).toBeNull()
  })

  it('names every line in the legend with its price, and says the scenario lines are reference paths', () => {
    render(<ForecastPathChart path={path} />)
    const legend = screen.getByTestId('forecast-path-legend').textContent || ''
    expect(legend).toContain('forecast.path.actual')
    expect(legend).toContain('forecast.panel.bull 1,500.00')
    expect(legend).toContain('forecast.panel.base 1,250.00')
    expect(legend).toContain('forecast.panel.bear 950.00')
    expect(legend).toContain('forecast.panel.terminal 1,300.00')
    expect(screen.getByText('forecast.path.note')).toBeTruthy()
  })

  it('tells a flat checkpoint from one that was not judged', () => {
    render(<ForecastPathChart path={{ ...path, checkpoints: [
      { checkpoint: 'd5', d: '2026-09-25', fen: 123400, direction: 'flat' },
      { checkpoint: 'd20', d: '2026-10-23', fen: 118800, direction: 'untested' },
    ] }} />)
    const [flat, untested] = screen.getAllByTestId('forecast-path-checkpoint')
    expect(`${flat.getAttribute('fill')} ${flat.getAttribute('stroke')}`).not.toBe(`${untested.getAttribute('fill')} ${untested.getAttribute('stroke')}`)
  })

  it.each([
    ['light', theme.defaultAlgorithm],
    ['dark', theme.darkAlgorithm],
  ] as const)('draws every checkpoint outcome at 3:1 or better against the card, in the %s theme', (_, algorithm) => {
    const t = theme.getDesignToken({ algorithm })
    render(
      <ConfigProvider theme={{ algorithm }}>
        <ForecastPathChart path={{ ...path, checkpoints: (['agree', 'disagree', 'flat', 'untested'] as const).map((direction, i) => ({ checkpoint: (['d5', 'd20', 'd60', 'd120'] as const)[i], d: '2026-10-01', fen: 120000, direction })) }} />
      </ConfigProvider>,
    )
    const parse = (c: string): number[] => {
      const fn = c.match(/^rgba?\(([^)]+)\)$/)
      if (fn) {
        const [r, g, b, a = 1] = fn[1].split(',').map((v) => Number(v.trim()))
        return [r, g, b, a]
      }
      const h = c.replace('#', '')
      return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16), 1]
    }
    const over = (f: number[], b: number[]) => [0, 1, 2].map((i) => f[i] * f[3] + b[i] * (1 - f[3]))
    const lum = (rgb: number[]) => {
      const ch = (v: number) => {
        const x = v / 255
        return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4
      }
      return 0.2126 * ch(rgb[0]) + 0.7152 * ch(rgb[1]) + 0.0722 * ch(rgb[2])
    }
    const contrast = (a: number[], b: number[]) => {
      const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
      return (hi + 0.05) / (lo + 0.05)
    }
    const bg = over(parse(t.colorBgContainer), [0, 0, 0])
    for (const dot of screen.getAllByTestId('forecast-path-checkpoint')) {
      // The dot is seen by whichever of its fill and its ring stands out more.
      const best = Math.max(...['fill', 'stroke'].map((a) => contrast(over(parse(dot.getAttribute(a) || ''), bg), bg)))
      expect(best, dot.getAttribute('data-direction') || '').toBeGreaterThanOrEqual(3)
    }
  })

  it.each([
    ['light', theme.defaultAlgorithm],
    ['dark', theme.darkAlgorithm],
  ] as const)('colours the scenarios like the band and the actual line as text, in the %s theme', (_, algorithm) => {
    const t = theme.getDesignToken({ algorithm })
    render(
      <ConfigProvider theme={{ algorithm }}>
        <ForecastPathChart path={path} />
      </ConfigProvider>,
    )
    const stroke = (label: string) => screen.getAllByTestId('forecast-path-scenario').find((l) => l.getAttribute('data-label') === label)?.getAttribute('stroke')
    expect(stroke('bear')).toBe(t.colorSuccess)
    expect(stroke('base')).toBe(t.colorTextTertiary)
    expect(stroke('bull')).toBe(t.colorError)
    expect(screen.getByTestId('forecast-path-actual').getAttribute('stroke')).toBe(t.colorText)
  })
})
