package app

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The session-free task route authenticates a backend with a service identity whose MAC only the database
// can check: the seed is sealed in the installation's row, and unsealing it costs a transaction, about ten
// statements and a signature check. An unauthenticated caller must not be able to make the Portal do that,
// so the Portal keeps the seed of an installation in memory after a full verification has succeeded and
// refuses a forged credential from that. Material is only ever a reason to refuse: a credential whose MAC
// is valid still goes through the full live verification, so authority (the installation's state, its
// generation, policy revision and epoch, its grants, publisher trust) is never read from it.
//
// Material is keyed by the installation alone, not by its generation, policy revision and epoch. The seed
// is written once when the installation is created and never rotated, and the installation id is never
// reused, so keying on facts that move would only hand an attacker more ways to miss the table: the MAC
// covers those fields, so a credential that names a stale or invented value still fails the MAC, and one
// whose MAC is valid reaches the live verification, which compares them.

// platformServiceMaterialTTL is how long material is kept after the last full verification that
// succeeded. A successful verification renews it, so material of a backend that submits at least that
// often stays warm; one that does not is verified in full again under platformUnverifiedVerifications.
const platformServiceMaterialTTL = 30 * time.Second

// platformServiceMaterialMax bounds the memory: one 32-byte seed per installation. The platform admits at
// most 50 installations (installation_capacity), so this never evicts a live one.
const platformServiceMaterialMax = 128

// platformUnverifiedVerifications is how many full verifications of credentials with no material may run
// at once, Portal-wide. A verification is about 2 ms of database work on PostgreSQL, so two admit a
// thousand cold verifications a second, far more than 50 installations ever need, and bound what an
// unauthenticated flood can hold of the pool to two connections (on SQLite, to the one writer, once).
// When both are taken the request is answered 429 app_platform_busy at once: it never waits.
const platformUnverifiedVerifications = 2

type platformServiceMaterial struct {
	seed  [32]byte
	until time.Time
}

// platformServiceMaterialCache holds platformServiceMaterial by installation. Seeds are zeroed when an
// entry expires, is replaced or is evicted, and are never logged.
type platformServiceMaterialCache struct {
	mu      sync.Mutex
	entries map[string]*platformServiceMaterial
	now     func() time.Time // test seam; nil is time.Now
}

func (c *platformServiceMaterialCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// remember keeps (or renews) the material of an installation after a successful full verification.
func (c *platformServiceMaterialCache) remember(installation string, seed []byte) {
	if len(seed) != 32 {
		return
	}
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*platformServiceMaterial{}
	}
	if old, ok := c.entries[installation]; ok {
		copy(old.seed[:], seed)
		old.until = now.Add(platformServiceMaterialTTL)
		return
	}
	for key, entry := range c.entries {
		if !entry.until.After(now) {
			c.evictLocked(key)
		}
	}
	for key := range c.entries {
		if len(c.entries) < platformServiceMaterialMax {
			break
		}
		c.evictLocked(key)
	}
	entry := &platformServiceMaterial{until: now.Add(platformServiceMaterialTTL)}
	copy(entry.seed[:], seed)
	c.entries[installation] = entry
}

func (c *platformServiceMaterialCache) evictLocked(installation string) {
	if entry, ok := c.entries[installation]; ok {
		clear(entry.seed[:])
		delete(c.entries, installation)
	}
}

// material returns a copy of the seed of an installation, which the caller clears, and whether there is
// live material for it.
func (c *platformServiceMaterialCache) material(installation string) ([32]byte, bool) {
	var seed [32]byte
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[installation]
	if !ok {
		return seed, false
	}
	if !entry.until.After(c.clock()) {
		c.evictLocked(installation)
		return seed, false
	}
	return entry.seed, true
}

func (c *platformServiceMaterialCache) forget(installation string) {
	c.mu.Lock()
	c.evictLocked(installation)
	c.mu.Unlock()
}

func (c *platformServiceMaterialCache) reset() {
	c.mu.Lock()
	for key := range c.entries {
		c.evictLocked(key)
	}
	c.mu.Unlock()
}

func (c *platformServiceMaterialCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// platformServiceCredentialInstallation reads the installation a credential names, without the database
// and without trusting it: it is only used to find material, and a credential that names another
// installation than the path is refused by the full verification anyway.
func platformServiceCredentialInstallation(raw []byte) (string, bool) {
	var probe struct {
		InstallationID string `json:"installation_id"`
	}
	if json.Unmarshal(raw, &probe) != nil || !platformValidUUID(probe.InstallationID) {
		return "", false
	}
	return probe.InstallationID, true
}

// authentic reports, from memory, whether a credential is a canonical identity with a valid MAC under the
// installation's material. known is false when there is no live material, in which case nothing is said
// about the credential.
func (c *platformServiceMaterialCache) authentic(installation string, raw []byte) (known, ok bool) {
	seed, known := c.material(installation)
	if !known {
		return false, false
	}
	defer clear(seed[:])
	return true, appplatform.ServiceIdentityAuthentic(raw, seed[:])
}
