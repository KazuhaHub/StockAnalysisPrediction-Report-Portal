import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App, ConfigProvider } from 'antd'
import RecurringConsole from './RecurringConsole'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
}))

vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
const pending = vi.hoisted(() => vi.fn())
vi.mock('./manage/AdminDraftBoundary', () => ({ usePendingSettings: (dirty: boolean, saving?: boolean) => pending(dirty, saving) }))
const lastPending = () => pending.mock.calls[pending.mock.calls.length - 1]
vi.mock('../auth', () => ({ useAuth: () => ({ admin: true }) }))

const targets = { targets: [{ id: 1, plugin_slug: 'dify', name: 'Daily Review', created_at: '', inputs: [{ key: 'code' }] }] }
const tasks = {
  tasks: [
    {
      id: 7,
      name: 'Close review',
      target_id: 1,
      target_name: 'Daily Review',
      concurrency: 1,
      priority: '',
      max_retries: 2,
      freq: 'daily',
      at_time: '09:30',
      weekday: 1,
      monthday: 1,
      enabled: true,
      created_by: 'alice',
      created_at: '2026-07-13 08:00:00',
      last_fired: '2026-07-12',
      row_count: 3,
      next_run: '2026-07-14 09:30:00',
    },
  ],
}

function renderPage() {
  return render(
    <App>
      <RecurringConsole />
    </App>,
  )
}

describe('RecurringConsole', () => {
  beforeEach(() => {
    pending.mockReset()
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.put.mockReset()
    apiMock.del.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/batch/targets')) return Promise.resolve({ ...targets })
      if (url === '/api/admin/batch/recurring') return Promise.resolve({ ...tasks })
      if (url === '/api/admin/batch/recurring/7')
        return Promise.resolve({ ...tasks.tasks[0], rows: [{ code: '600000' }, { code: '000001' }], history: [] })
      return Promise.resolve({})
    })
    apiMock.post.mockResolvedValue({ ok: true, job_id: 42 })
  })

  it('lists tasks with their cadence, next run, and target', async () => {
    renderPage()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/admin/batch/recurring'))
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/batch/targets')
    expect(await screen.findByText('Close review')).toBeTruthy()
    expect(screen.getByText('recurring.cadence.daily')).toBeTruthy() // daily label key
    expect(screen.getByText('2026-07-14 09:30:00')).toBeTruthy() // next run
    expect(screen.getByText('recurring.priorityNormal')).toBeTruthy() // '' → normal tag
  })

  it('toggling the enable switch posts to /enable', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByRole('switch'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/batch/recurring/7/enable', { enabled: false }))
  })

  it('the new-task button opens the create modal', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByRole('button', { name: /recurring\.new/ }))
    // the modal renders the name + target fields.
    expect((await screen.findAllByText('recurring.fieldName')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('recurring.fieldTarget').length).toBeGreaterThan(0)
    expect(screen.getByText('recurring.executionSettings')).toBeTruthy()
    expect(document.querySelector('.rp-recurring-execution-grid')).not.toBeNull()
  })

  it('editing prefills the CSV editor WITH a header row so the template round-trips', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByLabelText('common.edit')) // the aria-labelled edit action
    await screen.findByText('recurring.edit') // modal title (editing mode)
    const ta = screen.getByPlaceholderText('batch.csvPlaceholder') as HTMLTextAreaElement
    // header line + both data rows — NOT data-only (which csvToRows would eat as a header, blanking it).
    expect(ta.value).toBe('code\n600000\n000001')
  })

  it('imports a CSV file into a new task template', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByRole('button', { name: /recurring\.new/ }))

    fireEvent.mouseDown(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('Daily Review'))
    const input = document.querySelector<HTMLInputElement>('input[type="file"]')
    expect(input).not.toBeNull()
    await user.upload(input!, new File(['code\n600519\n000001'], 'rows.csv', { type: 'text/csv' }))

    await waitFor(() => {
      const editor = screen.getByPlaceholderText('batch.csvPlaceholder') as HTMLTextAreaElement
      expect(editor.value).toBe('code\n600519\n000001')
    })
  })
})

