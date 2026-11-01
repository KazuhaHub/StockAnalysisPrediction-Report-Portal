import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App, ConfigProvider } from 'antd'
import { MemoryRouter, Route, Routes, useLocation, useNavigationType } from 'react-router'
import RecurringEditor from './RecurringEditor'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }))
const notifyErrorMock = vi.hoisted(() => vi.fn())
const authState = vi.hoisted(() => ({ admin: true }))

vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
const pending = vi.hoisted(() => vi.fn())
vi.mock('./manage/AdminDraftBoundary', () => ({ usePendingSettings: (dirty: boolean, saving?: boolean) => pending(dirty, saving) }))
vi.mock('../auth', () => ({ useAuth: () => authState }))
vi.mock('../lib/notify', async (original) => ({ ...(await original<typeof import('../lib/notify')>()), notifyError: notifyErrorMock }))
const lastPending = () => pending.mock.calls[pending.mock.calls.length - 1]

const targets = {
  targets: [
    { id: 1, plugin_slug: 'dify', name: 'Daily Review', created_at: '', inputs: [{ key: 'code', required: true }] },
    { id: 2, plugin_slug: 'dify', name: 'Weekly Digest', created_at: '', inputs: [{ key: 'code', required: true }, { key: 'note' }] },
  ],
}
const task = {
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
  row_count: 2,
  next_run: '2026-07-14 09:30:00',
}
let detail: Record<string, unknown>

function Where() {
  const loc = useLocation()
  const type = useNavigationType()
  return <div data-testid="where" data-nav={type}>{loc.pathname}</div>
}

function mount(path: string) {
  return render(
    <ConfigProvider theme={{ token: { motion: false } }}>
      <App>
        <MemoryRouter initialEntries={[path]}>
          <Where />
          <Routes>
            <Route path="/apps/recurring" element={<div>list-page</div>} />
            <Route path="/apps/recurring/new" element={<RecurringEditor />} />
            <Route path="/apps/recurring/:id" element={<RecurringEditor />} />
          </Routes>
        </MemoryRouter>
      </App>
    </ConfigProvider>,
  )
}
const where = () => screen.getByTestId('where').textContent
const nameField = () => screen.findByPlaceholderText('recurring.namePlaceholder') as Promise<HTMLInputElement>
const codeBoxes = () => screen.getAllByRole('textbox', { name: /batch\.editor\.cellLabel/ }) as HTMLTextAreaElement[]

beforeEach(() => {
  pending.mockReset()
  notifyErrorMock.mockReset()
  authState.admin = true
  for (const fn of Object.values(apiMock)) fn.mockReset()
  detail = { ...task, rows: [{ code: '600000' }, { code: '000001' }], history: [] }
  apiMock.get.mockImplementation((url: string) => {
    if (url.includes('/batch/targets')) return Promise.resolve({ ...targets })
    if (url === '/api/admin/batch/recurring') return Promise.resolve({ tasks: [task], maxConcurrency: 8 })
    if (url === '/api/admin/batch/recurring/7') return Promise.resolve({ ...detail })
    return Promise.reject(Object.assign(new Error('not found'), { status: 404 }))
  })
  apiMock.post.mockResolvedValue({ ok: true })
  apiMock.put.mockResolvedValue({ ok: true })
})

