import type { AppDisplay } from '../lib/descriptor-contract.generated'

export interface PlatformRelease {
  release_sha256: string
  app_id: string
  publisher_id: string
  version: string
  trust_revision: string
  display?: AppDisplay
  installable: boolean
  reason?: string
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
