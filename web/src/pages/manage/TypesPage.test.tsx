import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import TypesPage from './TypesPage'

const post = vi.fn()
const get = vi.fn()

vi.mock('../../api/client', () => ({
  api: {
    get: (...a: unknown[]) => get(...a),
    post: (...a: unknown[]) => post(...a),
    del: () => Promise.resolve({}),
  },
  errText: () => 'Offline',
}))

// Echo the i18n key (plus interpolation) so buttons are findable by their key.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k),
  }),
}))

const renderPage = () =>
  render(
    <App>
      <TypesPage />
    </App>,
  )

describe('TypesPage — restore defaults', () => {
  beforeEach(() => {
    post.mockReset()
    get.mockReset()
    get.mockResolvedValue({ groups: [], kinds: [], colors: {} })
  })

  it('posts to the restore-defaults endpoint only after the confirm is accepted', async () => {
    post.mockResolvedValue({ restored: 26 })
    renderPage()

    fireEvent.click(await screen.findByText('types.restoreDefaults'))
    expect(post).not.toHaveBeenCalled() // Popconfirm gate: nothing fires on the first click

    fireEvent.click(await screen.findByText('OK')) // antd Popconfirm default confirm label
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/admin/types/restore-defaults', {}))
  })
})

describe('TypesPage — kind colors', () => {
  beforeEach(() => {
    post.mockReset()
    get.mockReset()
  })
  it('retains failed configuration edits and discards without changing the saved taxonomy', async () => {
    get.mockResolvedValue({ groups: [{ kind: 'Research', rows: [{ name: 'Overview', kind: 'Research', ord: 0, isSummary: true, label: 'Summary' }] }], kinds: ['Research'], colors: {} })
    post.mockRejectedValue(new Error('Offline'))
    renderPage()
    const label = await screen.findByDisplayValue('Summary')
    fireEvent.change(label, { target: { value: 'Draft label' } })
    fireEvent.click(screen.getByRole('button', { name: /common.save/ }))
    await screen.findByText('Offline')
    expect((label as HTMLInputElement).value).toBe('Draft label')
    fireEvent.click(screen.getByRole('button', { name: 'manage.discard' }))
    expect((label as HTMLInputElement).value).toBe('Summary')
  })

  it('shows the configured color on the kind tag and saves a change immediately (no Save-page click needed)', async () => {
    get.mockResolvedValue({
      groups: [{ kind: '投资决策', rows: [{ name: '汇总', kind: '投资决策', ord: 0, isSummary: true, label: '' }] }],
      kinds: ['投资决策'],
      colors: { 投资决策: 'blue' },
    })
    post.mockResolvedValue({ ok: true })
    renderPage()

    // antd 6's Select renders the selected value as visible text (not just the input
    // value as in v5), so the kind name now appears twice — scope to the Tag we assert on.
    const tag = await screen.findByText('投资决策', { selector: '.ant-tag' })
    expect(tag.className).toContain('blue')

    // The swatch trigger opens a palette; picking a swatch posts immediately and closes it.
    fireEvent.click(screen.getByRole('button', { name: 'types.kindColor' }))
    fireEvent.click(await screen.findByRole('button', { name: 'volcano' }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/admin/kind-colors', { kind: '投资决策', color: 'volcano' }),
    )
  })
})
