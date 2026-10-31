import { useState } from 'react'
import { afterEach, describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import SettingsWorkspace, { SettingsPanel } from './SettingsWorkspace'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

function Harness() {
  const [active, setActive] = useState('a')
  return (
    <SettingsWorkspace active={active} onChange={setActive} sections={[{ key: 'a', label: 'Alpha' }, { key: 'b', label: 'Beta' }]}>
      <SettingsPanel section="a"><input aria-label="draft-a" /></SettingsPanel>
      <SettingsPanel section="b"><input aria-label="draft-b" /></SettingsPanel>
    </SettingsWorkspace>
  )
}

describe('SettingsWorkspace', () => {
  it('renders a horizontal tab bar and no side column', () => {
    const { container } = render(<Harness />)
    expect(screen.getByRole('tablist')).toBeTruthy()
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Alpha', 'Beta'])
    expect(screen.getByRole('tab', { name: 'Alpha' }).getAttribute('aria-selected')).toBe('true')
    expect(container.querySelector('.rp-settings-sections')).toBeNull()
  })

  it('sets no inline fixed height', () => {
    const { container } = render(<Harness />)
    const workspace = container.querySelector('.rp-settings-workspace') as HTMLElement
    expect(workspace.style.height).toBe('')
  })

  it('keeps every panel mounted and preserves state across tab switches', () => {
    render(<Harness />)
    const a = document.querySelector('input[aria-label="draft-a"]') as HTMLInputElement
    fireEvent.change(a, { target: { value: 'typed' } })
    fireEvent.click(screen.getByRole('tab', { name: 'Beta' }))
    expect(screen.getByRole('tab', { name: 'Beta' }).getAttribute('aria-selected')).toBe('true')
    expect(document.querySelector('input[aria-label="draft-a"]')).toBe(a)
    expect(a.closest('section')!.hidden).toBe(true)
    expect(a.value).toBe('typed')
    fireEvent.click(screen.getByRole('tab', { name: 'Alpha' }))
    expect(a.closest('section')!.hidden).toBe(false)
  })

  it('labels the tablist and points every tab at the real content region', () => {
    render(<Harness />)
    expect(screen.getByRole('tablist', { name: 'manage.sections' })).toBeTruthy()
    const panel = screen.getByRole('tabpanel')
    for (const tab of screen.getAllByRole('tab')) expect(tab.getAttribute('aria-controls')).toBe(panel.id)
    expect(panel.getAttribute('aria-labelledby')).toBe(screen.getByRole('tab', { name: 'Alpha' }).id)
    expect(panel.querySelectorAll('section')).toHaveLength(2)
  })

  // The underline is one element that moves under the selected tab, so a switch slides it across
  // like antd's ink bar instead of jumping. jsdom lays nothing out, so the tabs' boxes are stubbed.
  describe('sliding underline', () => {
    const boxes: Record<string, { left: number; width: number }> = { Alpha: { left: 0, width: 60 }, Beta: { left: 64, width: 48 } }
    const stub = (prop: 'offsetLeft' | 'offsetWidth', pick: (b: { left: number; width: number }) => number) =>
      vi.spyOn(HTMLElement.prototype, prop, 'get').mockImplementation(function (this: HTMLElement) {
        const box = boxes[this.textContent ?? '']
        return this.getAttribute('role') === 'tab' && box ? pick(box) : 0
      })
    afterEach(() => {
      vi.restoreAllMocks()
      vi.unstubAllGlobals()
      boxes.Beta = { left: 64, width: 48 }
    })

    it('sits under the selected tab and follows it to the next one', () => {
      stub('offsetLeft', (b) => b.left)
      stub('offsetWidth', (b) => b.width)
      const { container } = render(<Harness />)
      const bar = container.querySelector('.rp-settings-tab-ink') as HTMLElement
      expect(bar.getAttribute('aria-hidden')).toBe('true')
      expect(bar.style.transform).toBe('translateX(0px)')
      expect(bar.style.width).toBe('60px')
      fireEvent.click(screen.getByRole('tab', { name: 'Beta' }))
      expect(bar.style.transform).toBe('translateX(64px)')
      expect(bar.style.width).toBe('48px')
    })

    it('appears in place on first paint and slides only on later switches', () => {
      stub('offsetLeft', (b) => b.left)
      stub('offsetWidth', (b) => b.width)
      const { container } = render(<Harness />)
      const bar = container.querySelector('.rp-settings-tab-ink') as HTMLElement
      expect(bar.classList.contains('rp-settings-tab-ink--animated')).toBe(false)
      fireEvent.click(screen.getByRole('tab', { name: 'Beta' }))
      expect(bar.classList.contains('rp-settings-tab-ink--animated')).toBe(true)
    })

    it('follows a resize without sliding, and is not rebuilt by a re-render with the same tabs', () => {
      stub('offsetLeft', (b) => b.left)
      stub('offsetWidth', (b) => b.width)
      const observers: (() => void)[] = []
      let created = 0
      vi.stubGlobal('ResizeObserver', class {
        constructor(cb: () => void) { created++; observers.push(cb) }
        observe() {}
        disconnect() {}
      })
      const sections = () => [{ key: 'a', label: 'Alpha' }, { key: 'b', label: 'Beta' }]
      const view = (active: string) => (
        <SettingsWorkspace active={active} onChange={() => {}} sections={sections()}>
          <SettingsPanel section="a">a</SettingsPanel>
          <SettingsPanel section="b">b</SettingsPanel>
        </SettingsWorkspace>
      )
      const { container, rerender } = render(view('a'))
      rerender(view('b'))
      const bar = container.querySelector('.rp-settings-tab-ink') as HTMLElement
      expect(bar.classList.contains('rp-settings-tab-ink--animated')).toBe(true)
      rerender(view('b'))
      expect(created).toBe(1)
      // A callback that finds the tab unmoved (only the list changed) leaves the slide running.
      observers[0]()
      expect(bar.classList.contains('rp-settings-tab-ink--animated')).toBe(true)
      boxes.Beta = { left: 70, width: 52 }
      observers[0]()
      expect(bar.style.transform).toBe('translateX(70px)')
      expect(bar.classList.contains('rp-settings-tab-ink--animated')).toBe(false)
    })

    it('underlines nothing when the selection has no tab', () => {
      const { container } = render(
        <SettingsWorkspace active="gone" onChange={() => {}} sections={[{ key: 'a', label: 'Alpha' }]}>
          <SettingsPanel section="a">a</SettingsPanel>
        </SettingsWorkspace>,
      )
      expect((container.querySelector('.rp-settings-tab-ink') as HTMLElement).style.visibility).toBe('hidden')
    })
  })
})
