package app

import (
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"net"
	"sort"
	"sync"
	"time"
)

// In-memory state of the anonymous public projection (doc 179): the response cache, its
// single-flight, the rate limiters and the traffic counters. The zero value is ready to use.

const (
	platformPublicEntriesPerInstallation = 256
	platformPublicBytesPerInstallation   = 8 << 20
	platformPublicBytesTotal             = 32 << 20
	platformPublicFetchConcurrency       = 4
	// One installation cannot be asked more often than this by all clients together. It bounds the
	// authority reads an anonymous flood can cause; it is not a policy knob.
	platformPublicInstallationPerSecond = 200
	platformPublicSummaryWindow         = 10 * time.Minute
)

// platformPublicLimiter is a fixed-window counter that cannot be used to reset other callers. The
// shared platform limiter drops its whole table when a flood of distinct keys fills it, which hands
// every other caller a fresh allowance; this one keeps the live records and charges keys it has no
// room for to one shared overflow allowance instead.
type platformPublicLimiter struct {
	mu       sync.Mutex
	recs     map[string]*platformPublicRec
	overflow platformPublicRec
	capacity int
}

type platformPublicRec struct {
	n       int
	resetAt time.Time
}

const platformPublicLimiterCapacity = 8192

// platformPublicOverflowPerWindow is what every key without a record shares while the table is full.
const platformPublicOverflowPerWindow = 600

func (l *platformPublicLimiter) allow(key string, limit int, window time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recs == nil {
		l.recs = map[string]*platformPublicRec{}
	}
	capacity := l.capacity
	if capacity == 0 {
		capacity = platformPublicLimiterCapacity
	}
	r := l.recs[key]
	if r == nil {
		if len(l.recs) >= capacity {
			for k, rec := range l.recs {
				if now.After(rec.resetAt) {
					delete(l.recs, k)
				}
			}
		}
		if len(l.recs) >= capacity {
			if now.After(l.overflow.resetAt) {
				l.overflow = platformPublicRec{resetAt: now.Add(window)}
			}
			if l.overflow.n >= platformPublicOverflowPerWindow {
				return false
			}
			l.overflow.n++
			return true
		}
		l.recs[key] = &platformPublicRec{n: 1, resetAt: now.Add(window)}
		return true
	}
	if now.After(r.resetAt) {
		r.n, r.resetAt = 0, now.Add(window)
	}
	if r.n >= limit {
		return false
	}
	r.n++
	return true
}

// platformPublicClientKey narrows an address to what one client can cheaply vary: an IPv6 client
// owns a whole /64, so rotating inside it must not buy a new allowance.
func platformPublicClientKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "ip:" + ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return "ip:" + v4.String()
	}
	return "ip:" + parsed.Mask(net.CIDRMask(64, 128)).String()
}

type platformPublicEntry struct {
	key, installation, tuple string
	body                     []byte
	contentType              string
	files                    map[string]appplatform.FrontendFile
	extra                    int64
	cacheSeconds             int
	expires                  time.Time
	size                     int64
	seq                      uint64
}

type platformPublicFlight struct {
	done  chan struct{}
	entry *platformPublicEntry
	err   error
}

type platformPublicStats struct {
	start                   time.Time
	requests, hits, fetches int
	refusals                map[string]int
}

type platformPublicState struct {
	mu          sync.Mutex
	entries     map[string]*platformPublicEntry
	tuples      map[string]string
	bytes       map[string]int64
	total       int64
	seq         uint64
	flights     map[string]*platformPublicFlight
	fetching    map[string]int
	backendRate map[string]*platformPublicBucket
	stats       map[string]*platformPublicStats

	clients       platformPublicLimiter
	installations platformPublicLimiter
}

type platformPublicBucket struct {
	tokens float64
	at     time.Time
}

func (p *platformPublicState) init() {
	if p.entries == nil {
		p.entries = map[string]*platformPublicEntry{}
		p.tuples = map[string]string{}
		p.bytes = map[string]int64{}
		p.flights = map[string]*platformPublicFlight{}
		p.fetching = map[string]int{}
		p.backendRate = map[string]*platformPublicBucket{}
		p.stats = map[string]*platformPublicStats{}
	}
}

// useTuple records the current identity of an installation's public content. A different tuple
// (generation, policy revision, release or policy) empties that installation's cache first, so a
// response of an old release or policy can never be served.
func (p *platformPublicState) useTuple(installation, tuple string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if old, ok := p.tuples[installation]; ok && old != tuple {
		p.dropLocked(installation)
	}
	p.tuples[installation] = tuple
}

// purge empties an installation's cache and forgets its tuple: disable, policy change, uninstall.
func (p *platformPublicState) purge(installation string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	p.dropLocked(installation)
	delete(p.tuples, installation)
}

func (p *platformPublicState) dropLocked(installation string) {
	for key, entry := range p.entries {
		if entry.installation == installation {
			delete(p.entries, key)
		}
	}
	p.total -= p.bytes[installation]
	delete(p.bytes, installation)
}

// cacheSize reports an installation's live entries and bytes (tests and the audit summary).
func (p *platformPublicState) cacheSize(installation string) (int, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	count := 0
	for _, entry := range p.entries {
		if entry.installation == installation {
			count++
		}
	}
	return count, p.bytes[installation]
}