describe('RecurringEditor layout', () => {
  it('composes a task from the batch composer: data on the left, run settings on the right', async () => {
    mount('/apps/recurring/new')
    await nameField()
    const layout = document.querySelector('.rp-batch-compose-layout')!
    expect(layout).toBeTruthy()
    const data = layout.querySelector('.rp-batch-compose-data')!
    const settings = layout.querySelector('.rp-batch-compose-settings')!
    expect(within(data as HTMLElement).getByText('recurring.fieldName')).toBeTruthy()
    expect(within(data as HTMLElement).getByText('batch.target')).toBeTruthy()
    expect(within(settings as HTMLElement).getByText('run.settings')).toBeTruthy()
    expect(within(settings as HTMLElement).getByText('recurring.fieldCadence')).toBeTruthy()
    expect(within(settings as HTMLElement).getByText('recurring.tzHint')).toBeTruthy()
    expect(within(settings as HTMLElement).getByText('recurring.fieldEnabled')).toBeTruthy()
    // The raw CSV box and its import and download buttons are gone; the shared grid replaces them.
    expect(document.querySelector('.rp-recurring-execution')).toBeNull()
    expect(screen.queryByText('recurring.importCsv')).toBeNull()
  })

  it('shows the shared grid once a workflow is picked, with the CSV view beside it', async () => {
    const user = userEvent.setup()
    mount('/apps/recurring/new')
    await nameField()
    expect(screen.getByText('batch.selectTargetFirst')).toBeTruthy()
    fireEvent.mouseDown(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('Daily Review'))
    expect(document.querySelector('.rp-batch-grid')).toBeTruthy()
    expect(screen.getByText('batch.editor.csvMode')).toBeTruthy()
    expect(codeBoxes()).toHaveLength(1) // the trailing blank row
  })

  it('uses the shared execution fields in the batch order, capped by the ceiling the server reports', async () => {
    mount('/apps/recurring/new')
    await nameField()
    const fields = Array.from(document.querySelector('.rp-batch-setting-fields')!.querySelectorAll(':scope > label')).map((node) => node.firstElementChild?.textContent)
    expect(fields).toEqual(['batch.maxRetries', 'batch.rowConcurrency', 'batch.priorityLabel'])
    const [, concurrency] = within(document.querySelector('.rp-batch-setting-fields') as HTMLElement).getAllByRole('spinbutton')
    await waitFor(() => expect(concurrency.getAttribute('aria-valuemax')).toBe('8'))
  })

  it('offers the same cadence controls as before', async () => {
    const user = userEvent.setup()
    mount('/apps/recurring/new')
    await nameField()
    expect(screen.queryByText('recurring.dayOfMonth')).toBeNull()
    const [freq] = within(document.querySelector('.rp-recurring-cadence') as HTMLElement).getAllByRole('combobox')
    fireEvent.mouseDown(freq)
    await user.click(await screen.findByText('run.freq.monthly'))
    expect(screen.getByText('recurring.dayOfMonth')).toBeTruthy()
  })

  it('says there is nothing to schedule when no workflow can be run', async () => {
    apiMock.get.mockImplementation((url: string) => (url.includes('/batch/targets') ? Promise.resolve({ targets: [] }) : Promise.resolve({ tasks: [] })))
    mount('/apps/recurring/new')
    expect(await screen.findByText('recurring.noTargets')).toBeTruthy()
    expect(document.querySelector('.rp-batch-compose-layout')).toBeNull()
  })

  it('reports a task that cannot be loaded, with a retry', async () => {
    mount('/apps/recurring/404')
    expect(await screen.findByRole('button', { name: /common\.retry/ })).toBeTruthy()
    expect(document.querySelector('.rp-batch-compose-layout')).toBeNull()
  })
})

