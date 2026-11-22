import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import AppPageHeader from '../../components/apps/AppPageHeader'
import { InstalledCard } from './appStoreParts'
import { installedPublisher, type AdminApp } from './appsAdminModel'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

const app: AdminApp = {
  key: 'platform:11111111-1111-4111-8111-111111111111', builtin: false, platform: true,
  installationId: '11111111-1111-4111-8111-111111111111', appId: 'market-monitor', publisherId: 'kazuha-hub',
  name: 'Market Monitor', version: '1.4.0', scopes: [], access: [], state: 'installed',
}
const noop = () => undefined
const card = (publisherPending: boolean, publisher?: string) => render(
  <MemoryRouter><InstalledCard app={app} name="Market Monitor" description="" publisher={publisher} publisherPending={publisherPending}
    busy={false} onSwitchVersion={noop} onLifecycle={noop} onViewInCatalogue={noop} /></MemoryRouter>)

afterEach(cleanup)

describe('names are not replaced by identifiers while they load', () => {
  it('keeps the publisher line pending until the layout or the catalogue names the publisher', () => {
    // Nothing has answered: a placeholder, never the identifier.
    expect(installedPublisher(app, undefined, false)).toEqual({ pending: true })
    // The layout carries the verified name with the app: there on first paint, before the catalogue is read.
    expect(installedPublisher({ ...app, publisherName: 'Kazuha Hub' }, undefined, false)).toEqual({ label: 'Kazuha Hub', pending: false })
    // The catalogue's name counts too.
    expect(installedPublisher(app, { publisher_name: 'Kazuha Hub', publisher_id: 'kazuha-hub' }, false)).toEqual({ label: 'Kazuha Hub', pending: false })
    // The identifier is the fallback only once the catalogue has settled and nothing carries a name.
    expect(installedPublisher(app, undefined, true)).toEqual({ pending: false })
    expect(installedPublisher(app, { publisher_id: 'kazuha-hub' }, true)).toEqual({ label: 'kazuha-hub', pending: false })
    // A built-in or retired app has no publisher to wait for.
    expect(installedPublisher({ ...app, platform: false }, undefined, false)).toEqual({ pending: false })
  })

  it('renders a placeholder, not the app id, while the publisher is pending', () => {
    card(true)
    expect(screen.getByRole('status', { name: 'appStore.loadingPublisher' })).toBeTruthy()
    expect(screen.queryByText(/market-monitor/)).toBeNull()
    expect(screen.queryByText(/kazuha-hub/)).toBeNull()
    expect(screen.getByText('Market Monitor')).toBeTruthy()
    // The version needs no catalogue: it is there from the first paint.
    expect(screen.getByText(/1\.4\.0/)).toBeTruthy()
  })

  it('shows the publisher name when it has arrived, and the app id only when nothing names the publisher', () => {
    card(false, 'Kazuha Hub')
    expect(screen.getByText(/Kazuha Hub/)).toBeTruthy()
    expect(screen.queryByRole('status')).toBeNull()
    cleanup()
    card(false)
    expect(screen.getByText(/market-monitor/)).toBeTruthy()
  })

  it('shows a placeholder of the title in the app header until the app has loaded', () => {
    const { rerender } = render(<AppPageHeader name="" loading icon={null} state={null} onBack={noop} />)
    expect(screen.getByRole('status', { name: 'apps.loadingName' })).toBeTruthy()
    rerender(<AppPageHeader name="Market Monitor" icon={null} state={null} onBack={noop} />)
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.getByText('Market Monitor')).toBeTruthy()
  })
})