func (p *platformPublicState) lookup(key string, now time.Time) *platformPublicEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	entry := p.entries[key]
	if entry == nil {
		return nil
	}
	if !now.Before(entry.expires) {
		p.removeLocked(entry)
		return nil
	}
	return entry
}

func (p *platformPublicState) removeLocked(entry *platformPublicEntry) {
	if p.entries[entry.key] == entry {
		delete(p.entries, entry.key)
		p.bytes[entry.installation] -= entry.size
		p.total -= entry.size
	}
}

// store adds an entry inside the per installation and global bounds, evicting the oldest entries of
// the installation first and, for the global bound, from whichever installation holds the most. An
// entry that cannot fit alone is not cached. It reports whether the entry was kept.
func (p *platformPublicState) store(entry *platformPublicEntry, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	entry.size = int64(len(entry.body)) + int64(len(entry.key)) + 64 + entry.extra
	if entry.size > platformPublicBytesPerInstallation {
		return false
	}
	if p.tuples[entry.installation] != entry.tuple {
		return false
	}
	p.seq++
	entry.seq = p.seq
	if old := p.entries[entry.key]; old != nil {
		p.removeLocked(old)
	}
	for _, e := range p.entries {
		if e.installation == entry.installation && !now.Before(e.expires) {
			p.removeLocked(e)
		}
	}
	for p.countLocked(entry.installation) >= platformPublicEntriesPerInstallation || p.bytes[entry.installation]+entry.size > platformPublicBytesPerInstallation {
		if !p.evictOldestLocked(entry.installation) {
			return false
		}
	}
	for p.total+entry.size > platformPublicBytesTotal {
		victim, largest := "", int64(-1)
		for installation, size := range p.bytes {
			if size > largest {
				victim, largest = installation, size
			}
		}
		if victim == "" || !p.evictOldestLocked(victim) {
			return false
		}
	}
	p.entries[entry.key] = entry
	p.bytes[entry.installation] += entry.size
	p.total += entry.size
	return true
}

func (p *platformPublicState) countLocked(installation string) int {
	n := 0
	for _, e := range p.entries {
		if e.installation == installation {
			n++
		}
	}
	return n
}

func (p *platformPublicState) evictOldestLocked(installation string) bool {
	var oldest *platformPublicEntry
	for _, e := range p.entries {
		if e.installation == installation && (oldest == nil || e.seq < oldest.seq) {
			oldest = e
		}
	}
	if oldest == nil {
		return false
	}
	p.removeLocked(oldest)
	return true
}

// flight joins or starts the single fetch of a key. The caller that gets leader=true must call
// finish; everyone else waits on the returned flight.
func (p *platformPublicState) flight(key string) (*platformPublicFlight, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if f := p.flights[key]; f != nil {
		return f, false
	}
	f := &platformPublicFlight{done: make(chan struct{})}
	p.flights[key] = f
	return f, true
}

func (p *platformPublicState) finish(key string, f *platformPublicFlight, entry *platformPublicEntry, err error) {
	p.mu.Lock()
	delete(p.flights, key)
	p.mu.Unlock()
	f.entry, f.err = entry, err
	close(f.done)
}

// beginFetch admits one backend fetch of an installation: at most platformPublicFetchConcurrency at
// once and at most perSecond per second (a token bucket with a burst of perSecond).
func (p *platformPublicState) beginFetch(installation string, perSecond int, now time.Time) (end func(), ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if p.fetching[installation] >= platformPublicFetchConcurrency {
		return nil, false
	}
	b := p.backendRate[installation]
	if b == nil {
		b = &platformPublicBucket{tokens: float64(perSecond), at: now}
		p.backendRate[installation] = b
	}
	b.tokens += now.Sub(b.at).Seconds() * float64(perSecond)
	if b.tokens > float64(perSecond) {
		b.tokens = float64(perSecond)
	}
	b.at = now
	if b.tokens < 1 {
		return nil, false
	}
	b.tokens--
	p.fetching[installation]++
	return func() {
		p.mu.Lock()
		p.fetching[installation]--
		p.mu.Unlock()
	}, true
}

// record counts one request outcome. When the installation's window has run its course the counts
// are returned for audit and a new window starts.
func (p *platformPublicState) record(installation string, now time.Time, class string) (done *platformPublicStats) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	st := p.stats[installation]
	if st != nil && now.Sub(st.start) >= platformPublicSummaryWindow {
		done, st = st, nil
	}
	if st == nil {
		st = &platformPublicStats{start: now, refusals: map[string]int{}}
		p.stats[installation] = st
	}
	switch class {
	case "request":
		st.requests++
	case "hit":
		st.hits++
	case "fetch":
		st.fetches++
	default:
		st.refusals[class]++
	}
	return done
}

// takeStats removes and returns an installation's open window (disable, shutdown, tests).
func (p *platformPublicState) takeStats(installation string) *platformPublicStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	st := p.stats[installation]
	delete(p.stats, installation)
	return st
}

func (st *platformPublicStats) detail(window time.Duration) map[string]any {
	classes := make([]string, 0, len(st.refusals))
	for class := range st.refusals {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	refusals := map[string]int{}
	for _, class := range classes {
		refusals[class] = st.refusals[class]
	}
	return map[string]any{"window_start": st.start.UTC().Format(time.RFC3339), "window_seconds": int(window / time.Second), "requests": st.requests, "cache_hits": st.hits, "backend_fetches": st.fetches, "refusals": refusals}
}
