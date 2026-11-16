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
	// expiresMS is the credential's own expiry, stamped by the database; it is compared with the database's
	// clock in platformTaskBodyQuotaRefused, never with this process's.
	expiresMS int64
	until     time.Time
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
		policyRevision: grant.PolicyRevision, epoch: grant.ControllerEpoch, expiresMS: expires, until: until,
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

// platformTaskQuotaRefused answers, from a few live statements and without a transaction or the admission
// lock, whether a submission for a remembered credential would be refused for the installation's quota
// by the full path. It is true only when the full path would reach its quota count and that count is at
// its limit, so every check the full path makes before the count is made here too:
//
//   - the installation is enabled and ready on the remembered generation, policy revision and epoch, the
//     restore is not paused and the controller epoch is the remembered one (one statement, which also
//     carries the active count, whether the occurrence already exists, and whether the activation is
//     still active);
//   - the application's business setup bindings are present;
//   - the handler resolves against the capability grants of the installation's policy revision, and for
//     an event delivery the delivery payload parses and events.subscribe (and reports.read for a report
//     event) are granted.
//
// Any other outcome, including an error, is false and the caller takes the full path, which then gives
// its own answer. One divergence remains by design: publisher trust and the signed descriptor, which the
// full path re-verifies, are not re-read here, so a publisher retired in the last platformTaskRefusalTTL
// is still told 429 by a credential that was verified before that.
func (s *Server) platformTaskQuotaRefused(entry platformTaskRefusalEntry, in platformTaskAdmission) bool {
	var generation, policyRevision int64
	var epoch, appID string
	var active, existing, activations int64
	var current, paused string
	e := s.st.poolExec()
	err := e.queryRow(`SELECT i.app_id,i.generation,i.policy_revision,i.controller_epoch,
		(SELECT COUNT(*) FROM app_platform_tasks t WHERE t.installation_id=i.id AND t.state NOT IN ('succeeded','failed','cancelled')),
		(SELECT COUNT(*) FROM app_platform_tasks t WHERE t.installation_id=i.id AND t.occurrence_id=?),
		(SELECT COUNT(*) FROM app_platform_activations a WHERE a.installation_id=i.id AND a.generation=i.generation AND a.policy_revision=i.policy_revision AND a.state='active'),
		COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),
		COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'')
		FROM app_platform_installations i WHERE i.id=? AND i.desired_state='enabled' AND i.phase='ready'`,
		in.OccurrenceID, entry.installation).Scan(&appID, &generation, &policyRevision, &epoch, &active, &existing, &activations, &current, &paused)
	if err != nil || existing != 0 || activations != 1 || paused != "0" || current != epoch ||
		platformCounterString(generation) != entry.generation ||
		platformCounterString(policyRevision) != entry.policyRevision || epoch != entry.epoch {
		return false
	}
	if active < appplatform.MaxInstallationActiveTasks {
		return false
	}
	if missing, err := platformMissingSetupBindings(e, entry.installation, appID, policyRevision); err != nil || len(missing) > 0 {
		return false
	}
	granted, err := platformTaskGrantedCapabilities(e, entry.installation, platformCounterString(policyRevision))
	if err != nil {
		return false
	}
	resources := []string{"tasks.schedule"}
	if in.Handler == appplatform.TaskHandlerEvent {
		delivery, err := appplatform.ParseEventDelivery(in.Payload)
		if err != nil {
			return false
		}
		resources = append(resources, "events.subscribe")
		if delivery.EventType == appplatform.EventReportIngested {
			resources = append(resources, "reports.read")
		}
	}
	for _, resource := range resources {
		if !granted[resource] {
			return false
		}
	}
	_, err = appplatform.ResolveTaskHandler(appID, in.Handler, granted)
	return err == nil
}
