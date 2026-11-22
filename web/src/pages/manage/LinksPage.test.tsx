import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DESCRIPTOR_LOCALES } from '../../lib/descriptor-contract.generated'
import { App } from 'antd'
import LinksPage from './LinksPage'

// Smoke test: the reworked LinksPage (single drag list of group headers + links) must mount
// without crashing and render both a group header and a link. Guards against a render/hook
// regression in the group rework.
const requested: string[] = []
const live = vi.hoisted(() => ({ extraLinks: [] as unknown[], layout: [] as unknown[], gate: null as Promise<void> | null }))
vi.mock('../../api/client', () => ({
  api: {
    get: (url: string) => {
      requested.push(url)
      if (url === '/api/admin/links')
        return Promise.resolve({
          links: [
            { id: 1, label: 'GitHub', url: 'https://github.com', icon: '', newTab: true, groupId: 5, ord: 0 },
            { id: 2, label: '', url: 'rp:apps:builtin:batch', icon: '', newTab: false, groupId: 0, ord: 1 },
            ...live.extraLinks,
          ],
          groups: [{ id: 5, name: 'External', mode: 'expand', showLabel: true, ord: 0 }],
        })
      if (url === '/api/admin/apps/layout') return (live.gate ?? Promise.resolve()).then(() => ({ apps: live.layout }))
      return Promise.resolve({ apps: [], targets: [] })
    },
    post: () => Promise.resolve({}),
    put: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
}))
vi.mock('../../auth', () => ({ useAuth: () => ({ can: () => true }) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

describe('LinksPage', () => {
  it('renders group headers and links without crashing', async () => {
    render(
      <App>
        <LinksPage />
      </App>,
    )
    // The link label and the group name (now shown as static text, edited via the pencil→modal)
    // both render.
    expect(await screen.findByText('GitHub')).toBeTruthy()
    expect(screen.getByText('External')).toBeTruthy()
    // The add-group action is present.
    expect(screen.getByText('links.addGroup')).toBeTruthy()
  })
})

describe('the entry-button app picker', () => {
  it('reads the administrator layout, never the reader listing that follows the switches', async () => {
    render(
      <App>
        <LinksPage />
      </App>,
    )
    await screen.findByText('GitHub')
    expect(requested).toContain('/api/admin/apps/layout')
    expect(requested).not.toContain('/api/apps')
  })
})

describe('a shortcut entry left blank', () => {
  it('is listed under the name the home page will show, marked as following the language', async () => {
    render(
      <App>
        <LinksPage />
      </App>,
    )
    expect(await screen.findByText('nav.batch')).toBeTruthy()
    expect(screen.getByText('links.labelAuto')).toBeTruthy()
  })
})

describe('entry buttons pinned to a platform app', () => {
  const live1 = '33333333-3333-4333-8333-333333333333'
  const gone = '44444444-4444-4444-8444-444444444444'
  const display = (name: string) => ({ icon: 'file-text', texts: DESCRIPTOR_LOCALES.map(locale => ({ locale, name, description: 'd' })) })
  const layout = () => [
    { key: 'builtin:batch', kind: 'builtin', name: 'batch', builtin: true, access: [] },
    { key: 'hello', kind: 'app', name: 'Retired v1', builtin: false, access: [] },
    { key: `platform:${live1}`, kind: 'platform', name: 'Notes', builtin: false, installation_id: live1, app_id: 'notes', state: 'installed', display: display('Notes'), access: [] },
  ]
  const mount = () => render(<App><LinksPage /></App>)

  it('lists such a pin under the app’s name, without calling it uninstalled', async () => {
    live.layout = layout()
    live.extraLinks = [{ id: 3, label: '', url: `rp:apps:platform:${live1}`, icon: '', newTab: false, groupId: 0, ord: 2 }]
    mount()
    expect(await screen.findByText('Notes')).toBeTruthy()
    expect(screen.queryByText('links.appUninstalled')).toBeNull()
  })

  it('says an entry pinned to an uninstalled app is uninstalled, so it can be cleaned up', async () => {
    live.layout = layout()
    live.extraLinks = [{ id: 4, label: 'Old notes', url: `rp:apps:platform:${gone}`, icon: '', newTab: false, groupId: 0, ord: 2 }]
    mount()
    expect(await screen.findByText('links.appUninstalled')).toBeTruthy()
  })

  it('waits for the layout before it lists a pin, so no label or verdict appears or changes afterwards', async () => {
    live.layout = layout()
    live.extraLinks = [
      { id: 4, label: 'Old notes', url: `rp:apps:platform:${gone}`, icon: '', newTab: false, groupId: 0, ord: 2 },
      { id: 3, label: '', url: `rp:apps:platform:${live1}`, icon: '', newTab: false, groupId: 0, ord: 3 },
    ]
    let open!: () => void
    live.gate = new Promise<void>(resolve => { open = resolve })
    try {
      mount()
      await waitFor(() => expect(screen.getByText('common.loading')).toBeTruthy())
      // Nothing of the list is on screen while the layout is out: not a generic label, not a verdict.
      expect(screen.queryByText('Old notes')).toBeNull()
      expect(screen.queryByText('nav.apps')).toBeNull()
      expect(screen.queryByText('links.appUninstalled')).toBeNull()
      open()
      // The first render of the list is already final: the app's name, and the uninstalled verdict.
      expect(await screen.findByText('Notes')).toBeTruthy()
      expect(screen.getByText('links.appUninstalled')).toBeTruthy()
      expect(screen.getByText('Old notes')).toBeTruthy()
    } finally { live.gate = null }
  })

  it('offers the platform apps after the built-in ones when picking a target, and fills in the label', async () => {
    live.layout = layout()
    live.extraLinks = []
    const user = userEvent.setup()
    mount()
    await screen.findByText('GitHub')
    await user.click(screen.getAllByRole('button', { name: /common\.add/ })[0])
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByText('links.typeShortcut'))
    await user.click(within(dialog).getByRole('combobox', { name: 'links.shortcut' }))
    await user.click(await screen.findByText('nav.apps', { selector: '.ant-select-item-option-content' }))
    await user.click(within(dialog).getByRole('combobox', { name: 'links.shortcutTarget' }))
    const options = await waitFor(() => {
      const found = Array.from(document.querySelectorAll('.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option-content')).map(o => o.textContent)
      expect(found).toContain('Notes')
      return found
    })
    expect(options.indexOf('Notes')).toBeGreaterThan(options.indexOf('nav.batch'))
    expect(options).not.toContain('Retired v1')
    await user.click(screen.getByText('Notes', { selector: '.ant-select-item-option-content' }))
    expect((within(dialog).getByRole('textbox', { name: 'links.label' }) as HTMLInputElement).value).toBe('Notes')
  })
})
