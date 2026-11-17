package app

import (
	"sync"
	"time"
)

// Read-path costs (doc 171). A page load is its statements times the database's round trip, and a write on the
// path adds a disk flush; on the reference VM, a KVM guest, both are slow, so the read paths do as little as the
// semantics allow.

// sessionStampCache remembers, in memory, the activity stamp a session last had, so the requests in between do not
// read it from the database. The stamp is due once per lastSeenInterval and the UPDATE keeps its own condition, so
// the stored value is unchanged; a restart simply reads once. Bounded: it is emptied when it grows past its limit.
type sessionStampCache struct {
	mu sync.Mutex
	m  map[string]int64
}

const sessionStampMax = 4096

// fresh reports whether the remembered stamp of the session is newer than stale, i.e. no stamp is due.
func (c *sessionStampCache) fresh(id string, stale int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[id] >= stale && c.m[id] != 0
}

func (c *sessionStampCache) set(id string, stamp int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= sessionStampMax {
		c.m = map[string]int64{}
	}
	c.m[id] = stamp
}

func (c *sessionStampCache) reset() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

// ReportFacets is NewTypes, ReportKinds and ReportVersionsPresent in one statement: the home page's three filter
// lists, scoped like them, each ordered like them.
func (s *Store) ReportFacets(sc *ownerScope) (types, kinds, versions []string) {
	frag, args := sc.where("")
	tail := ""
	if frag != "" {
		tail = " AND " + frag
	}
	var all []any
	for range 3 {
		all = append(all, args...)
	}
	rows, err := s.readQuery("SELECT 1 AS f, rtype AS v FROM reports WHERE rtype<>''"+tail+
		" UNION SELECT 2, kind FROM reports WHERE kind<>''"+tail+
		" UNION SELECT 3, version FROM reports WHERE version<>''"+tail+" ORDER BY 1, 2", all...)
	if err != nil {
		return nil, nil, nil
	}
	defer rows.Close()
	for rows.Next() {
		var f int
		var v string
		if rows.Scan(&f, &v) != nil {
			continue
		}
		switch f {
		case 1:
			types = append(types, v)
		case 2:
			kinds = append(kinds, v)
		case 3:
			versions = append(versions, v)
		}
	}
	return types, kinds, versions
}

// homeConfigTTL backs the in-memory drop every writer does with a time limit, for a write made by another
// process (a restored backup, a second replica).
const homeConfigTTL = 10 * time.Second

type homeConfigSnapshot struct {
	links  []Link
	groups []LinkGroup
	colors map[string]string
	at     time.Time
}

// HomeConfig is the entry buttons, their groups and the kind colours the home page shows, from memory when they
// were read less than homeConfigTTL ago and no writer has dropped them since. Callers must not modify the result.
func (s *Store) HomeConfig() ([]Link, []LinkGroup, map[string]string) {
	now := time.Now
	if s.homeConfigNow != nil {
		now = s.homeConfigNow
	}
	s.homeConfigMu.Lock()
	cached := s.homeConfigVal
	s.homeConfigMu.Unlock()
	if cached != nil && now().Sub(cached.at) < homeConfigTTL {
		return cached.links, cached.groups, cached.colors
	}
	fresh := &homeConfigSnapshot{links: s.Links(), groups: s.LinkGroups(), colors: s.KindColors(), at: now()}
	s.homeConfigMu.Lock()
	s.homeConfigVal = fresh
	s.homeConfigMu.Unlock()
	return fresh.links, fresh.groups, fresh.colors
}

// dropHomeConfig is called by every writer of links, link groups and kind colours.
func (s *Store) dropHomeConfig() {
	s.homeConfigMu.Lock()
	s.homeConfigVal = nil
	s.homeConfigMu.Unlock()
}

// WriteReportReadAudit records a report read. Audit records stay durable: the row commits synchronously like every
// other audit row, and no synchronous_commit setting is used, not even one scoped to a statement. It is the same
// insert WriteAudit issues, kept as its own entry point so that the report-read path is named at its one call site.
func (s *Store) WriteReportReadAudit(e AuditEntry) {
	s.WriteAudit(e)
}
