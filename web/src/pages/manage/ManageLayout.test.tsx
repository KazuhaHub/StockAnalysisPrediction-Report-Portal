import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import ManageLayout from './ManageLayout'

const navigate = vi.fn()
const locState = vi.hoisted(() => ({ pathname: '/manage/site' }))
const siteState = vi.hoisted(() => ({ settings: {versionDisplay:'footer'} }))
vi.mock('../../site', () => ({ useSite: () => siteState }))

// The layout reads the active key off the path and navigates on menu click.
vi.mock('react-router', () => ({
  useNavigate: () => navigate,
  useLocation: () => ({ pathname: locState.pathname }),
  Outlet: () => null,
}))

// Echo the i18n key, with its values, so menu entries are findable by their key.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${Object.values(o).join(',')}` : k),
    i18n: { language: 'en-US' },
  }),
}))

const COLLAPSE_KEY = 'rp.manage.sider.collapsed'

// Hovering a menu item starts that page's page-defining GET so the click lands on a filled page.
// The prefetch module is mocked rather than fetch, because what is under test is WHICH url a hover
// asks for: warming the wrong one costs a request and buys nothing, and warming the statistics
// endpoint — which measures the database — would be real work started by a mouse passing over a
// menu. The harness records every call so the assertion can be exact.
const prefetched = vi.hoisted(() => [] as string[])
vi.mock('../../lib/prefetch', () => ({
  prefetch: (url: string) => {
    prefetched.push(url)
    return Promise.resolve()
  },
}))

