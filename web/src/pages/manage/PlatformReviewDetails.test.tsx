import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import PlatformReviewDetails from './PlatformReviewDetails'
import type { PlatformRelease } from '../../api/appPlatform'
import type { Review } from './platformLifecycle'

const mock = vi.hoisted(() => ({ t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mock.t }) }))

const sha = (c: string) => c.repeat(64)
const proxy = 'network.proxy'
const updateReview = (extra: Record<string, unknown>): Review => ({
  plan_id: '22222222-2222-4222-8222-222222222222', plan_sha256: sha('b'), expires_ms: String(Date.now() + 900000),
  review: {
    action: 'update', installation_id: '11111111-1111-4111-8111-111111111111', app_id: 'market-monitor', publisher_id: 'kazuha',
    source_release_sha256: sha('c'), release_sha256: sha('a'), source_generation: '2', target_generation: '3', policy_revision: '3', target_policy_revision: '4',
    declined_capabilities: [], previous_capabilities: ['background.run', proxy], target_capabilities: ['background.run', proxy],
    declared_data_schema: '1', grants_sha256: sha('d'), ...extra,
  },
} as unknown as Review)
const release = { required_capabilities: ['background.run'], optional_capabilities: [proxy], version: '1.2.0' } as unknown as PlatformRelease

describe('PlatformReviewDetails optional permissions on update', () => {
  afterEach(cleanup)

  it('shows a kept optional permission as retained, neither declined nor removed', () => {
    render(<PlatformReviewDetails review={updateReview({})} release={release} groups={[]} expired={false} />)
    const label = screen.getByText('appPlatform.retainedPermissions')
    expect(label.closest('.ant-descriptions-row, tr')?.textContent).toContain(proxy)
    expect(screen.queryByText('appPlatform.declinedPermissions')).toBeNull()
    const diff = screen.getByText('appPlatform.permissionDiff')
    expect(diff).toBeTruthy()
  })

  it('lists a permission the update drops as removed and not as retained', () => {
    const dropped = { ...release, optional_capabilities: [] } as unknown as PlatformRelease
    render(<PlatformReviewDetails review={updateReview({ target_capabilities: ['background.run'] })} release={dropped} groups={[]} expired={false} />)
    expect(screen.queryByText('appPlatform.retainedPermissions')).toBeNull()
  })

  it('shows a newly declared optional permission as declined, not retained', () => {
    render(<PlatformReviewDetails review={updateReview({ previous_capabilities: ['background.run'], target_capabilities: ['background.run'], declined_capabilities: [proxy] })} release={release} groups={[]} expired={false} />)
    expect(screen.queryByText('appPlatform.retainedPermissions')).toBeNull()
    expect(screen.getByText('appPlatform.declinedPermissions').closest('.ant-descriptions-row, tr')?.textContent).toContain(proxy)
  })
})
