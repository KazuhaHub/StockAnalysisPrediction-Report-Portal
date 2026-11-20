package app

import (
	"sort"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// In-memory state of authenticated inbound ingress (doc 180): the limiters, the nonce cache, the
// parsed descriptor routes and the traffic counters. Nothing here is written to the database by a
// request; the summary is written by the timer. The zero value is ready to use.

const (
	// Step 2 of the contract: the ceilings that apply before anything is looked up.
	platformIngressClientPerMinute       = 120
	platformIngressInstallationPerMinute = 600
	// Step 5: a nonce seen inside the window is a replay; the cache is bounded per installation.
	platformIngressNonceEntries = 4096
	// A request refused after the installation is known shares these counters; before it is known,
	// one global bucket does, so a flood of identifiers cannot grow the table.
	platformIngressSummaryWindow      = 10 * time.Minute
	platformIngressStatsInstallations = 1024
	platformIngressDescriptorEntries  = 256
	platformIngressEndpointsPerWindow = 64
)

// platformIngressCounters is what one endpoint has heard in the open traffic window (doc 180 section
// 5): shown in the binding's console detail, never an address.
type platformIngressCounters struct {
	WindowStart string `json:"window_start"`
	Accepted    int    `json:"accepted"`
	Refused     int    `json:"refused"`
}

type platformIngressStats struct {
	start                                          time.Time
	requests, accepted, forwarded, backendFailures int
	refusals                                       map[string]int
	endpoints                                      map[string]*platformIngressCounters
}

type platformIngressNonce struct {
	key string
	at  time.Time
}

type platformIngressNonces struct {
	seen  map[string]time.Time
	queue []platformIngressNonce
	head  int
}

// platformIngressDeclaration is what a release says about ingress, parsed once per release (a
// release is content addressed, so the answer never changes for its digest).
type platformIngressDeclaration struct {
	declares bool
	routes   []appplatform.IngressRoute
}

type platformIngressState struct {
	mu          sync.Mutex
	nonces      map[string]*platformIngressNonces
	stats       map[string]*platformIngressStats
	finished    map[string][]*platformIngressStats
	descriptors map[string]platformIngressDeclaration

	stopOnce sync.Once
	stop     chan struct{}

	clients       platformPublicLimiter
	installations platformPublicLimiter
	endpoints     platformPublicLimiter
}

// stopped is closed by shutdown; the summary loop ends on it.
func (p *platformIngressState) stopped() <-chan struct{} {
	p.stopOnce.Do(func() { p.stop = make(chan struct{}) })
	return p.stop
}

func (p *platformIngressState) shutdown() {
	p.stopped()
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
}

func (p *platformIngressState) initLocked() {
	if p.nonces == nil {
		p.nonces = map[string]*platformIngressNonces{}
		p.stats = map[string]*platformIngressStats{}
		p.finished = map[string][]*platformIngressStats{}
		p.descriptors = map[string]platformIngressDeclaration{}
	}
}

// claimNonce records (endpoint, nonce digest) for the window and reports whether it is new. The
// oldest entry is evicted when the installation's cache is full, so an installation can never grow
// the table, and an entry never outlives the window. Only an authenticated request reaches it.
func (p *platformIngressState) claimNonce(installation, endpoint, nonceDigest string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	n := p.nonces[installation]
	if n == nil {
		n = &platformIngressNonces{seen: map[string]time.Time{}}
		p.nonces[installation] = n
	}
	for n.head < len(n.queue) && !now.Before(n.queue[n.head].at.Add(appplatform.IngressNonceWindow)) {
		if n.seen[n.queue[n.head].key] == n.queue[n.head].at {
			delete(n.seen, n.queue[n.head].key)
		}
		n.head++
	}
	key := endpoint + "\x00" + nonceDigest
	if at, ok := n.seen[key]; ok && now.Before(at.Add(appplatform.IngressNonceWindow)) {
		return false
	}
	for len(n.seen) >= platformIngressNonceEntries && n.head < len(n.queue) {
		if n.seen[n.queue[n.head].key] == n.queue[n.head].at {
			delete(n.seen, n.queue[n.head].key)
		}
		n.head++
	}
	if n.head > 1024 && n.head > len(n.queue)/2 {
		n.queue = append([]platformIngressNonce(nil), n.queue[n.head:]...)
		n.head = 0
	}
	n.seen[key] = now
	n.queue = append(n.queue, platformIngressNonce{key, now})
	return true
}

// nonceCount is the live entries of an installation (tests).
func (p *platformIngressState) nonceCount(installation string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	if n := p.nonces[installation]; n != nil {
		return len(n.seen)
	}
	return 0
}

// declaration returns the parsed ingress block of a release, if it has been read before.
func (p *platformIngressState) declaration(release string) (platformIngressDeclaration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	d, ok := p.descriptors[release]
	return d, ok
}

func (p *platformIngressState) storeDeclaration(release string, d platformIngressDeclaration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	if len(p.descriptors) >= platformIngressDescriptorEntries {
		clear(p.descriptors)
	}
	p.descriptors[release] = d
}

// forget drops what is held for an installation: a disabled capability, an uninstall.
func (p *platformIngressState) forget(installation string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	delete(p.nonces, installation)
}

// record counts one outcome. class is "request", "accepted", "forwarded", "backend_failure" or a
// refusal class; endpoint is empty until the binding is known to be real. A window that has run
// its course moves to the finished list for the timer to write, never to a request.
func (p *platformIngressState) record(installation, endpoint string, now time.Time, class string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	if _, known := p.stats[installation]; !known && len(p.stats) >= platformIngressStatsInstallations {
		installation, endpoint = "", ""
	}
	st := p.stats[installation]
	if st != nil && now.Sub(st.start) >= platformIngressSummaryWindow {
		p.finished[installation] = append(p.finished[installation], st)
		st = nil
	}
	if st == nil {
		st = &platformIngressStats{start: now, refusals: map[string]int{}, endpoints: map[string]*platformIngressCounters{}}
		p.stats[installation] = st
	}
	var counters *platformIngressCounters
	if endpoint != "" {
		if counters = st.endpoints[endpoint]; counters == nil && len(st.endpoints) < platformIngressEndpointsPerWindow {
			counters = &platformIngressCounters{WindowStart: st.start.UTC().Format(time.RFC3339)}
			st.endpoints[endpoint] = counters
		}
	}
	switch class {
	case "request":
		st.requests++
	case "accepted":
		st.accepted++
		if counters != nil {
			counters.Accepted++
		}
	case "forwarded":
		st.forwarded++
	case "backend_failure":
		st.backendFailures++
		if counters != nil {
			counters.Refused++
		}
	default:
		st.refusals[class]++
		if counters != nil {
			counters.Refused++
		}
	}
}

// counters is a copy of what an endpoint has heard in the installation's open window, or nil.
func (p *platformIngressState) counters(installation, endpoint string) *platformIngressCounters {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	if st := p.stats[installation]; st != nil {
		if c := st.endpoints[endpoint]; c != nil {
			copied := *c
			return &copied
		}
	}
	return nil
}

type platformIngressDue struct {
	installation string
	stats        *platformIngressStats
}

// takeDue removes and returns every window that has run its course.
func (p *platformIngressState) takeDue(now time.Time) []platformIngressDue {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	var due []platformIngressDue
	for installation, list := range p.finished {
		for _, st := range list {
			due = append(due, platformIngressDue{installation, st})
		}
		delete(p.finished, installation)
	}
	for installation, st := range p.stats {
		if now.Sub(st.start) >= platformIngressSummaryWindow {
			due = append(due, platformIngressDue{installation, st})
			delete(p.stats, installation)
		}
	}
	return due
}

// takeAll removes every open window (shutdown, tests).
func (p *platformIngressState) takeAll() []platformIngressDue {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	var due []platformIngressDue
	for installation, list := range p.finished {
		for _, st := range list {
			due = append(due, platformIngressDue{installation, st})
		}
	}
	for installation, st := range p.stats {
		due = append(due, platformIngressDue{installation, st})
	}
	p.finished, p.stats = map[string][]*platformIngressStats{}, map[string]*platformIngressStats{}
	return due
}

// detail states the window the counts cover: from its start to the earlier of now and its end.
func (st *platformIngressStats) detail(now time.Time) map[string]any {
	end := now
	if limit := st.start.Add(platformIngressSummaryWindow); limit.Before(end) {
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
	endpoints := map[string]map[string]int{}
	for name, c := range st.endpoints {
		endpoints[name] = map[string]int{"accepted": c.Accepted, "refused": c.Refused}
	}
	return map[string]any{"window_start": st.start.UTC().Format(time.RFC3339), "window_end": end.UTC().Format(time.RFC3339), "requests": st.requests,
		"accepted": st.accepted, "forwarded": st.forwarded, "backend_failures": st.backendFailures, "refusals": refusals, "endpoints": endpoints}
}
