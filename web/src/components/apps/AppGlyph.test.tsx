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

test.each(['constructor', 'toString', '__proto__', 'hasOwnProperty'])('prototype key %s falls back without crashing', (key) => {
  const { container } = render(<AppGlyph icon={key} />)
  expect(screen.queryByText(key)).toBeNull()
  expect(container.querySelector('.anticon-appstore')).not.toBeNull()
})

test.each(['\u{1F468}\u200D\u{1F469}\u200D\u{1F467}', '\u{1F1EF}\u{1F1F5}', '1\uFE0F\u20E3'])('a single-grapheme emoji %s renders as text', (e) => {
  render(<AppGlyph icon={e} />)
  expect(screen.getByText(e)).toBeTruthy()
})

test.each(['ok\u{1F44D}', '\u00A9 Co', '\u{1F44D}\u{1F44D}'])('%s is not a single emoji and falls back', (v) => {
  const { container } = render(<AppGlyph icon={v} />)
  expect(screen.queryByText(v)).toBeNull()
  expect(container.querySelector('.anticon-appstore')).not.toBeNull()
})
