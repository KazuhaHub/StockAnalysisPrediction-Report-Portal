import { describe, expect, it } from 'vitest'
import type { PlatformRelease } from '../../api/appPlatform'
import { releaseAction } from './platformLifecycle'

const t = ((key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key) as never
const installation = '33333333-3333-4333-8333-333333333333'
const release = (extra: Partial<PlatformRelease> = {}): PlatformRelease => ({ release_sha256: 'a'.repeat(64), app_id: 'notes', publisher_id: 'kazuha', version: '1.1.0', trust_revision: '1', installable: true, ...extra })

describe('releaseAction: which flow a staged release opens for an installed app', () => {
  it('is an enable when the release is the disabled installation\'s own', () => {
    expect(releaseAction(release({ enableable: true }), installation, t)).toMatchObject({ action: 'enable', release: { installation_id: installation } })
  })
  it('is an uninstall when that is all the release allows', () => {
    expect(releaseAction(release({ uninstallable: true }), installation, t)).toMatchObject({ action: 'uninstall', release: { installation_id: installation } })
  })
  it('refuses the version that is already installed', () => {
    expect(releaseAction(release({ installed: true }), installation, t)).toEqual({ error: 'appStore.alreadyInstalled' })
  })
  it('is an update for any other release of an installed app, leaving a downgrade to the server', () => {
    expect(releaseAction(release(), installation, t)).toMatchObject({ action: 'update', release: { installation_id: installation } })
  })
  it('is an install when no installation is named and the release is installable', () => {
    expect(releaseAction(release(), undefined, t)).toMatchObject({ action: 'install' })
  })
  it('says why a release that can be neither installed nor tied to an installation opens nothing', () => {
    expect(releaseAction(release({ installable: false, reason: 'platform_incompatible' }), undefined, t)).toEqual({ error: 'appMarket.verificationChanged' })
  })
})