describe('ManageLayout — grouped rail', () => {
  beforeEach(() => {
    navigate.mockReset()
    siteState.settings.versionDisplay = 'footer'
    prefetched.length = 0
    locState.pathname = '/manage/site'
    localStorage.removeItem(COLLAPSE_KEY)
    localStorage.removeItem('rp.manage.sider.open')
    vi.stubEnv('VITE_BUILD_VERSION', 'v2026.38')
  })
  afterEach(() => vi.unstubAllEnvs())

  it('keeps the page description on a page and drops it on a page nested under it', () => {
    locState.pathname = '/manage/apps'
    const first = render(<ManageLayout />)
    expect(screen.getAllByText('manage.description.apps').length).toBeGreaterThan(0)
    first.unmount()
    locState.pathname = '/manage/apps/builtin%3Aquotes/access'
    render(<ManageLayout />)
    expect(screen.queryByText('manage.description.apps')).toBeNull()
    // The title stays: the rail still highlights Apps and the page body has its own breadcrumb.
    expect(screen.getAllByText('nav.appsAdmin').length).toBeGreaterThan(0)
  })

  it('renders section group headers (no Maintenance group after legacy import removal)', () => {
    render(<ManageLayout />)
    for (const header of [
      'nav.group.site',
      'nav.group.content',
      'nav.group.access',
      'nav.group.batch',
      'nav.group.integrations',
    ]) {
      expect(within(screen.getAllByRole('menu')[0]).getByText(header)).toBeTruthy()
    }
    expect(screen.queryByText('nav.group.maintenance')).toBeNull()
  })

  it('exposes the pages that used to be buried under Settings; legacy import is gone', () => {
    render(<ManageLayout />)
    fireEvent.click(screen.getByText('nav.group.access'))
    fireEvent.click(screen.getByText('nav.group.integrations'))
    expect(screen.getByText('settings.apidoc')).toBeTruthy()
    fireEvent.click(screen.getByText('nav.group.access'))
    expect(screen.getByText('settings.tokens')).toBeTruthy()
    fireEvent.click(within(screen.getAllByRole('menu')[0]).getByText('nav.group.site'))
    for (const leaf of ['settings.general', 'nav.announcement']) expect(within(screen.getAllByRole('menu')[0]).getByText(leaf)).toBeTruthy()
    expect(screen.queryByText('settings.legacyTab')).toBeNull()
  })

  it('navigates to the sub-route when a menu item is clicked', () => {
    render(<ManageLayout />)
    fireEvent.click(screen.getByText('nav.group.integrations'))
    fireEvent.click(screen.getByText('nav.webhooks'))
    expect(navigate).toHaveBeenCalledWith('/manage/webhooks')
  })

  it('finds settings by topic and navigates to the selected result', async () => {
    render(<ManageLayout />)
    fireEvent.click(screen.getByRole('button', { name: 'manage.search' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'manage.description.sso' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /nav.sso/ }))
    expect(navigate).toHaveBeenCalledWith('/manage/sso')
  })

  it('offers a route back to the administration overview', () => {
    render(<ManageLayout />)
    fireEvent.click(screen.getByRole('menuitem', { name: /manage.overview/ }))
    expect(navigate).toHaveBeenCalledWith('/manage/overview')
  })

  it('has a 预测验证 entry in the content group', () => {
    render(<ManageLayout />)
    fireEvent.click(screen.getByText('nav.group.content'))
    expect(screen.getByText('nav.forecast')).toBeTruthy()
    fireEvent.click(screen.getByText('nav.forecast'))
    expect(navigate).toHaveBeenCalledWith('/manage/forecast')
  })

  // The hover prefetch for the verification page warms the SETTINGS GET and nothing else. The page
  // also reads a statistics endpoint in PR 7, and that one measures the whole ledger — starting it
  // because a pointer crossed a menu label is exactly the kind of work a hover must never begin.
  it('warms only the settings GET when the forecast entry is hovered', () => {
    render(<ManageLayout />)
    fireEvent.click(screen.getByText('nav.group.content'))
    fireEvent.mouseEnter(screen.getByText('nav.forecast'))
    expect(prefetched).toContain('/api/admin/forecast')
    expect(prefetched.filter((u) => u.includes('forecast'))).toEqual(['/api/admin/forecast'])
  })

  // Every entry in the table is a cheap GET, so the rule the forecast case is a special case of:
  // nothing warmed on hover may be a measuring endpoint.
  it('does not prefetch a nonexistent assistant configuration GET', () => {
    render(<ManageLayout />)
    fireEvent.click(screen.getByText('nav.group.batch'))
    fireEvent.mouseEnter(screen.getByText('manage.label.assistant'))
    expect(prefetched).not.toContain('/api/admin/chat/config')
    expect(prefetched).not.toContain('/api/admin/chat/live')
  })

  it('never warms a measuring endpoint from a hover', () => {
    render(<ManageLayout />)
    for (const key of ['storage', 'batch', 'quotes', 'forecast']) {
      const labels: Record<string, string> = {
        storage: 'nav.storage',
        batch: 'nav.batchAdmin',
        quotes: 'nav.quoteAdmin',
        forecast: 'nav.forecast',
      }
      const section = key === 'storage' || key === 'quotes' ? 'system' : key === 'batch' ? 'batch' : 'content'
      const heading = screen.getByText(`nav.group.${section}`).closest('[role=menuitem]')!
      if (heading.getAttribute('aria-expanded') !== 'true') fireEvent.click(screen.getByText(`nav.group.${section}`))
      fireEvent.mouseEnter(screen.getByText(labels[key]))
    }
    expect(prefetched.some((u) => u.includes('/cleanup/usage'))).toBe(false)
    expect(prefetched.some((u) => u.includes('/forecast/groups'))).toBe(false)
  })

  it('collapses the rail and persists the choice', () => {
    render(<ManageLayout />)
    expect(localStorage.getItem(COLLAPSE_KEY)).toBeNull()
    fireEvent.click(screen.getByText('nav.collapse'))
    expect(localStorage.getItem(COLLAPSE_KEY)).toBe('1')
  })

  it('opens one branch at a time and remembers the selected branch', () => {
    render(<ManageLayout />)
    const site = within(screen.getAllByRole('menu')[0]).getByText('nav.group.site').closest('[role=menuitem]')!
    expect(site.getAttribute('aria-expanded')).toBe('true')
    fireEvent.click(screen.getByText('nav.group.integrations'))
    expect(site.getAttribute('aria-expanded')).toBe('false')
    expect(JSON.parse(localStorage.getItem('rp.manage.sider.open')!)).toEqual(['group.integrations'])
    fireEvent.click(within(screen.getAllByRole('menu')[0]).getByText('nav.group.site'))
    expect(site.getAttribute('aria-expanded')).toBe('true')
    expect(JSON.parse(localStorage.getItem('rp.manage.sider.open')!)).toEqual(['group.site'])
    fireEvent.click(within(screen.getAllByRole('menu')[0]).getByText('nav.group.site'))
    expect(JSON.parse(localStorage.getItem('rp.manage.sider.open')!)).toEqual([])
  })

  // The console shows the same version label as the portal footer, and it is the loaded build's —
  // not the server's, which is what the update prompt exists to compare.
  it('shows the version label in the rail footer, and hides it when collapsed', () => {
    const { unmount } = render(<ManageLayout />)
    expect(screen.getByText('version.label:2026.38')).toBeTruthy()
    unmount()

    localStorage.setItem(COLLAPSE_KEY, '1')
    render(<ManageLayout />)
    // An icon-only strip has no room for it, and the rail's own collapse is not the place to fight
    // that: the footer keeps its entry point at every other width.
    expect(screen.queryByText('version.label:2026.38')).toBeNull()
  })
})

it.each(['header','hidden'])('does not duplicate or expose the version in the rail for %s placement', placement => {
  siteState.settings.versionDisplay = placement
  localStorage.removeItem(COLLAPSE_KEY)
  const { container } = render(<ManageLayout />)
  expect(container.querySelector('.rp-version-label')).toBeNull()
  expect(screen.getByRole('button',{name:'manage.search'})).toBeTruthy()
})