describe('RecurringConsole unsaved changes', () => {
  beforeEach(() => {
    pending.mockReset()
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.put.mockReset().mockResolvedValue({ ok: true })
    apiMock.del.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/batch/targets')) return Promise.resolve({ ...targets })
      if (url === '/api/admin/batch/recurring') return Promise.resolve({ ...tasks })
      if (url === '/api/admin/batch/recurring/7')
        return Promise.resolve({ ...tasks.tasks[0], rows: [{ code: '600000' }, { code: '000001' }], history: [] })
      return Promise.resolve({})
    })
  })
  const mount = () => render(<ConfigProvider theme={{ token: { motion: false } }}><App><RecurringConsole /></App></ConfigProvider>)
  const openEdit = async () => {
    mount()
    await screen.findByText('Close review')
    fireEvent.click(screen.getByLabelText('common.edit'))
    return (await screen.findByPlaceholderText('recurring.namePlaceholder')) as HTMLInputElement
  }

  it('is not pending when the console loads or a new task is opened', async () => {
    mount()
    await screen.findByText('Close review')
    expect(lastPending()).toEqual([false, false])
    fireEvent.click(screen.getByRole('button', { name: /recurring\.new/ }))
    await screen.findByPlaceholderText('recurring.namePlaceholder')
    expect(lastPending()).toEqual([false, false])
  })

  it('is not pending when an existing task is opened for editing', async () => {
    await openEdit()
    expect(pending.mock.calls.some(([dirty]) => dirty === true)).toBe(false)
    expect(lastPending()).toEqual([false, false])
  })

  it('closes a clean editor without asking', async () => {
    const name = await openEdit()
    fireEvent.click(screen.getByRole('button', { name: 'common.cancel' }))
    await waitFor(() => expect(name.isConnected).toBe(false))
    expect(screen.queryAllByText('manage.leaveTitle')).toHaveLength(0)
  })

  it('asks before closing an edited editor through Cancel or the X, and keeps the edits on stay', async () => {
    const name = await openEdit()
    fireEvent.change(name, { target: { value: 'Renamed review' } })
    expect(lastPending()).toEqual([true, false])
    for (const close of [() => screen.getByRole('button', { name: 'common.cancel' }), () => screen.getByRole('button', { name: 'Close' })]) {
      fireEvent.click(close())
      fireEvent.click(await screen.findByRole('button', { name: 'manage.stay' }))
      await waitFor(() => expect(screen.queryAllByText('manage.leaveTitle')).toHaveLength(0))
      expect((screen.getByPlaceholderText('recurring.namePlaceholder') as HTMLInputElement).value).toBe('Renamed review')
    }
    fireEvent.click(screen.getByRole('button', { name: 'common.cancel' }))
    fireEvent.click(await screen.findByRole('button', { name: 'manage.leave' }))
    await waitFor(() => expect(name.isConnected).toBe(false))
    expect(lastPending()).toEqual([false, false])
  })

  it('stays pending when a save fails, and is clean after a successful save', async () => {
    const name = await openEdit()
    fireEvent.change(name, { target: { value: 'Renamed review' } })
    apiMock.put.mockRejectedValueOnce(new Error('boom'))
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(lastPending()).toEqual([true, false]))
    expect(name.isConnected).toBe(true)
    // By text: the error notification that just opened can hide the dialog from role queries.
    // The button ignores clicks until its loading state from the failed save has cleared.
    const saveButton = screen.getByText('common.save').closest('button')!
    await waitFor(() => expect(saveButton.className).not.toContain('ant-btn-loading'))
    fireEvent.click(saveButton)
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(name.isConnected).toBe(false))
    expect(lastPending()).toEqual([false, false])
  })
})