describe('RecurringEditor an existing task', () => {
  it('prefills the grid from the stored rows, with one blank row below them', async () => {
    mount('/apps/recurring/7')
    expect((await nameField()).value).toBe('Close review')
    await waitFor(() => expect(codeBoxes()).toHaveLength(3))
    expect(codeBoxes().map((box) => box.value)).toEqual(['600000', '000001', ''])
  })

  it('is not pending when opened, with its stored rows and the trailing blank row', async () => {
    mount('/apps/recurring/7')
    await nameField()
    await waitFor(() => expect(codeBoxes()).toHaveLength(3))
    expect(pending.mock.calls.some(([dirty]) => dirty === true)).toBe(false)
    expect(lastPending()).toEqual([false, false])
  })

  it('saves exactly the body the old editor sent for the same rows, then returns to the list', async () => {
    mount('/apps/recurring/7')
    await nameField()
    await waitFor(() => expect(codeBoxes()).toHaveLength(3))
    fireEvent.change(await nameField(), { target: { value: '  Renamed review ' } })
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledTimes(1))
    expect(apiMock.put).toHaveBeenCalledWith('/api/admin/batch/recurring/7', {
      name: 'Renamed review',
      target_id: 1,
      rows: [{ code: '600000' }, { code: '000001' }],
      concurrency: 1,
      max_retries: 2,
      priority: '',
      freq: 'daily',
      at_time: '09:30',
      weekday: 1,
      monthday: 1,
      enabled: true,
    })
    await waitFor(() => expect(where()).toBe('/apps/recurring'))
    expect(screen.getByText('list-page')).toBeTruthy()
    // Replaced, not pushed: the browser's Back from the list must not reopen the editor.
    expect(screen.getByTestId('where').dataset.nav).toBe('REPLACE')
  })

  it('keeps a stored custom priority exactly as stored', async () => {
    detail = { ...detail, priority: '70' }
    mount('/apps/recurring/7')
    await nameField()
    await waitFor(() => expect(codeBoxes()).toHaveLength(3))
    expect((screen.getByRole('spinbutton', { name: 'recurring.priorityCustom' }) as HTMLInputElement).value).toBe('70')
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalled())
    expect(apiMock.put.mock.calls[0][1].priority).toBe('70')
  })

  it('keeps the rows and columns of a task whose workflow was deleted, and warns', async () => {
    detail = { ...detail, target_id: 99, rows: [{ a: '1', b: '2' }] }
    mount('/apps/recurring/7')
    expect(await screen.findByText('recurring.targetDeletedWarn')).toBeTruthy()
    await waitFor(() => expect(screen.getAllByRole('textbox', { name: /batch\.editor\.cellLabel/ })).toHaveLength(4))
    const grid = within(document.querySelector('.rp-batch-grid') as HTMLElement)
    expect(grid.getByDisplayValue('1')).toBeTruthy()
    expect(grid.getByDisplayValue('2')).toBeTruthy()
    expect(screen.getByText('recurring.targetGone')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalled())
    expect(apiMock.put.mock.calls[0][1].rows).toEqual([{ a: '1', b: '2' }])
    expect(apiMock.put.mock.calls[0][1].target_id).toBe(99)
  })

  it('carries filled values across to another workflow that has the same columns', async () => {
    const user = userEvent.setup()
    mount('/apps/recurring/7')
    await nameField()
    await waitFor(() => expect(codeBoxes()).toHaveLength(3))
    fireEvent.mouseDown(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('Weekly Digest'))
    await waitFor(() => expect(screen.getByDisplayValue('600000')).toBeTruthy())
    expect(screen.getByDisplayValue('000001')).toBeTruthy()
  })
})

