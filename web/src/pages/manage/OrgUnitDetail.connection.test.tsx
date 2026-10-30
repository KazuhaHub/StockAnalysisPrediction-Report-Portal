import { describe, it, expect, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import OrgUnitDetail from './OrgUnitDetail'
import type { OUPolicyResult, OUPolicyResults, UserGroupRow } from '../../api/types'

const post = vi.hoisted(() => vi.fn())
vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn(), patch: vi.fn(), post, del: vi.fn() },
  errText: (e: unknown) => String(e),
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const g = (o: Partial<UserGroupRow>): UserGroupRow => ({ id: 2, name: 'Clients', members: 0, weight: null, ...o }) as UserGroupRow
const result = <T,>(value: T): OUPolicyResult<T> => ({ own: null, effective: value, source_ou: 8, inherited: true, available: true })
const policies: OUPolicyResults = {
  weight: result(2), urgent_unlimited: result(false), allow_urgent: result(true),
  max_queued: result(9), run_window: result(''), restricted: result(false),
  quota: result({ limit: 20, period: 'month' }), totp_enroll: result(false),
  passkey_enroll: result(true), require_2fa: result(true), forecast_scope: result('none'),
}
const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })

describe('OrgUnitDetail parent-move preview while the connection is down', () => {
  it('shows the preview as pending with Save disabled, and asks again on reconnect', async () => {
    post.mockReset().mockRejectedValueOnce(lost()).mockResolvedValue({ inherited_policy_results: policies })
    const def = g({ id: 1, name: 'Default', is_default: true })
    const a = g({ id: 8, name: 'Server parent' })
    const b = g({ id: 9, name: 'Other parent' })
    const target = g({ id: 3, parent_id: 8, policy_results: policies, inherited_policy_results: policies })
    render(<App><OrgUnitDetail sections={['directory']} group={target} groups={[def, a, b, target]} onChanged={vi.fn()} onDeleted={vi.fn()} /></App>)
    const select = screen.getAllByRole('combobox').find((el) => el.closest('.ant-select')?.textContent?.includes('Server parent'))!
    fireEvent.mouseDown(select)
    fireEvent.click(await screen.findByText('Other parent', { selector: '.ant-select-item-option-content' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1), { timeout: 2000 })
    await act(async () => { await Promise.resolve() })
    expect(screen.getByText('ou.previewPending')).toBeTruthy()
    // The current parent's values must not stand in for the unanswered preview.
    expect(screen.queryAllByText(/ou\.inheritedAs/).every((n) => n.textContent?.includes('—'))).toBe(true)
    expect((screen.getByRole('button', { name: /common.save|manage.save/ }) as HTMLButtonElement).disabled).toBe(true)
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    await waitFor(() => expect(screen.queryByText('ou.previewPending')).toBeNull(), { timeout: 2000 })
    expect(post).toHaveBeenCalledTimes(2)
  })
})
