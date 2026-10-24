import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
import TimelinePanel from './TimelinePanel'

const nodes = [
  { date: '2026-05-09', n: 3 },
  { date: '2026-04-20', n: 7 },
  { date: '2026-01-17', n: 1 },
]

describe('TimelinePanel (vertical / desktop)', () => {
  it('renders a bounded scroll container so a long timeline does not grow the page', () => {
    render(<TimelinePanel nodes={nodes} selected="2026-05-09" onSelect={() => {}} horizontal={false} />)
    const scroll = screen.getByTestId('timeline-scroll')
    expect(scroll.style.overflowY).toBe('auto')
    expect(scroll.style.maxHeight).not.toBe('')
  })

  it('shows every date and fires onSelect when one is clicked', () => {
    const onSelect = vi.fn()
    render(<TimelinePanel nodes={nodes} selected="2026-05-09" onSelect={onSelect} horizontal={false} />)
    nodes.forEach((n) => expect(screen.getByText(n.date)).toBeTruthy())
    fireEvent.click(screen.getByText('2026-04-20'))
    expect(onSelect).toHaveBeenCalledWith('2026-04-20')
  })
})

describe('TimelinePanel (horizontal / mobile)', () => {
  it('renders a horizontally scrollable strip and fires onSelect on tap', () => {
    const onSelect = vi.fn()
    render(<TimelinePanel nodes={nodes} selected="2026-05-09" onSelect={onSelect} horizontal />)
    const scroll = screen.getByTestId('timeline-hscroll')
    expect(scroll.style.overflowX).toBe('auto')
    fireEvent.click(screen.getByText('2026-01-17'))
    expect(onSelect).toHaveBeenCalledWith('2026-01-17')
  })

  it('gives a forecast dot an accessible label', () => {
    render(<TimelinePanel nodes={[{ date: '2026-05-09', n: 3, predicted: true }]} selected="2026-05-09" onSelect={() => {}} horizontal />)
    expect(screen.getByRole('button').getAttribute('aria-label')).toContain('forecast.timeline.predicted')
  })
})

describe('TimelinePanel forecast marks', () => {
  const predicted = [
    { date: '2026-05-09', n: 3, predicted: true },
    { date: '2026-04-20', n: 7, predicted: true },
  ]
  const dot = (chip: HTMLElement) => chip.querySelector('span[aria-hidden="true"]') as HTMLElement

  it('draws the forecast dot in a colour that contrasts with the selected chip', () => {
    render(<TimelinePanel nodes={predicted} selected="2026-05-09" onSelect={() => {}} horizontal />)
    const selected = screen.getByRole('button', { name: /2026-05-09/ })
    // The selected chip is filled with the primary colour; a primary dot on it would be invisible.
    expect(dot(selected).textContent).toBe('●')
    expect(dot(selected).style.color).not.toBe('')
    expect(dot(selected).style.color).not.toBe(selected.style.backgroundColor)
    // The dot reads like the chip's own text, which is what is legible on that fill.
    expect(dot(selected).style.color).toBe(selected.style.color)
  })

  it('keeps the primary dot on an unselected chip', () => {
    render(<TimelinePanel nodes={predicted} selected="2026-05-09" onSelect={() => {}} horizontal />)
    const selected = screen.getByRole('button', { name: /2026-05-09/ })
    const other = screen.getByRole('button', { name: /2026-04-20/ })
    expect(dot(other).style.color).not.toBe(other.style.backgroundColor)
    expect(dot(other).style.color).not.toBe(dot(selected).style.color)
  })

  it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('has no axe violations for the marks in each layout and theme', async (algorithm) => {
    const { container } = render(
      <ConfigProvider theme={{ algorithm }}>
        <TimelinePanel nodes={predicted} selected="2026-05-09" onSelect={() => {}} horizontal />
        <TimelinePanel nodes={predicted} selected="2026-04-20" onSelect={() => {}} horizontal={false} />
      </ConfigProvider>,
    )
    // jsdom cannot measure layout or contrast; the palette suite measures contrast separately.
    const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 120)).join(' | ')}`)).toEqual([])
  })
})