describe('RecurringEditor a new task', () => {
  it('posts the grid rows with the cadence and execution settings', async () => {
    const user = userEvent.setup()
    mount('/apps/recurring/new')
    fireEvent.change(await nameField(), { target: { value: 'Nightly' } })
    fireEvent.mouseDown(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('Weekly Digest'))
    fireEvent.paste(codeBoxes()[0], { clipboardData: { getData: () => '600519\tbuy\n000001\thold' } })
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(apiMock.post).toHaveBeenCalledWith('/api/admin/batch/recurring', {
      name: 'Nightly',
      target_id: 2,
      rows: [
        { code: '600519', note: 'buy' },
        { code: '000001', note: 'hold' },
      ],
      concurrency: 1,
      max_retries: 2,
      priority: '',
      freq: 'daily',
      at_time: '09:30',
      weekday: 1,
      monthday: 1,
      enabled: true,
    })
    await waitFor(() => expect(where()).toBe('/apps/recurring'))
  })

  it.each([
    ['idle', 'recurring.priorityIdleLong'],
    ['urgent', 'recurring.priorityUrgent'],
  ])('stores %s priority from the priority select in the shared field slot', async (stored, optionText) => {
    const user = userEvent.setup()
    mount('/apps/recurring/7')
    await nameField()
    await waitFor(() => expect(codeBoxes()).toHaveLength(3))
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'batch.priorityLabel' }))
    await user.click(await screen.findByText(optionText))
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalled())
    expect(apiMock.put.mock.calls[0][1].priority).toBe(stored)
  })

  it('stores 80 for a custom score until it is edited, and offers only normal and idle to a non-admin', async () => {
    const user = userEvent.setup()
    mount('/apps/recurring/7')
    await nameField()
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'batch.priorityLabel' }))
    await user.click(await screen.findByText('recurring.priorityCustom'))
    expect((screen.getByRole('spinbutton', { name: 'recurring.priorityCustom' }) as HTMLInputElement).value).toBe('80')
  })

  it('does not offer urgent or a custom score to a non-admin', async () => {
    authState.admin = false
    mount('/apps/recurring/new')
    await nameField()
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'batch.priorityLabel' }))
    expect(await screen.findAllByText('recurring.priorityIdleLong')).not.toHaveLength(0)
    expect(screen.queryByText('recurring.priorityUrgent')).toBeNull()
    expect(screen.queryByText('recurring.priorityCustom')).toBeNull()
  })

  it('refuses to save without a name, without rows, or with a required cell missing', async () => {
    const user = userEvent.setup()
    mount('/apps/recurring/new')
    await nameField()
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    expect(notifyErrorMock).toHaveBeenLastCalledWith('recurring.errName')
    fireEvent.change(await nameField(), { target: { value: 'Nightly' } })
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    expect(notifyErrorMock).toHaveBeenLastCalledWith('recurring.errRows')
    fireEvent.mouseDown(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('Weekly Digest'))
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    expect(notifyErrorMock).toHaveBeenLastCalledWith('recurring.errRows')
    expect(apiMock.post).not.toHaveBeenCalled()
  })

  it('blocks a save while a required cell is empty', async () => {
    const user = userEvent.setup()
    mount('/apps/recurring/new')
    fireEvent.change(await nameField(), { target: { value: 'Nightly' } })
    fireEvent.mouseDown(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('Weekly Digest')) // `code` is required there
    await user.type(codeBoxes()[1], 'only a note') // a row with data, but no code
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    expect(notifyErrorMock).toHaveBeenLastCalledWith('batch.editor.submitBlocked')
    expect(apiMock.post).not.toHaveBeenCalled()
  })
})

describe('RecurringEditor unsaved changes', () => {
  it('is pending once edited, and not for the trailing blank row alone', async () => {
    mount('/apps/recurring/new')
    const name = await nameField()
    expect(lastPending()).toEqual([false, false])
    fireEvent.change(name, { target: { value: 'x' } })
    expect(lastPending()).toEqual([true, false])
    fireEvent.change(name, { target: { value: '' } })
    expect(lastPending()).toEqual([false, false])
  })

  it('goes pending when a grid cell is edited', async () => {
    mount('/apps/recurring/7')
    await nameField()
    await waitFor(() => expect(codeBoxes()).toHaveLength(3))
    await userEvent.setup().type(codeBoxes()[2], '5')
    expect(lastPending()).toEqual([true, false])
  })

  it('leaves for the list from Back; the draft boundary does the asking', async () => {
    mount('/apps/recurring/7')
    await nameField()
    fireEvent.click(screen.getByRole('button', { name: /common\.back/ }))
    await waitFor(() => expect(where()).toBe('/apps/recurring'))
  })

  it('stays dirty and on the page when a save fails, and is clean and gone after a successful one', async () => {
    mount('/apps/recurring/7')
    const name = await nameField()
    await waitFor(() => expect(codeBoxes()).toHaveLength(3))
    fireEvent.change(name, { target: { value: 'Renamed review' } })
    apiMock.put.mockRejectedValueOnce(new Error('boom'))
    fireEvent.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(lastPending()).toEqual([true, false]))
    expect(where()).toBe('/apps/recurring/7')
    const saveButton = screen.getByText('common.save').closest('button')!
    await waitFor(() => expect(saveButton.className).not.toContain('ant-btn-loading'))
    fireEvent.click(saveButton)
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(where()).toBe('/apps/recurring'))
    expect(lastPending()).toEqual([false, false])
  })
})
