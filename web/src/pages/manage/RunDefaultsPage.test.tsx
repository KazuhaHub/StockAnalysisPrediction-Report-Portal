import { describe, expect, it, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import RunDefaultsPage from './RunDefaultsPage'

// Three GETs back this page — the stored defaults, the workflow list and the preset windows —
// and `hold` keeps them all on the wire, which is what a slow link looks like for the seconds
// that matter.
const server = vi.hoisted(() => ({
  hold: false,
  config: {} as Record<string, unknown>,
  targets: [] as Array<Record<string, unknown>>,
  presets: [] as Array<Record<string, unknown>>,
  fail: false,
  posted: [] as Array<Record<string, unknown>>,
  put: [] as Array<{url:string; body:Record<string,unknown>}>,
  ou: {} as Record<string,unknown>,
}))

vi.mock('../../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url === '/api/admin/groups') return Promise.resolve({groups:[{id:1,name:'Default',is_default:true,members:0},{id:7,name:'Research',parent_id:1,members:1}]})
      if (url.endsWith('/defaults/7')) return Promise.resolve(server.ou)
      if (server.hold) return new Promise(() => {})
      if (server.fail) return Promise.reject(new Error('offline'))
      if (url.endsWith('/config')) return Promise.resolve(server.config)
      if (url.endsWith('/targets')) return Promise.resolve({ targets: server.targets })
      return Promise.resolve({ presets: server.presets })
    },
    put: (url:string, body:Record<string,unknown>) => {server.put.push({url,body}); return Promise.resolve(server.ou)},
    post: (_url: string, body: Record<string, unknown>) => {
      server.posted.push(body)
      return Promise.resolve({})
    },
  },
  errText: () => 'boom',
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const target = (id: number, name: string, surfaces?: string[]) => ({
  id,
  name,
  plugin_slug: 'dify',
  created_at: '',
  mode: 'workflow',
  ...(surfaces ? { surfaces } : {}),
})

const renderPage = (entry = '/') =>
  render(
    <MemoryRouter initialEntries={[entry]}>
      <App>
        <RunDefaultsPage />
      </App>
    </MemoryRouter>,
  )

beforeEach(() => {
  server.hold = false
  server.fail = false
  server.config = {}
  server.targets = []
  server.presets = []
  server.posted = []
  server.put = []
  server.ou = {effective:{run_default_retries:2},inherited:{run_default_retries:2},sources:{run_default_retries:"Default"},overrides:{}}
})

describe('RunDefaultsPage before its answers arrive', () => {
  // Same rule as the run/queue page: every field here starts at a plausible value and the form is
  // live, so showing it early means an admin can read a default the server never sent — and save
  // it back over a real one.
  it('shows neither the form nor the save button until the server has answered', async () => {
    server.hold = true
    renderPage()
    await screen.findByText('common.loading')
    expect(screen.queryByText('runDefaults.workflow')).toBeNull()
    expect(screen.queryByText('common.save')).toBeNull()
  })

  it('reports a failed load instead of presenting its own blanks as the configuration', async () => {
    server.fail = true
    renderPage()
    await waitFor(() => expect(screen.getByText('common.loadFailed')).toBeTruthy())
    expect(screen.queryByText('common.save')).toBeNull()
  })
})

describe('RunDefaultsPage', () => {
  it('retains a launch edit while changing completion defaults and discards both', async () => {
    renderPage()
    const idle = await screen.findByRole('switch', { name: 'runDefaults.idle' })
    fireEvent.click(idle)
    fireEvent.click(screen.getByRole('tab', { name: 'runDefaults.section.completion' }))
    fireEvent.change(screen.getByRole('spinbutton', { name: 'runDefaults.retries' }), { target: { value: '3' } })
    fireEvent.click(screen.getByRole('button', { name: 'manage.discard' }))
    expect((screen.getByRole('spinbutton', { name: 'runDefaults.retries' }) as HTMLInputElement).value).toBe('0')
    fireEvent.click(screen.getByRole('tab', { name: 'runDefaults.section.launch' }))
    expect(idle.getAttribute('aria-checked')).toBe('false')
    expect(server.posted).toHaveLength(0)
  })
  it('posts the zeros that mean "no default" rather than omitting them', async () => {
    // Clearing a default has to be expressible: an omitted field leaves the stored one alone, so
    // "no default" must travel as an explicit 0 / false.
    server.config = { run_default_target_id: 7, run_default_mode: 'now', run_default_preset_id: 3 }
    server.targets = [target(7, 'Daily')]
    server.presets = [{ id: 3, label: 'Off-peak', freq: 'daily', intervals: [], on_overrun: 'next', enabled: true, invert: false, ord: 0 }]
    renderPage()
    await screen.findByText('runDefaults.workflow')

    // The workflow picker: open it and take the "no default" entry.
    const user = userEvent.setup()
    await user.click(screen.getByText('Daily'))
    await user.click(await screen.findByTitle('runDefaults.none'))
    await user.click(screen.getByText('common.save'))

    await waitFor(() => expect(server.posted).toHaveLength(1))
    expect(server.posted[0]).toMatchObject({
      run_default_target_id: 0,
      run_default_preset_id: 3, // untouched fields still travel, so a save is a full statement
      run_default_mode: 'now',
      run_default_idle: false,
      run_default_notify: false,
    })
  })

  it('still lists a saved workflow that has since been hidden from the run dialog', async () => {
    // The alternative is a picker that shows "no default" for a portal that has one — the admin
    // would have no way to see, keep, or clear the choice they made.
    server.config = { run_default_target_id: 7 }
    server.targets = [target(7, 'Daily', ['batch']), target(8, 'Weekly')]
    renderPage()
    await screen.findByText('runDefaults.workflow')
    await waitFor(() => expect(screen.getByTitle('Daily（runDefaults.hiddenOnRun）')).toBeTruthy())
  })
})

 describe('OU run defaults', () => {
  it('inherits defaults and saves an explicit zero only to the selected OU', async () => {
    renderPage('/manage/rundefaults?scope=ou&ou=7')
    await screen.findByText('runDefaults.workflow')
    fireEvent.click(screen.getByRole('tab', { name: 'runDefaults.section.completion'}))
    expect(screen.queryByRole('spinbutton')).toBeNull()
    fireEvent.click(screen.getAllByRole('button', {name:'ou.override'})[0])
    fireEvent.change(screen.getByRole('spinbutton'), {target:{value:'0'}})
    fireEvent.click(screen.getByRole('button', {name:/common.save/}))
    await waitFor(() => expect(server.put).toHaveLength(1))
    expect(server.put[0]).toEqual({url:'/api/admin/batch/defaults/7',body:{
      run_default_retries:0,run_default_idle:null,run_default_mode:null,run_default_target_id:null,
      run_default_preset_id:null,run_default_notify:null,run_show_preset_rule:null,
    }})
    expect(server.posted).toHaveLength(0)
  })
 })
