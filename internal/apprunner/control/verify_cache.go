package control

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// Verification of a signed command parses strict JSON, recomputes a logical digest and checks an
// Ed25519 signature, and every layer of a nested command repeats that for the layers beneath it.
// The same signed bytes are checked again and again: a signer checks its own output, and every
// consumer of the result checks it once more. A successful verification is therefore remembered,
// process-locally, for exactly as long as it stays true.
//
// An entry is keyed by a digest over everything verification depends on: which verifier ran, the
// raw document, the signature and the whole verifying authority (deployment, controller epoch,
// control revision, fingerprint and public key). A rotated key or a new control revision is a
// different key, so it is verified again and refused on its own merits; no entry can answer for a
// different authority. Only successes are stored, so a failure is always recomputed.
//
// An entry is served only for a clock sample inside [verified at, earliest expiry of any layer):
// never past the document's own validity window, and never to a clock earlier than the one that
// proved the not-before bound.

// VerifyCacheCapacity bounds the number of remembered verifications.
const VerifyCacheCapacity = 256

type verifyCacheEntry struct {
	key        [sha256.Size]byte
	value      any
	verifiedAt int64
	expires    int64
}

type verifyCache struct {
	mu           sync.Mutex
	capacity     int
	order        *list.List // front = most recently used
	entries      map[[sha256.Size]byte]*list.Element
	hits, misses uint64
}

func newVerifyCache(capacity int) *verifyCache {
	return &verifyCache{capacity: capacity, order: list.New(), entries: map[[sha256.Size]byte]*list.Element{}}
}

var verifiedCommands = newVerifyCache(VerifyCacheCapacity)

// VerifyCacheStats reports lookups served from the cache, verifications performed, and entries held.
func VerifyCacheStats() (hits, misses uint64, size int) {
	c := verifiedCommands
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.order.Len()
}

func (c *verifyCache) get(key [sha256.Size]byte, nowMS int64) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		e := el.Value.(*verifyCacheEntry)
		if nowMS >= e.verifiedAt && nowMS < e.expires {
			c.order.MoveToFront(el)
			c.hits++
			return e.value, true
		}
		if nowMS >= e.expires {
			c.order.Remove(el)
			delete(c.entries, key)
		}
	}
	c.misses++
	return nil, false
}

func (c *verifyCache) put(key [sha256.Size]byte, value any, verifiedAt, expires int64) {
	if expires <= verifiedAt {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		e := el.Value.(*verifyCacheEntry)
		e.value, e.verifiedAt, e.expires = value, verifiedAt, expires
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(&verifyCacheEntry{key: key, value: value, verifiedAt: verifiedAt, expires: expires})
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*verifyCacheEntry).key)
	}
}

func verifyCacheKey(kind string, raw, signature []byte, a Authority) [sha256.Size]byte {
	h := sha256.New()
	part := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	part([]byte(kind))
	part(raw)
	part(signature)
	part([]byte(a.DeploymentID))
	part([]byte(a.ControllerEpoch))
	part([]byte(a.Fingerprint))
	var revision [8]byte
	binary.BigEndian.PutUint64(revision[:], uint64(a.ControlRevision))
	part(revision[:])
	part(a.PublicKey)
	var key [sha256.Size]byte
	h.Sum(key[:0])
	return key
}

// cachedVerify runs verify only when no still-valid success is remembered. verify returns the
// verified value and the earliest expiry (Unix ms) among the document and everything nested in it.
func cachedVerify[V any](kind string, raw, signature []byte, a Authority, nowMS int64, verify func() (V, int64, error)) (V, error) {
	key := verifyCacheKey(kind, raw, signature, a)
	if v, ok := verifiedCommands.get(key, nowMS); ok {
		return v.(V), nil
	}
	v, expires, err := verify()
	if err != nil {
		var zero V
		return zero, err
	}
	verifiedCommands.put(key, v, nowMS, expires)
	return v, nil
}

// expiryMS is the instant a command's window closes, or 0 (never cached) when it cannot be read.
func expiryMS(expiresMS string) int64 {
	n, ok := counter(expiresMS, false)
	if !ok {
		return 0
	}
	return n
}
