import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import VersionsPage from './VersionsPage'

const api = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), del: vi.fn() }))
vi.mock('../../api/client', () => ({ api, errText: (error: unknown) => String(error) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
const listing = { versions: [
  { name: 'default', label: 'Internal', ord: 0, visibility: 'all', grants: ['g:1'], is_default: true, reports: 9 },
  { name: 'client', label: 'Client', ord: 1, visibility: 'owner', grants: ['g:2'], is_default: false, reports: 3 },
], groups: [{ principal: 'g:1', name: 'Default' }, { principal: 'g:2', name: 'Customers' }], users: [] }

beforeEach(() => {
  api.get.mockReset().mockResolvedValue(structuredClone(listing))
  api.post.mockReset().mockResolvedValue({})
  api.del.mockReset().mockResolvedValue({})
})
const mount = () => render(<MemoryRouter><App><VersionsPage /></App></MemoryRouter>)

describe('report-version editing', () => {
  it('renders and opens an edition when the server returns null for an empty grant list', async () => {
    api.get.mockResolvedValueOnce({ versions: [{ ...listing.versions[1], grants: null }], groups: null, users: null })
    mount()
    expect(await screen.findByText('versions.noGrants')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    expect(await screen.findByRole('dialog')).toBeTruthy()
    expect(screen.getByRole('textbox', { name: 'versions.label' })).toHaveProperty('value', 'Client')
  })
  it('opens one version in an editor and writes only on explicit save, never on blur', async () => {
    mount()
    const row = (await screen.findByText('Client')).closest('tr')!
    fireEvent.click(within(row).getByRole('button', { name: 'common.edit' }))
    const dialog = await screen.findByRole('dialog')
    const label = within(dialog).getByRole('textbox', { name: 'versions.label' })
    fireEvent.change(label, { target: { value: 'Client edition' } })
    fireEvent.blur(label)
    expect(api.post).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole('button', { name: /common.save/ }))
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/api/admin/versions', {
      name: 'client', label: 'Client edition', ord: 1, visibility: 'owner', grants: ['g:2'],
    }))
  })

  it('keeps creation inputs on a server failure', async () => {
    api.post.mockRejectedValueOnce(new Error('Not available'))
    mount()
    fireEvent.click(await screen.findByRole('button', { name: 'versions.add' }))
    const dialog = await screen.findByRole('dialog')
    const name = within(dialog).getByRole('textbox', { name: 'versions.name' })
    fireEvent.change(name, { target: { value: 'brief' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /common.save/ }))
    await waitFor(() => expect(api.post).toHaveBeenCalled())
    expect((name as HTMLInputElement).value).toBe('brief')
    expect(screen.getByRole('dialog')).toBeTruthy()
  })

  it('shows a failed load with a retry instead of claiming there are no versions', async () => {
    api.get.mockRejectedValueOnce(new Error('Offline'))
    mount()
    expect(await screen.findByText(/Offline/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'versions.add' })).toBeNull()
  })
})
