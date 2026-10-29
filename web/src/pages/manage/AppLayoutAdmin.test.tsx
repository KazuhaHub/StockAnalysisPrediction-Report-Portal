import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import type { AppAccessOU, AppLayoutEntry, UserGroupRow } from '../../api/types'
import AppLayoutAdmin from './AppLayoutAdmin'

// The administrator's app layout (ADR 0048): one shared order, and per OU an open / closed / inherit
// control that shows its own value, the effective value and the OU the effective value comes from.

const apiMock = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})
// Dragging itself is dnd.test.tsx's concern. This stand-in exposes the wrapper's contract, a reordered
// id sequence, through a button, so the test can assert what the page persists.
vi.mock('./dnd', () => ({
  SortableWrapper: ({ ids, onReorder, children }: { ids: string[]; onReorder: (ids: string[]) => void; children: React.ReactNode }) => (
    <div>
      <button onClick={() => onReorder([...ids].reverse())}>reverse</button>
      {children}
    </div>
  ),
  SortableItem: ({ id, children }: { id: string; children: React.ReactNode }) => <div data-sortable-id={id}>{children}</div>,
  DragHandle: ({ label }: { label?: string }) => <span aria-label={label} />,
}))

const groups = [
  { id: 1, name: 'Default', is_default: true, members: 0, weight: 0, parent_id: 0 },
  { id: 2, name: 'Research', is_default: false, members: 3, weight: null, parent_id: 1 },
] as unknown as UserGroupRow[]

const acc = (ou: number, own: boolean | null, effective: boolean, source_ou: number): AppAccessOU => ({ ou, own, effective, source_ou, inherited: own === null && source_ou !== 0 })
const layout = (): AppLayoutEntry[] => [
  { key: 'hello', name: 'Hello', builtin: false, access: [acc(1, false, false, 1), acc(2, null, false, 1)] },
  { key: 'builtin:quotes', name: 'quotes', builtin: true, access: [acc(1, null, true, 0), acc(2, true, true, 2)] },
]

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.put.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/groups') return { groups }
    if (url === '/api/admin/apps/layout') return { apps: layout() }
    throw new Error(`unexpected ${url}`)
  })
  apiMock.put.mockResolvedValue({ apps: layout() })
})

const renderPanel = () => render(<App><AppLayoutAdmin /></App>)

describe('AppLayoutAdmin order', () => {
  it('lists every app in the order the server sent, built-in apps by their translated name', async () => {
    const { container } = renderPanel()
    await screen.findByRole('heading', { name: 'Default' })
    const rows = Array.from(container.querySelectorAll('[data-sortable-id]'))
    expect(rows.map((r) => r.getAttribute('data-sortable-id'))).toEqual(['hello', 'builtin:quotes'])
    expect(rows.map((r) => r.textContent)).toEqual(['Hello', 'nav.quotes'])
  })

  it('persists the new order on drop, as the whole key sequence', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(await screen.findByRole('button', { name: 'reverse' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/apps/order', { keys: ['builtin:quotes', 'hello'] }))
  })
})

describe('AppLayoutAdmin access by OU', () => {
  it('opens on the Default OU, where no row means open from the system default', async () => {
    renderPanel()
    expect(await screen.findByRole('heading', { name: 'Default' })).toBeTruthy()
    expect(screen.getByRole('switch', { name: 'Hello' }).getAttribute('aria-checked')).toBe('false')
    // Hello's link back to inheriting, and quotes' state line: both say open from the system default.
    expect(screen.getAllByText('ou.inheritedAs:{"from":"ou.systemDefault","value":"apps.access.open"}').length).toBe(2)
    expect(screen.queryByRole('switch', { name: 'nav.quotes' })).toBeNull()
  })

  it('shows what a child inherits and from which OU', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(await screen.findByText('Research'))
    expect(await screen.findByRole('heading', { name: 'Research' })).toBeTruthy()
    // Hello is inherited as closed from Default; quotes is the child's own override (open).
    expect(screen.getByText('ou.inheritedAs:{"from":"Default","value":"apps.access.closed"}')).toBeTruthy()
    expect(screen.getAllByRole('button', { name: 'ou.override' }).length).toBe(1)
  })

  it('sets an override, and returns to inheriting with null', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(await screen.findByText('Research'))
    await screen.findByRole('heading', { name: 'Research' })
    await user.click(screen.getByRole('button', { name: 'ou.override' }))
    // Overriding starts from the effective value (closed), as an explicit row.
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/apps/hello/access/2', { enabled: false }))
    apiMock.put.mockClear()
    // quotes is overridden open on Research: its inherited link clears the row.
    await user.click(screen.getByRole('button', { name: 'ou.inheritedAs:{"from":"ou.systemDefault","value":"apps.access.open"}' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/apps/builtin%3Aquotes/access/2', { enabled: null }))
  })

  it('toggles the switch into an explicit value', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(await screen.findByRole('switch', { name: 'Hello' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/apps/hello/access/1', { enabled: true }))
  })
})
