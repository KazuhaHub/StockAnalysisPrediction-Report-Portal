import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App, ConfigProvider } from 'antd'
import type { ExportPolicyEntry, ExportPolicyResult, UserGroupRow } from '../../../api/types'
import ReaderExportPolicy from './ReaderExportPolicy'
import { mockNarrowViewport } from '../../../test/narrow'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('../../../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const pending = vi.hoisted(() => vi.fn())
vi.mock('../AdminDraftBoundary', () => ({ usePendingSettings: (dirty: boolean, saving?: boolean) => pending(dirty, saving) }))
const lastPending = () => pending.mock.calls[pending.mock.calls.length - 1]

const res = <T,>(own: T | null, effective: T, source_ou: number): ExportPolicyResult<T> => ({ own, effective, source_ou, inherited: own === null, available: true })
const groups = [
  { id: 1, name: 'Default', is_default: true, members: 0, weight: 0, parent_id: 0 },
  { id: 2, name: 'Research', is_default: false, members: 3, weight: null, parent_id: 1 },
] as unknown as UserGroupRow[]
const entries: ExportPolicyEntry[] = [
  { id: 1, name: 'Default', parent_id: 0, is_default: true, allowed: res(false, false, 1), max_reports: res(200, 200, 1), daily_quota: res(1000, 1000, 1) },
  { id: 2, name: 'Research', parent_id: 1, is_default: false, allowed: res<boolean>(null, false, 1), max_reports: res<number>(null, 200, 1), daily_quota: res<number>(null, 1000, 1) },
]

beforeEach(() => {
  pending.mockReset()
  apiMock.get.mockReset()
  apiMock.put.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/groups') return { groups }
    if (url === '/api/admin/exports/policy') return { groups: entries }
    throw new Error(`unexpected ${url}`)
  })
  apiMock.put.mockResolvedValue({})
})

const renderPanel = () => render(<ConfigProvider theme={{ token: { motion: false } }}><App><ReaderExportPolicy /></App></ConfigProvider>)

describe('ReaderExportPolicy', () => {
  it('opens on the Default OU, which cannot inherit', async () => {
    renderPanel()
    expect(await screen.findByRole('heading', { name: 'Default' })).toBeTruthy()
    expect(screen.getByRole('spinbutton', { name: 'export.reader.fieldMax' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'ou.override' })).toBeNull()
    expect((screen.getByRole('button', { name: 'common.save' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('shows what a child inherits and from where, and overrides one field only', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByRole('heading', { name: 'Default' })
    await user.click(await screen.findByText('Research'))
    expect(await screen.findByRole('heading', { name: 'Research' })).toBeTruthy()
    expect(screen.getByText('ou.inheritedAs:{"from":"Default","value":"200"}')).toBeTruthy()
    // Override the per-export maximum only.
    await user.click(screen.getAllByRole('button', { name: 'ou.override' })[1])
    const input = screen.getByRole('spinbutton', { name: 'export.reader.fieldMax' })
    await user.clear(input)
    await user.type(input, '20')
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/exports/policy/2', { max_reports: 20 }))
  })

  it('hints when the daily quota is below the per-export maximum', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByRole('heading', { name: 'Default' })
    const quota = screen.getByRole('spinbutton', { name: 'export.reader.fieldQuota' })
    await user.clear(quota)
    await user.type(quota, '50')
    expect(await screen.findByText('export.reader.quotaBelowMax:{"quota":50,"max":200}')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/exports/policy/1', { daily_quota: 50 }))
  })
})

describe('ReaderExportPolicy unsaved changes', () => {
  it('is not pending after load', async () => {
    renderPanel()
    await screen.findByRole('heading', { name: 'Default' })
    expect(pending.mock.calls.some(([dirty]) => dirty === true)).toBe(false)
    expect(lastPending()).toEqual([false, false])
  })

  it('is pending once a field differs, including while the value is invalid, and clean when put back', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByRole('heading', { name: 'Default' })
    const max = screen.getByRole('spinbutton', { name: 'export.reader.fieldMax' })
    await user.clear(max)
    expect(lastPending()).toEqual([true, false])
    expect((screen.getByRole('button', { name: 'common.save' }) as HTMLButtonElement).disabled).toBe(true)
    await user.type(max, '200')
    expect(lastPending()).toEqual([false, false])
  })

  it('is clean again after a successful save, even when the server answer is unchanged', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByRole('heading', { name: 'Default' })
    const quota = screen.getByRole('spinbutton', { name: 'export.reader.fieldQuota' })
    await user.clear(quota)
    await user.type(quota, '900')
    expect(lastPending()).toEqual([true, false])
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/exports/policy/1', { daily_quota: 900 }))
    await waitFor(() => expect(lastPending()).toEqual([false, false]))
    expect((screen.getByRole('button', { name: 'common.save' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('asks before switching to another OU with an unsaved draft', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByRole('heading', { name: 'Default' })
    const max = screen.getByRole('spinbutton', { name: 'export.reader.fieldMax' })
    await user.clear(max)
    await user.type(max, '20')
    await user.click(await screen.findByText('Research'))
    await user.click(await screen.findByRole('button', { name: 'manage.stay' }))
    await waitFor(() => expect(screen.queryAllByText('manage.leaveTitle')).toHaveLength(0))
    expect(screen.getByRole('heading', { name: 'Default' })).toBeTruthy()
    expect((screen.getByRole('spinbutton', { name: 'export.reader.fieldMax' }) as HTMLInputElement).value).toBe('20')
    await user.click(screen.getByText('Research'))
    await user.click(await screen.findByRole('button', { name: 'manage.leave' }))
    expect(await screen.findByRole('heading', { name: 'Research' })).toBeTruthy()
  })

  it('switches OU without asking when the draft is clean', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByRole('heading', { name: 'Default' })
    await user.click(screen.getByText('Research'))
    expect(await screen.findByRole('heading', { name: 'Research' })).toBeTruthy()
    expect(screen.queryAllByText('manage.leaveTitle')).toHaveLength(0)
  })
})

describe('ReaderExportPolicy on a phone', () => {
  mockNarrowViewport()
  it('folds the unit tree behind a current-unit button and folds it after a pick', async () => {
    const user = userEvent.setup()
    renderPanel()
    await screen.findByRole('heading', { name: 'Default' })
    expect(screen.queryByRole('tree')).toBeNull()
    await user.click(screen.getByRole('button', { name: /Default/ }))
    await user.click(await screen.findByText('Research'))
    expect(await screen.findByRole('heading', { name: 'Research' })).toBeTruthy()
    expect(screen.queryByRole('tree')).toBeNull()
    const after = screen.getByRole('button', { name: /Research/ })
    expect(after.getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(after)
  })
})
