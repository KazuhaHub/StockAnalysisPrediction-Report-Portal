import { vi } from 'vitest'
import { screen } from '@testing-library/react'
import { layout } from './appsAdminFixtures'
import { DESCRIPTOR_LOCALES } from '../../lib/descriptor-contract.generated'
import type { AppLayoutEntry } from '../../api/types'

// Shared data and API stand-in for the tests of the installed page and the install page.
export const root = '/api/admin/app-platform'
export const installationId = '33333333-3333-4333-8333-333333333333'
export const plan = '22222222-2222-4222-8222-222222222222'
export const operation = '11111111-1111-4111-8111-111111111111'
export const planSha = 'd'.repeat(64)
export const sha = (c: string) => c.repeat(64)
export const catalogueUrl = 'https://market.example/index.json'
export const display = (name: string, description: string) => ({ icon: 'file-text', texts: DESCRIPTOR_LOCALES.map(locale => ({ locale, name, description })) })
export const notesApp = { publisher_id: 'kazuha', app_id: 'notes', versions: [{ version: '1.1.0', descriptor_sha256: sha('b') }, { version: '1.0.0', descriptor_sha256: sha('a') }], display: display('Notes', 'Personal notes'), publisher_trusted: true }
export const chartsApp = { publisher_id: 'kazuha', app_id: 'charts', versions: [{ version: '2.0.0', descriptor_sha256: sha('c') }, { version: '2.0.0-beta.1', descriptor_sha256: sha('e') }], display: display('Charts', 'Draw charts'), publisher_trusted: true }
export const strangerApp = { publisher_id: 'stranger', app_id: 'tools', versions: [{ version: '0.1.0', descriptor_sha256: sha('f') }], publisher_trusted: false }
export const catalogue = (apps: unknown[] = [notesApp, chartsApp, strangerApp]) => ({ catalogue_sha256: sha('9'), publisher_verified: false, apps, releases: (apps as typeof notesApp[]).flatMap(a => a.versions.map(v => ({ app_id: a.app_id, publisher_id: a.publisher_id, version: v.version, descriptor_sha256: v.descriptor_sha256, descriptor_url: 'https://market.example/d.json', signature_url: 'https://market.example/d.sig' }))) })
export const platformEntry = (state: string, version = '1.0.0'): AppLayoutEntry => ({ key: `platform:${installationId}`, kind: 'platform', name: 'Notes', builtin: false, icon: 'file-text', installation_id: installationId, app_id: 'notes', publisher_id: 'kazuha', state, version, access: [] })
export const verifiedRow = (c: string, version: string, extra: Record<string, unknown> = {}) => ({ release_sha256: sha(c), app_id: c === 'b' || c === 'a' ? 'notes' : 'charts', publisher_id: 'kazuha', version, trust_revision: '1', installable: true, display: display('Notes', 'Personal notes'), ...extra })
export const planFor = (action: string, release: string, app = 'notes') => ({ plan_id: plan, plan_sha256: planSha, expires_ms: String(Date.now() + 900000), review: action === 'install'
  ? { action, app_id: app, publisher_id: 'kazuha', release_sha256: release, declined_capabilities: [], open_to_ous: [] }
  : { action, installation_id: installationId, app_id: app, publisher_id: 'kazuha', release_sha256: release, generation: '1', target_generation: '2', source_generation: '1', managed: false, data_effect: 'preserve', source_release_sha256: sha('a'), policy_revision: '1', target_policy_revision: '1', declined_capabilities: [], previous_capabilities: [], target_capabilities: [], declared_data_schema: '1', grants_sha256: planSha, plugin_data_cleared: true, managed_data_cleared: false } })


export const fx: { entries: AppLayoutEntry[]; catalogueResponse: () => unknown; releaseRows: Record<string, Record<string, unknown>> } = {
  entries: [], catalogueResponse: () => catalogue(), releaseRows: {},
}

type Mocks = Record<'get' | 'post' | 'put' | 'postBytes', ReturnType<typeof vi.fn>>
/** Resets the fixtures and installs the API stand-in; call it from beforeEach. */
export function resetFixtures(apiMock: Mocks) {
  fx.entries = layout()
  fx.catalogueResponse = () => catalogue()
  fx.releaseRows = { [sha('b')]: verifiedRow('b', '1.1.0'), [sha('a')]: verifiedRow('a', '1.0.0', { installation_id: installationId, installed: true, installable: false }), [sha('c')]: verifiedRow('c', '2.0.0') }
  for (const m of Object.values(apiMock)) m.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/apps/layout') return { apps: fx.entries }
    if (url === `${root}/market/sources`) return { official: { name: 'Kazuha Hub', url: catalogueUrl, enabled: true } }
    if (url === `${root}/storage-status`) return {}
    if (url === `${root}/subjects?kind=ou`) return { items: [{ id: '7', name: 'Research' }], next_cursor: '' }
    if (url === `${root}/installations/${installationId}/settings`) return { current_release_sha256: sha('a'), version: '1.0.0' }
    if (url.startsWith(`${root}/releases?release_sha256=`)) { const row = fx.releaseRows[url.split('=')[1]]; return { items: row ? [row] : [], next_cursor: '' } }
    if (url.startsWith(`${root}/operations/`)) return { operation_id: operation, installation_id: installationId, action: 'install', stage: 'queued', terminal: false, control_paused: false }
    return { items: [], next_cursor: '' }
  })
  apiMock.post.mockImplementation(async (url: string, body: { action?: string; release_sha256?: string }) => {
    if (url === `${root}/market/catalogue`) return fx.catalogueResponse()
    if (url === `${root}/market/stage`) return { release_sha256: body.release_sha256 }
    if (url === `${root}/plans`) return planFor(body.action!, body.release_sha256 ?? sha('a'))
    if (url === `${root}/operations`) return { operation_id: operation }
    throw new Error(`unexpected ${url}`)
  })
  apiMock.put.mockResolvedValue({ apps: layout() })
}

export const cardOf = (name: string) => screen.getAllByText(name).map(n => n.closest('.rp-app-card') as HTMLElement).find(Boolean) as HTMLElement
export const withPlatform = (state: string, version = '1.0.0') => { fx.entries = [...layout(), platformEntry(state, version)] }
