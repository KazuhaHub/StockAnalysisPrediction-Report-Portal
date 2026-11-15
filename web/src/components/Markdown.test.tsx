import { act, fireEvent, render, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Markdown from './Markdown'

const { renderMermaid, parseMermaid, initializeMermaid, loadMermaid, cacheMermaidSVG } = vi.hoisted(() => {
  const renderMermaid = vi.fn(async (id: string, _source = '') => ({ svg: `<svg data-chart-id="${id}"><text>chart</text></svg>` }))
  const parseMermaid = vi.fn(async () => true)
  const initializeMermaid = vi.fn()
  const loadMermaid = vi.fn(async () => ({
    default: {
      initialize: initializeMermaid,
      parse: parseMermaid,
      render: renderMermaid,
      // The real detector throws for source it cannot classify; this one knows the two kinds the tests use.
      detectType: (source: string) => (source.trimStart().startsWith('mindmap') ? 'mindmap' : 'flowchart-v2'),
    },
  }))
  const cacheMermaidSVG = vi.fn(async () => undefined)
  return { renderMermaid, parseMermaid, initializeMermaid, loadMermaid, cacheMermaidSVG }
})

vi.mock('../lib/mermaid', () => ({
  loadMermaid,
}))

vi.mock('../lib/mermaidCache', () => ({
  cacheMermaidSVG,
}))

vi.mock('../lib/flattenSvg', () => ({
  flattenMermaidSVG: () => '<svg><text>flattened</text></svg>',
}))

describe('Markdown', () => {
  // BEFORE each, not after. The cache write is a passive effect that runs a commit AFTER the SVG
  // lands, so a slow machine can leave it pending when the test ends — and unmounting during
  // testing-library's cleanup then flushes it, i.e. AFTER an afterEach clear. That call showed up
  // as the next test's first call, which is how CI failed with one test's chart source asserted
  // against another test's expectation. Clearing at the start of a test cannot be outrun.
  beforeEach(() => {
    document.documentElement.dataset.theme = 'light'
    renderMermaid.mockClear()
    parseMermaid.mockReset()
    parseMermaid.mockResolvedValue(true)
    initializeMermaid.mockClear()
    loadMermaid.mockClear()
    cacheMermaidSVG.mockClear()
  })

  it('renders report math blocks with KaTeX', () => {
    const md = String.raw`$$P_{基础} = \frac{4.7793}{4.7793 + 4.1289} = \frac{4.7793}{8.9082} = 53.65%$$`
    const { container } = render(<Markdown md={md} />)

    expect(container.querySelector('.katex-display')).not.toBeNull()
    expect(container.querySelector('math')).not.toBeNull()
    expect(container.textContent).toContain('基础')
    expect(container.querySelector('.katex-html')?.textContent).toContain('%')
  })

  it('sanitizes the HTML fallback: strips scripts and inline event handlers', () => {
    const html = '<p>ok</p><img src=x onerror="window.__xss=1"><script>window.__xss=1</script>'
    const { container } = render(<Markdown html={html} />)
    // benign content survives...
    expect(container.textContent).toContain('ok')
    // ...but the script tag and the onerror handler are removed.
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('img')?.getAttribute('onerror')).toBeNull()
    expect(container.innerHTML).not.toContain('onerror')
  })

  it('renders a mermaid fence as an SVG instead of source code', async () => {
    const { container } = render(<Markdown md={'```mermaid\nxychart-beta\n  line [1, 2]\n```'} />)

    await waitFor(() => expect(container.querySelector('svg')).not.toBeNull())
    expect(container.textContent).toContain('chart')
    expect(container.textContent).not.toContain('xychart-beta')
    expect(initializeMermaid).toHaveBeenCalledWith(
      expect.objectContaining({
        securityLevel: 'strict',
        htmlLabels: false,
        // Mermaid 12 defaults to the ELK layout, which the build does not ship (doc 167 section 8):
        // the portal asks for dagre explicitly, so ordinary diagrams never reach the ELK stub.
        layout: 'dagre',
        secure: expect.arrayContaining(['htmlLabels']),
      }),
    )
    // Waited for, not merely asserted: the SVG appearing is one commit EARLIER than the effect
    // that writes the cache, so checking straight after waitFor(svg) is a race the test loses on a
    // loaded machine.
    await waitFor(() =>
      expect(cacheMermaidSVG).toHaveBeenCalledWith(
        expect.stringContaining('xychart-beta'),
        '<svg><text>flattened</text></svg>',
        'light',
      ),
    )
  })

  it('lays a flowchart out with dagre but leaves a mind map on its own layout', async () => {
    const flow = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B\n```'} />)
    await waitFor(() => expect(flow.container.querySelector('.md-mermaid svg')).not.toBeNull())
    expect(initializeMermaid).toHaveBeenLastCalledWith(expect.objectContaining({ layout: 'dagre' }))
    flow.unmount()

    initializeMermaid.mockClear()
    const mind = render(<Markdown md={'```mermaid\nmindmap\n  root\n    a\n```'} />)
    await waitFor(() => expect(mind.container.querySelector('.md-mermaid svg')).not.toBeNull())
    expect(initializeMermaid).toHaveBeenCalled()
    expect(initializeMermaid.mock.calls[0][0]).not.toHaveProperty('layout')
  })

  // mermaid.initialize is global and synchronous, while parse and render are asynchronous: with a
  // flowchart and a mind map on one page, the last initialize used to win for both renders, so either
  // the flowchart reached the ELK layout (absent from the build, a blank chart) or the mind map was
  // forced onto dagre. Each block's configuration must be in force for its own render.
  describe('two diagrams on one page', () => {
    const FLOW = '```mermaid\nflowchart LR\nA --> B\n```'
    const MIND = '```mermaid\nmindmap\n  root\n    a\n```'
    const defaultRender = async (id: string) => ({ svg: `<svg data-chart-id="${id}"><text>chart</text></svg>` })
    const tick = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

    async function renderBoth(md: string) {
      let config: { layout?: string } = {}
      const seen: Record<string, { layout?: string; atEnd?: string }> = {}
      initializeMermaid.mockImplementation((next: { layout?: string }) => {
        config = next
      })
      parseMermaid.mockImplementation(async () => {
        await tick(3)
        return true
      })
      renderMermaid.mockImplementation(async (id: string, source: string) => {
        const kind = source.startsWith('mindmap') ? 'mindmap' : 'flowchart'
        seen[kind] = { layout: config.layout }
        await tick(8)
        seen[kind].atEnd = config.layout
        return { svg: `<svg data-chart-id="${id}"><text>chart</text></svg>` }
      })
      try {
        const { container } = render(<Markdown md={md} />)
        await waitFor(() => expect(container.querySelectorAll('.md-mermaid svg')).toHaveLength(2), { timeout: 4000 })
      } finally {
        initializeMermaid.mockReset()
        parseMermaid.mockReset()
        parseMermaid.mockResolvedValue(true)
        renderMermaid.mockImplementation(defaultRender)
      }
      return seen
    }

    it('gives each its own layout when the flowchart comes first', async () => {
      const seen = await renderBoth(`${FLOW}\n\n${MIND}`)
      expect(seen.flowchart).toEqual({ layout: 'dagre', atEnd: 'dagre' })
      expect(seen.mindmap).toEqual({ layout: undefined, atEnd: undefined })
    })

    it('gives each its own layout when the mind map comes first', async () => {
      const seen = await renderBoth(`${MIND}\n\n${FLOW}`)
      expect(seen.flowchart).toEqual({ layout: 'dagre', atEnd: 'dagre' })
      expect(seen.mindmap).toEqual({ layout: undefined, atEnd: undefined })
    })
  })

  it('uses parser-normalized source as the cache key for an indented mermaid fence', async () => {
    render(<Markdown md={'  ```mermaid\n  flowchart LR\n  A --> B\n  ```'} />)

    // Waits for THIS call, not for any call: "was called at all" is satisfied by a leftover from
    // another test, which is exactly what made the failure read as a wrong cache key.
    await waitFor(() =>
      expect(cacheMermaidSVG).toHaveBeenCalledWith('flowchart LR\nA --> B', expect.any(String), 'light'),
    )
  })

  it('zooms, pans, and resets a rendered mermaid chart', async () => {
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B\n```'} />)

    await waitFor(() => expect(container.querySelector('.md-mermaid svg')).not.toBeNull())
    const viewport = container.querySelector<HTMLElement>('.md-mermaid-viewport')!
    const canvas = container.querySelector<HTMLElement>('.md-mermaid-canvas')!
    const zoomIn = container.querySelector<HTMLButtonElement>('.md-mermaid-zoom-in')!
    const reset = container.querySelector<HTMLButtonElement>('.md-mermaid-reset')!

    fireEvent.click(zoomIn)
    expect(canvas.style.transform).toBe('translate(0px, 0px) scale(1.25)')

    fireEvent.pointerDown(viewport, { pointerId: 1, button: 0, clientX: 10, clientY: 20 })
    fireEvent.pointerMove(viewport, { pointerId: 1, clientX: 50, clientY: 80 })
    fireEvent.pointerUp(viewport, { pointerId: 1 })
    expect(canvas.style.transform).toBe('translate(40px, 60px) scale(1.25)')

    fireEvent.click(reset)
    expect(canvas.style.transform).toBe('translate(0px, 0px) scale(1)')

    fireEvent.wheel(viewport, { deltaY: -100, clientX: 0, clientY: 0 })
    expect(canvas.style.transform).toBe('translate(0px, 0px) scale(1.1)')
  })

  it('pinch-zooms a rendered mermaid chart on touch devices', async () => {
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B\n```'} />)

    await waitFor(() => expect(container.querySelector('.md-mermaid svg')).not.toBeNull())
    const viewport = container.querySelector<HTMLElement>('.md-mermaid-viewport')!
    const canvas = container.querySelector<HTMLElement>('.md-mermaid-canvas')!

    fireEvent.pointerDown(viewport, { pointerId: 1, pointerType: 'touch', button: 0, clientX: 0, clientY: 0 })
    fireEvent.pointerDown(viewport, { pointerId: 2, pointerType: 'touch', button: 0, clientX: 100, clientY: 0 })
    fireEvent.pointerMove(viewport, { pointerId: 2, pointerType: 'touch', clientX: 200, clientY: 0 })

    expect(canvas.style.transform).toBe('translate(0px, 0px) scale(2)')
  })

  it('exposes wide tables as keyboard-scrollable regions', () => {
    const { container } = render(<Markdown md={'| A | B | C |\n|---|---|---|\n| 1 | 2 | 3 |'} />)

    const wrap = container.querySelector<HTMLElement>('.md-table-wrap')!
    expect(wrap.getAttribute('role')).toBe('region')
    expect(wrap.getAttribute('tabindex')).toBe('0')
    expect(wrap.querySelector('table')).not.toBeNull()
  })

  it('renders ordinary code fences unchanged', () => {
    const { container } = render(<Markdown md={'```typescript\nconst answer = 42\n```'} />)

    expect(container.querySelector('code.language-typescript')?.textContent).toContain('const answer = 42')
    expect(loadMermaid).not.toHaveBeenCalled()
    expect(renderMermaid).not.toHaveBeenCalled()
  })

  it('does not load mermaid until a streaming fence is closed', () => {
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B'} />)

    expect(container.querySelector('svg')).toBeNull()
    expect(container.querySelector('code.language-mermaid')?.textContent).toContain('flowchart LR')
    expect(loadMermaid).not.toHaveBeenCalled()
    expect(parseMermaid).not.toHaveBeenCalled()
  })

  it('keeps invalid mermaid source as a code block', async () => {
    parseMermaid.mockResolvedValueOnce(false)
    const { container } = render(<Markdown md={'```mermaid\nxychart-beta\n  line ['} />)

    expect(container.querySelector('svg')).toBeNull()
    expect(container.querySelector('code.language-mermaid')?.textContent).toContain('xychart-beta')
    expect(loadMermaid).not.toHaveBeenCalled()
    expect(parseMermaid).not.toHaveBeenCalled()
    expect(renderMermaid).not.toHaveBeenCalled()
  })

  it('keeps invalid source as code after its fence closes', async () => {
    parseMermaid.mockResolvedValueOnce(false)
    const { container } = render(<Markdown md={'```mermaid\nxychart-beta\n  line [\n```'} />)

    await waitFor(() => expect(parseMermaid).toHaveBeenCalled())
    expect(container.querySelector('svg')).toBeNull()
    expect(container.querySelector('code.language-mermaid')).not.toBeNull()
    expect(renderMermaid).not.toHaveBeenCalled()
  })

  it('does not load mermaid for an oversized fence', () => {
    const source = `flowchart LR\n${'A --> B\n'.repeat(7_000)}`
    const { container } = render(<Markdown md={`\`\`\`mermaid\n${source}\`\`\``} />)

    expect(container.querySelector('code.language-mermaid')).not.toBeNull()
    expect(loadMermaid).not.toHaveBeenCalled()
  })

  it('sanitizes the SVG returned by mermaid before inserting it', async () => {
    renderMermaid.mockResolvedValueOnce({
      svg: '<svg onload="alert(1)"><script>alert(1)</script><foreignObject>unsafe</foreignObject><text>safe</text></svg>',
    })
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B\n```'} />)

    await waitFor(() => expect(container.querySelector('.md-mermaid svg')).not.toBeNull())
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('foreignObject')).toBeNull()
    expect(container.querySelector('svg')?.getAttribute('onload')).toBeNull()
    expect(container.textContent).toContain('safe')
  })

  it('rerenders mermaid charts when the document theme changes', async () => {
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B\n```'} />)
    await waitFor(() => expect(renderMermaid).toHaveBeenCalledTimes(1))

    await act(async () => {
      document.documentElement.dataset.theme = 'dark'
      await Promise.resolve()
    })

    await waitFor(() => expect(renderMermaid).toHaveBeenCalledTimes(2))
    expect(container.querySelector('svg')).not.toBeNull()
  })
})
