import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'

import StatusTag, { STATUS_TONES } from './StatusTag'

describe('StatusTag', () => {
  // A status is never told by colour alone: every tone carries its own icon as a second cue.
  // [Carbon status indicators]
  it('gives every tone its own icon, beside the text', () => {
    const icons = STATUS_TONES.map((tone) => {
      const { container, unmount } = render(<StatusTag tone={tone}>label</StatusTag>)
      const icon = container.querySelector('.ant-tag .anticon')
      expect(icon, tone).not.toBeNull()
      expect(container.textContent).toContain('label')
      const name = icon!.getAttribute('aria-label')
      unmount()
      return name
    })
    expect(new Set(icons).size).toBe(STATUS_TONES.length)
  })

  it('colours by tone', () => {
    const { container } = render(<StatusTag tone="error">failed</StatusTag>)
    expect(container.querySelector('.ant-tag')?.className).toContain('ant-tag-error')
  })
})
