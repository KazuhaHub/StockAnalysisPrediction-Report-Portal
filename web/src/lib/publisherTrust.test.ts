import { describe, expect, it } from 'vitest'
import { publisherFingerprint, publisherPublicBytes } from './publisherTrust'

describe('publisher public key review', () => {
  it('hashes the decoded 32-byte key rather than its textual representation', async () => {
    expect(await publisherFingerprint('A'.repeat(43))).toBe('66:68:7a:ad:f8:62:bd:77:6c:8f:c1:8b:8e:9f:8e:20:08:97:14:85:6e:e2:33:b3:90:2a:59:1d:0d:5f:29:25')
  })

  it.each(['', 'A'.repeat(42), 'A'.repeat(44), 'A'.repeat(43) + '=', 'A'.repeat(42) + 'B', 'A'.repeat(86), '-----BEGIN PUBLIC KEY-----', ' A'.repeat(43)])('rejects a noncanonical key: %s', value => {
    expect(publisherPublicBytes(value)).toBeNull()
  })
})
