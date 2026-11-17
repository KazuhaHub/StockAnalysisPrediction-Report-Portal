export interface PublisherNamespace { kind: 'exact' | 'prefix'; value: string }
export interface PublisherKeyInput { id: string; public_key: string; not_before_ms: string; not_after_ms: string | null }
export interface PublisherKey extends PublisherKeyInput { algorithm: 'Ed25519'; revision: string; revoked_ms: string | null }
export interface TrustedPublisher { id: string; enabled: boolean; revision: string; namespaces: PublisherNamespace[]; keys: PublisherKey[] }
export interface PublisherDirectory { initialized: boolean; paused: boolean; publishers: TrustedPublisher[] }
export type PublisherMutation =
  | { action: 'initialize' }
  | { action: 'register'; publisher_id: string; enabled: boolean; namespaces: PublisherNamespace[]; key: PublisherKeyInput }
  | { action: 'policy'; publisher_id: string; expected_revision: string; enabled: boolean; namespaces: PublisherNamespace[] }
  | { action: 'add_key'; publisher_id: string; expected_revision: string; key: PublisherKeyInput }
  | { action: 'revoke_key'; publisher_id: string; expected_revision: string; key_id: string; expected_key_revision: string }

export function publisherPublicBytes(value: string): Uint8Array | null {
  if (!/^[A-Za-z0-9_-]{43}$/.test(value)) return null
  try {
    const raw = atob(value.replace(/-/g, '+').replace(/_/g, '/') + '=')
    if (raw.length !== 32 || btoa(raw).replace(/=/g, '').replace(/\+/g, '-').replace(/\//g, '_') !== value) return null
    return Uint8Array.from(raw, c => c.charCodeAt(0))
  } catch { return null }
}

export async function publisherFingerprint(value: string): Promise<string> {
  const bytes = publisherPublicBytes(value)
  if (!bytes) throw new Error('invalid public key')
  const digest = await crypto.subtle.digest('SHA-256', new Uint8Array(bytes).buffer)
  return Array.from(new Uint8Array(digest), b => b.toString(16).padStart(2, '0')).join(':')
}
