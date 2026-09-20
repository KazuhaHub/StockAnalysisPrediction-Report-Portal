import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ManageLayout from './ManageLayout'

const navigate = vi.fn()

// The layout reads the active key off the path and navigates on menu click.
vi.mock('react-router', () => ({
  useNavigate: () => navigate,
  useLocation: () => ({ pathname: '/manage/site' }),
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

describe('ManageLayout — grouped rail', () => {
  beforeEach(() => {
    navigate.mockReset()
    localStorage.removeItem(COLLAPSE_KEY)
    vi.stubEnv('VITE_BUILD_VERSION', 'v2026.38')
  })
  afterEach(() => vi.unstubAllEnvs())

  it('renders section group headers (no Maintenance group after legacy import removal)', () => {
    render(<ManageLayout />)
    for (const header of [
      'nav.group.site',
      'nav.group.content',
      'nav.group.access',
      'nav.group.batch',
      'nav.group.integrations',
    ]) {
      expect(screen.getByText(header)).toBeTruthy()
    }
    expect(screen.queryByText('nav.group.maintenance')).toBeNull()
  })

  it('exposes the pages that used to be buried under Settings; legacy import is gone', () => {
    render(<ManageLayout />)
    for (const leaf of ['settings.general', 'nav.announcement', 'settings.tokens', 'settings.apidoc']) {
      expect(screen.getByText(leaf)).toBeTruthy()
    }
    expect(screen.queryByText('settings.legacyTab')).toBeNull()
  })

  it('navigates to the sub-route when a menu item is clicked', () => {
    render(<ManageLayout />)
    fireEvent.click(screen.getByText('nav.webhooks'))
    expect(navigate).toHaveBeenCalledWith('/manage/webhooks')
  })

  it('collapses the rail and persists the choice', () => {
    render(<ManageLayout />)
    expect(localStorage.getItem(COLLAPSE_KEY)).toBeNull()
    fireEvent.click(screen.getByText('nav.collapse'))
    expect(localStorage.getItem(COLLAPSE_KEY)).toBe('1')
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
