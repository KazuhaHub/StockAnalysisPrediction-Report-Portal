import type { AppDisplay } from '../lib/descriptor-contract.generated'
import { parseAppDisplay } from '../lib/appDisplayV2'

export interface PlatformRelease {
  release_sha256: string
  app_id: string
  publisher_id: string
  version: string
  trust_revision: string
  display?: AppDisplay
  compatibility?: {
    platform_api: string
    runner_protocol: string
  }
  platforms?: string[]
  required_capabilities?: string[]
  optional_capabilities?: string[]
  resources?: {
    memory_mib: number
    cpu_millis: number
    pids: number
  }
  data_schema?: number
  backend?: boolean
  installable: boolean
  reason?: string
  installation_id?: string
  current_release_sha256?: string
  updateable?: boolean
  enableable?: boolean
  installed?: boolean
  // A first install that failed after its candidate started: only an uninstall review clears it.
  uninstallable?: boolean
}

export interface PlatformMarketHint {
  app_id: string
  publisher_id: string
  version: string
  descriptor_sha256: string
  descriptor_url: string
  signature_url: string
}

const digest = /^[0-9a-f]{64}$/
const slug = /^[a-z][a-z0-9-]{0,63}$/
function object(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}
export function parsePlatformMarket(value: unknown): PlatformMarketHint[] | null {
  if (!object(value) || value.publisher_verified !== false || typeof value.catalogue_sha256 !== 'string' || !digest.test(value.catalogue_sha256) || !Array.isArray(value.releases) || value.releases.length > 256) return null
  const seen = new Set<string>()
  for (const item of value.releases) {
    if (!object(item) || typeof item.app_id !== 'string' || !slug.test(item.app_id) || typeof item.publisher_id !== 'string' || !slug.test(item.publisher_id) || typeof item.version !== 'string' || !item.version || item.version.length > 128 || typeof item.descriptor_sha256 !== 'string' || !digest.test(item.descriptor_sha256) || seen.has(item.descriptor_sha256) || typeof item.descriptor_url !== 'string' || typeof item.signature_url !== 'string') return null
    seen.add(item.descriptor_sha256)
  }
  // These remain plain-text discovery hints. Do not treat this shape validation
  // as signature/trust verification, or use returned URLs to fetch in the browser.
  return value.releases as PlatformMarketHint[]
}

/**
 * The state of staging a catalogue package: the answer of POST and GET /market/stage. A package comes down in the
 * background, so a start answers `downloading` (202) when it is not done in a moment and the caller polls the GET
 * until `staged` or `failed`. A synchronous 200 body carries no state and reads as staged.
 */
export type PlatformStageStatus =
  | { state: 'staged'; release_sha256: string }
  | { state: 'downloading'; release_sha256: string; bytes_done: number; bytes_total: number | null }
  | { state: 'failed'; release_sha256: string; error_code: string }

const counter = /^(0|[1-9][0-9]{0,15})$/
export function parsePlatformStageStatus(value: unknown): PlatformStageStatus | null {
  if (!object(value) || typeof value.release_sha256 !== 'string' || !digest.test(value.release_sha256)) return null
  const release_sha256 = value.release_sha256
  if (value.state === undefined || value.state === 'staged') return { state: 'staged', release_sha256 }
  if (value.state === 'failed') return typeof value.error_code === 'string' && value.error_code ? { state: 'failed', release_sha256, error_code: value.error_code } : null
  if (value.state !== 'downloading' || typeof value.bytes_done !== 'string' || !counter.test(value.bytes_done)) return null
  // The total is empty until the server has announced a size.
  if (typeof value.bytes_total !== 'string' || (value.bytes_total !== '' && !counter.test(value.bytes_total))) return null
  return { state: 'downloading', release_sha256, bytes_done: Number(value.bytes_done), bytes_total: value.bytes_total === '' ? null : Number(value.bytes_total) }
}

export interface PlatformMarketVersion {
  version: string
  descriptor_sha256: string
}

/** One application of a catalogue: its versions newest first, and how to show it when the server could verify it. */
export interface PlatformMarketApp {
  publisher_id: string
  app_id: string
  versions: PlatformMarketVersion[]
  display?: AppDisplay
  publisher_trusted: boolean
}

/**
 * Reads the `apps` of a catalogue response. The server has already checked any display against publisher trust;
 * the shape is still checked here and a display that does not parse is dropped, never shown. An older server
 * without `apps` yields an empty list.
 */
export function parsePlatformMarketApps(value: unknown): PlatformMarketApp[] {
  if (!object(value) || !Array.isArray(value.apps) || value.apps.length > 256) return []
  const apps: PlatformMarketApp[] = []
  for (const item of value.apps) {
    if (!object(item) || typeof item.app_id !== 'string' || !slug.test(item.app_id) || typeof item.publisher_id !== 'string' || !slug.test(item.publisher_id) || !Array.isArray(item.versions) || item.versions.length === 0) continue
    const versions: PlatformMarketVersion[] = []
    for (const v of item.versions) {
      if (!object(v) || typeof v.version !== 'string' || !v.version || v.version.length > 128 || typeof v.descriptor_sha256 !== 'string' || !digest.test(v.descriptor_sha256)) continue
      versions.push({ version: v.version, descriptor_sha256: v.descriptor_sha256 })
    }
    if (versions.length !== item.versions.length) continue
    const display = item.display === undefined || item.display === null ? undefined : parseAppDisplay(item.display) ?? undefined
    apps.push({ publisher_id: item.publisher_id, app_id: item.app_id, versions, display, publisher_trusted: item.publisher_trusted === true })
  }
  return apps
}
