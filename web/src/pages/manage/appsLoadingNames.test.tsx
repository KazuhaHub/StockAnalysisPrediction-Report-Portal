import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import AppPageHeader from '../../components/apps/AppPageHeader'
import { installedPublisher, type AdminApp } from './appsAdminModel'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

const app: AdminApp = {
  key: 'platform:11111111-1111-4111-8111-111111111111', builtin: false, platform: true,
  installationId: '11111111-1111-4111-8111-111111111111', appId: 'market-monitor', publisherId: 'kazuha-hub',
  name: 'Market Monitor', version: '1.4.0', scopes: [], access: [], state: 'installed',
}
const noop = () => undefined

afterEach(cleanup)

describe('names are never replaced by identifiers', () => {
  it('names the publisher from the layout, then the catalogue, and falls back to the id only when neither does', () => {
    // The layout carries the verified name with the app: the first paint already has it.
    expect(installedPublisher({ ...app, publisherName: 'Kazuha Hub' }, undefined)).toBe('Kazuha Hub')
    // The catalogue the page waited for can name it too.
    expect(installedPublisher(app, { publisher_name: 'Kazuha Hub', publisher_id: 'kazuha-hub' })).toBe('Kazuha Hub')
    // A name from the layout wins over a different one later, so nothing swaps.
    expect(installedPublisher({ ...app, publisherName: 'Kazuha Hub' }, { publisher_name: 'Other', publisher_id: 'kazuha-hub' })).toBe('Kazuha Hub')
    expect(installedPublisher(app, { publisher_id: 'kazuha-hub' })).toBe('kazuha-hub')
    expect(installedPublisher(app, undefined)).toBeUndefined()
    // A built-in or retired app has no publisher.
    expect(installedPublisher({ ...app, platform: false }, { publisher_id: 'kazuha-hub' })).toBeUndefined()
  })

  it('shows a placeholder of the title in the app header until the app has loaded', () => {
    const { rerender } = render(<AppPageHeader name="" loading icon={null} state={null} onBack={noop} />)
    expect(screen.getByRole('status', { name: 'apps.loadingName' })).toBeTruthy()
    rerender(<AppPageHeader name="Market Monitor" icon={null} state={null} onBack={noop} />)
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.getByText('Market Monitor')).toBeTruthy()
  })
})
