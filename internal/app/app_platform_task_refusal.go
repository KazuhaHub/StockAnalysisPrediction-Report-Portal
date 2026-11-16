package app

import (
	"crypto/sha256"
	"strconv"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformTaskRefusalTTL bounds how long one verified credential is remembered as having been refused
// for its installation's task quota. It is the longest a retired credential can be told 429 instead of
// 404; the live statement that backs every use re-reads the installation's authority, so in practice a
// retirement is seen at once and this limit only covers what that statement does not read (a revoked
// grant that keeps the policy revision, the credential seed, publisher trust).
const platformTaskRefusalTTL = 2 * time.Second

// platformTaskRefusalMax bounds the memory: one entry per recently refused credential.
const platformTaskRefusalMax = 256

type platformTaskRefusalEntry struct {
	installation, generation, policyRevision, epoch string
	until                                           time.Time
}

// platformTaskRefusalCache remembers credentials that were fully verified and then refused for their
// installation's task quota. It can only ever refuse: no admitting path reads it, the database count
// stays the source of truth on every use, and an entry that does not match the live row is dropped and
// the request takes the full path. Process-local and empty at start, so a restart needs no rebuild.
type platformTaskRefusalCache struct {
	mu      sync.Mutex
	entries map[[32]byte]platformTaskRefusalEntry
	now     func() time.Time // test seam; nil is time.Now
}

func platformTaskRefusalKey(raw []byte) [32]byte { return sha256.Sum256(raw) }

func (c *platformTaskRefusalCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *platformTaskRefusalCache) remember(key [32]byte, grant appplatform.ServiceIdentityGrant) {
	expires, err := strconv.ParseInt(grant.ExpiresMS, 10, 64)
	if err != nil {
		return
	}
	now := c.clock()
	until := now.Add(platformTaskRefusalTTL)
	if credential := time.UnixMilli(expires); credential.Before(until) {
		until = credential
	}
	if !until.After(now) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[[32]byte]platformTaskRefusalEntry{}
	}
	if _, ok := c.entries[key]; !ok && len(c.entries) >= platformTaskRefusalMax {
		for k, e := range c.entries {
			if !e.until.After(now) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < platformTaskRefusalMax {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[key] = platformTaskRefusalEntry{
		installation: grant.InstallationID, generation: grant.Generation,
		policyRevision: grant.PolicyRevision, epoch: grant.ControllerEpoch, until: until,
	}
}

func (c *platformTaskRefusalCache) lookup(key [32]byte, installation string) (platformTaskRefusalEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return e, false
	}
	if e.installation != installation || !e.until.After(c.clock()) {
		delete(c.entries, key)
		return e, false
	}
	return e, true
}

func (c *platformTaskRefusalCache) forget(key [32]byte) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

func (c *platformTaskRefusalCache) reset() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

func (c *platformTaskRefusalCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// platformTaskQuotaRefused answers, from one live statement and without a transaction or the admission
// lock, whether a submission for a remembered credential is refused for the installation's quota. It
// is true only when the installation is still enabled and ready on the remembered generation, policy
// revision and epoch, the restore is not paused, the occurrence does not already exist (a replay or a
// conflict is answered by the full path) and the installation's active task count is at its limit. Any
// other outcome, including an error, is false and the caller takes the full path.
func (s *Server) platformTaskQuotaRefused(entry platformTaskRefusalEntry, occurrence string) bool {
	var generation, policyRevision int64
	var epoch string
	var active, existing int64
	var current, paused string
	err := s.st.poolExec().queryRow(`SELECT i.generation,i.policy_revision,i.controller_epoch,
		(SELECT COUNT(*) FROM app_platform_tasks t WHERE t.installation_id=i.id AND t.state NOT IN ('succeeded','failed','cancelled')),
		(SELECT COUNT(*) FROM app_platform_tasks t WHERE t.installation_id=i.id AND t.occurrence_id=?),
		COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),
		COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'')
		FROM app_platform_installations i WHERE i.id=? AND i.desired_state='enabled' AND i.phase='ready'`,
		occurrence, entry.installation).Scan(&generation, &policyRevision, &epoch, &active, &existing, &current, &paused)
	if err != nil || existing != 0 || paused != "0" || current != epoch ||
		platformCounterString(generation) != entry.generation ||
		platformCounterString(policyRevision) != entry.policyRevision || epoch != entry.epoch {
		return false
	}
	return active >= appplatform.MaxInstallationActiveTasks
}
