import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen } from '@testing-library/react'
import { InstalledOperation } from './appStoreParts'
import { RESULT_BADGE_MS } from './operationSummary'
import type { PlatformOperationRow } from '../../api/appOperations'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k, i18n: { language: 'en-US' } }), initReactI18next: { type: '3rdParty', init: () => undefined } }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }), useOptionalPrefs: () => ({ lang: 'en-US' }) }))

const row = (completedAgo: number): PlatformOperationRow => ({
  operation_id: '11111111-1111-4111-8111-111111111111', installation_id: '22222222-2222-4222-8222-222222222222', action: 'install', stage: 'complete', managed: false,
  revision: '9', terminal: true, created_ms: String(Date.now() - completedAgo - 5000), updated_ms: String(Date.now() - completedAgo), completed_ms: String(Date.now() - completedAgo),
})

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { cleanup(); vi.useRealTimers() })

describe('the success badge on a card that stays open', () => {
  it('drops after 24 hours without a reload', async () => {
    render(<InstalledOperation operation={row(RESULT_BADGE_MS - 90_000)} onOpenOperation={() => undefined} />)
    expect(screen.getByRole('button', { name: /appPlatform\.ops\.succeeded/ })).toBeTruthy()
    await act(async () => { await vi.advanceTimersByTimeAsync(3 * 60_000) })
    expect(screen.queryByRole('button', { name: /appPlatform\.ops\.succeeded/ })).toBeNull()
  })

  it('never drops a failure', async () => {
    render(<InstalledOperation operation={{ ...row(0), stage: 'failed', error_code: 'capacity_exhausted_disk' }} onOpenOperation={() => undefined} />)
    await act(async () => { await vi.advanceTimersByTimeAsync(2 * RESULT_BADGE_MS) })
    expect(screen.getByRole('button', { name: /appPlatform\.ops\.failed/ })).toBeTruthy()
  })
})
