package app

import (
	"container/list"
	"hash/fnv"
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
	platformPublicStatsInstallations    = 1024
)

// platformPublicLimiter is a fixed-window counter over a bounded, sharded least-recently-used table.
// The shared platform limiter drops its whole table when a flood of distinct keys fills it, which
// hands every other caller a fresh allowance. This one never resets anyone: a full shard evicts its
// least recently used record, so a new key always gets its own allowance and memory stays bounded.
// The cost is the honest one: a source that is idle while more than a shard's capacity of fresh
// sources arrive loses its counter (and so gets a fresh allowance); a source that keeps calling
// keeps its counter. The per installation and backend ceilings do not depend on this table.
type platformPublicLimiter struct {
	once     sync.Once
	shards   []*platformPublicShard
	capacity int
}

type platformPublicShard struct {
	mu   sync.Mutex
	recs map[string]*list.Element
	lru  *list.List // front = most recently used
	max  int
}

type platformPublicRec struct {
	key     string
	n       int
	resetAt time.Time
}

const (
	platformPublicLimiterCapacity = 8192
	platformPublicLimiterShards   = 16
)

func (l *platformPublicLimiter) init() {
	l.once.Do(func() {
		capacity := l.capacity
		if capacity == 0 {
			capacity = platformPublicLimiterCapacity
		}
		shards := min(platformPublicLimiterShards, capacity)
		l.shards = make([]*platformPublicShard, shards)
		for i := range l.shards {
			l.shards[i] = &platformPublicShard{recs: map[string]*list.Element{}, lru: list.New(), max: max(1, capacity/shards)}
		}
	})
}

func (l *platformPublicLimiter) size() int {
	l.init()
	n := 0
	for _, sh := range l.shards {
		sh.mu.Lock()
		n += len(sh.recs)
		sh.mu.Unlock()
	}
	return n
}

// exhausted reports whether key has used its allowance in the current window, without using any.
func (l *platformPublicLimiter) exhausted(key string, limit int, now time.Time) bool {
	l.init()
	h := fnv.New32a()
	h.Write([]byte(key))
	sh := l.shards[int(h.Sum32())%len(l.shards)]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	el := sh.recs[key]
	if el == nil {
		return false
	}
	r := el.Value.(*platformPublicRec)
	return !now.After(r.resetAt) && r.n >= limit
}

func (l *platformPublicLimiter) allow(key string, limit int, window time.Duration, now time.Time) bool {
	l.init()
	h := fnv.New32a()
	h.Write([]byte(key))
	sh := l.shards[int(h.Sum32())%len(l.shards)]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if el := sh.recs[key]; el != nil {
		sh.lru.MoveToFront(el)
		r := el.Value.(*platformPublicRec)
		if now.After(r.resetAt) {
			r.n, r.resetAt = 0, now.Add(window)
		}
		if r.n >= limit {
			return false
		}
		r.n++
		return true
	}
	for len(sh.recs) >= sh.max {
		oldest := sh.lru.Back()
		delete(sh.recs, oldest.Value.(*platformPublicRec).key)
		sh.lru.Remove(oldest)
	}
	sh.recs[key] = sh.lru.PushFront(&platformPublicRec{key: key, n: 1, resetAt: now.Add(window)})
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
	verified                 *platformPublicVerified
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

	// verifyFailed remembers, per installation, the tuple whose release could not be verified and
	// until when not to try again. Only installations that passed the cheap read are keyed.
	verifyFailed map[string]platformPublicFailure

	stopOnce sync.Once
	stop     chan struct{}

	clients       platformPublicLimiter
	installations platformPublicLimiter
}

type platformPublicFailure struct {
	tuple string
	until time.Time
}

// platformPublicVerifyBackoff is how long an unverifiable release is left alone before the next try.
const platformPublicVerifyBackoff = 5 * time.Second

// stopped is closed by shutdown; background loops end on it.
func (p *platformPublicState) stopped() <-chan struct{} {
	p.stopOnce.Do(func() { p.stop = make(chan struct{}) })
	return p.stop
}

