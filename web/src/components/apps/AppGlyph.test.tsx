import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import AppGlyph from './AppGlyph'

test('a display key renders the antd icon, never the raw key as text', () => {
  const { container } = render(<AppGlyph icon="dashboard" />)
  expect(screen.queryByText('dashboard')).toBeNull()
  expect(container.querySelector('.anticon-dashboard')).not.toBeNull()
})

test('a short legacy emoji renders as text', () => {
  render(<AppGlyph icon="📊" />)
  expect(screen.getByText('📊')).toBeTruthy()
})

test('an unknown word or a missing value falls back to the generic glyph', () => {
  const a = render(<AppGlyph icon="some-unknown-key" />)
  expect(screen.queryByText('some-unknown-key')).toBeNull()
  expect(a.container.querySelector('.anticon-appstore')).not.toBeNull()
  a.unmount()
  const b = render(<AppGlyph />)
  expect(b.container.querySelector('.anticon-appstore')).not.toBeNull()
})
