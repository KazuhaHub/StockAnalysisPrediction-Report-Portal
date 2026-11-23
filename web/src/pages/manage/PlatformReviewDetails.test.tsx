import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import PlatformReviewDetails from './PlatformReviewDetails'
import type { PlatformRelease } from '../../api/appPlatform'
import type { Review } from './platformLifecycle'

// The mock writes the interpolation values after the key, so a test can see what a row was given.
const mock = vi.hoisted(() => ({ t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? (options ? `${key} ${JSON.stringify(options)}` : key) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mock.t }) }))

const sha = (c: string) => c.repeat(64)
const proxy = 'network.proxy'
const keptProxy = { hosts: ['*.internal.example.com', 'qt.gtimg.cn'], pins: [{ host: 'quotes.internal.example.com', address: '127.0.0.1', port: 20443 }], daily_bytes: 1073741824 }
const updateReview = (extra: Record<string, unknown>): Review => ({
  plan_id: '22222222-2222-4222-8222-222222222222', plan_sha256: sha('b'), expires_ms: String(Date.now() + 900000),
  review: {
    action: 'update', installation_id: '11111111-1111-4111-8111-111111111111', app_id: 'market-monitor', publisher_id: 'kazuha',
    source_release_sha256: sha('c'), release_sha256: sha('a'), source_generation: '2', target_generation: '3', policy_revision: '3', target_policy_revision: '4',
    declined_capabilities: [], previous_capabilities: ['background.run', proxy], target_capabilities: ['background.run', proxy],
    kept_capabilities: [proxy], kept_proxy: keptProxy, policy_reset: [],
    declared_data_schema: '1', grants_sha256: sha('d'), ...extra,
  },
} as unknown as Review)
const release = { required_capabilities: ['background.run'], optional_capabilities: [proxy], version: '1.2.0' } as unknown as PlatformRelease
const rowText = (label: string) => screen.getByText(label).closest('.ant-descriptions-row, tr')?.textContent ?? ''

describe('PlatformReviewDetails optional permissions on update', () => {
  afterEach(cleanup)

  it('shows a kept optional permission as retained with the proxy hosts, pins and budget it carries', () => {
    render(<PlatformReviewDetails review={updateReview({})} release={release} groups={[]} expired={false} />)
    expect(rowText('appPlatform.retainedPermissions')).toContain(proxy)
    const carried = screen.getByTestId('kept-proxy').textContent ?? ''
    expect(carried).toContain('*.internal.example.com, qt.gtimg.cn')
    expect(carried).toContain('quotes.internal.example.com → 127.0.0.1:20443')
    expect(carried).toContain('"budget":"1 GiB"')
    expect(screen.queryByText('appPlatform.declinedPermissions')).toBeNull()
    expect(screen.queryByText('appPlatform.policyReset')).toBeNull()
  })

  it('does not label a network capability whose policy is reset as kept, and lists the reset', () => {
    const both = ['background.run', 'network.http']
    render(<PlatformReviewDetails review={updateReview({ previous_capabilities: both, target_capabilities: both, kept_capabilities: [], kept_proxy: undefined, policy_reset: ['network.http'] })} release={{ ...release, optional_capabilities: ['network.http'] } as unknown as PlatformRelease} groups={[]} expired={false} />)
    expect(screen.queryByText('appPlatform.retainedPermissions')).toBeNull()
    expect(rowText('appPlatform.policyReset')).toContain('network.http')
  })

  it('lists a permission the update drops as removed and not as retained', () => {
    const dropped = { ...release, optional_capabilities: [] } as unknown as PlatformRelease
    render(<PlatformReviewDetails review={updateReview({ target_capabilities: ['background.run'], kept_capabilities: [], kept_proxy: undefined })} release={dropped} groups={[]} expired={false} />)
    expect(screen.queryByText('appPlatform.retainedPermissions')).toBeNull()
  })

  it('shows a newly declared optional permission as declined, not retained', () => {
    render(<PlatformReviewDetails review={updateReview({ previous_capabilities: ['background.run'], target_capabilities: ['background.run'], declined_capabilities: [proxy], kept_capabilities: [], kept_proxy: undefined })} release={release} groups={[]} expired={false} />)
    expect(screen.queryByText('appPlatform.retainedPermissions')).toBeNull()
    expect(rowText('appPlatform.declinedPermissions')).toContain(proxy)
  })
})