func (p *platformPublicState) shutdown() {
	p.stopped()
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
}

// verifyBlocked reports whether the release of this tuple failed verification recently.
func (p *platformPublicState) verifyBlocked(installation, tuple string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, ok := p.verifyFailed[installation]
	return ok && f.tuple == tuple && now.Before(f.until)
}

func (p *platformPublicState) noteVerifyFailure(installation, tuple string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.verifyFailed == nil {
		p.verifyFailed = map[string]platformPublicFailure{}
	}
	p.verifyFailed[installation] = platformPublicFailure{tuple: tuple, until: now.Add(platformPublicVerifyBackoff)}
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
	delete(p.verifyFailed, installation)
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
	if entry.verified != nil {
		// Standing behind every response of its installation, a verified release is the last
		// thing to evict: a hit makes it the newest.
		p.seq++
		entry.seq = p.seq
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
	for entry.verified == nil && p.countLocked(entry.installation) >= platformPublicEntriesPerInstallation {
		if !p.evictOldestLocked(entry.installation, false) {
			return false
		}
	}
	for p.bytes[entry.installation]+entry.size > platformPublicBytesPerInstallation {
		if !p.evictOldestLocked(entry.installation, true) {
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
		if victim == "" || !p.evictOldestLocked(victim, true) {
			return false
		}
	}
	p.entries[entry.key] = entry
	p.bytes[entry.installation] += entry.size
	p.total += entry.size
	return true
}

// countLocked counts the cached responses of an installation. The verified release is not one of
// them: it is bounded by bytes with everything else, but 256 distinct queries cannot push it out.
func (p *platformPublicState) countLocked(installation string) int {
	n := 0
	for _, e := range p.entries {
		if e.installation == installation && e.verified == nil {
			n++
		}
	}
	return n
}

// evictOldestLocked removes the oldest cached response of an installation and, only when it has no
// response left and viewOnly is false, its verified release (a last resort under byte pressure).
func (p *platformPublicState) evictOldestLocked(installation string, verifiedToo bool) bool {
	var oldest *platformPublicEntry
	for _, e := range p.entries {
		if e.installation == installation && e.verified == nil && (oldest == nil || e.seq < oldest.seq) {
			oldest = e
		}
	}
	if oldest == nil && verifiedToo {
		for _, e := range p.entries {
			if e.installation == installation && e.verified != nil {
				oldest = e
			}
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
	// Counters exist only for installations the caller has shown to be real, and no more than
	// platformPublicStatsInstallations of them; past that they share one bucket, so a flood of
	// identifiers cannot grow this table.
	if _, known := p.stats[installation]; !known && len(p.stats) >= platformPublicStatsInstallations {
		installation = ""
	}
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

// takeDue removes and returns every window that has run its course. Called by the timer, so a window
// is written within a minute of its end even when no further request arrives.
func (p *platformPublicState) takeDue(now time.Time) map[string]*platformPublicStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	due := map[string]*platformPublicStats{}
	for installation, st := range p.stats {
		if now.Sub(st.start) >= platformPublicSummaryWindow {
			due[installation] = st
			delete(p.stats, installation)
		}
	}
	return due
}

// takeAll removes every open window (shutdown).
func (p *platformPublicState) takeAll() map[string]*platformPublicStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	all := p.stats
	p.stats = map[string]*platformPublicStats{}
	return all
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

// detail states the window the counts cover: from its start to the earlier of now and its end, so a
// summary never claims more time than it counted.
func (st *platformPublicStats) detail(now time.Time) map[string]any {
	end := now
	if limit := st.start.Add(platformPublicSummaryWindow); limit.Before(end) {
		end = limit
	}
	classes := make([]string, 0, len(st.refusals))
	for class := range st.refusals {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	refusals := map[string]int{}
	for _, class := range classes {
		refusals[class] = st.refusals[class]
	}
	return map[string]any{"window_start": st.start.UTC().Format(time.RFC3339), "window_end": end.UTC().Format(time.RFC3339), "requests": st.requests, "cache_hits": st.hits, "backend_fetches": st.fetches, "refusals": refusals}
}
