import { useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import Markdown from './Markdown'

// Parsing a report body (remark, GFM tables, KaTeX) is the most expensive thing the reading page
// does: hundreds of milliseconds for a long report on a mid-range laptop. The reading page
// re-renders for reasons that have nothing to do with the body — a quote refresh, the forecast
// panel arriving, a loading flag — and each of those used to parse the same body again (four
// times per tab switch, measured). A body is parsed when it changes, and only then.
const parses = vi.hoisted(() => ({ n: 0 }))
vi.mock('react-markdown', () => ({
  default: ({ children }: { children: string }) => {
    parses.n += 1
    return <div data-testid="parsed">{children}</div>
  },
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

function Host({ md }: { md: string }) {
  const [n, setN] = useState(0)
  return (
    <>
      <button onClick={() => setN(n + 1)}>unrelated {n}</button>
      <Markdown md={md} />
    </>
  )
}

describe('Markdown', () => {
  it('does not parse the same body again when its parent re-renders', () => {
    parses.n = 0
    const { rerender } = render(<Host md="# a" />)
    expect(parses.n).toBe(1)

    fireEvent.click(screen.getByText('unrelated 0'))
    fireEvent.click(screen.getByText('unrelated 1'))
    rerender(<Host md="# a" />)
    expect(parses.n).toBe(1)

    rerender(<Host md="# b" />)
    expect(parses.n).toBe(2)
    expect(screen.getByTestId('parsed').textContent).toBe('# b')
  })
})
