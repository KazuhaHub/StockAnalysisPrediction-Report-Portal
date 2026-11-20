package app

import (
	"sort"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// In-memory state of the machine read API (doc 183): the limiters, the concurrency counters, the
// traffic counters, and the caches that keep a request from costing more than one indexed read. The
// zero value is ready to use. Nothing here is authority: every request re-reads the token, the
// installation and the grant from the database.

const (
	platformMachineSourcePerMinute = 120
	platformMachineSummaryWindow   = 10 * time.Minute
	platformMachineStatsTokens     = 1024
	platformMachineTouchEvery      = time.Minute
	platformMachineDescriptors     = 64
	platformMachineSourceKeys      = 4096
)

type platformMachineStats struct {
	start                         time.Time
	installation, handle          string
	requests, backendCalls, bytes int
	refusals                      map[string]int
}

// platformMachineDeclared is the machine block of a release with the identity the OpenAPI document
// names. A release is immutable (its identifier is the digest of its descriptor), so the parsed block
// is kept by that identifier; whether the release is the current one is read on every request.
type platformMachineDescriptor struct {
	machine         appplatform.Machine
	appID, version  string
	backendDeclared bool
}

type platformMachineState struct {
	// clients is the pre-auth ceiling per source, tokens the per-minute limit of a token and
	// installations the per-second limit of an installation.
	clients       platformPublicLimiter
	tokens        platformPublicLimiter
	installations platformPublicLimiter
	// slots caps the concurrent database reads a caller can cause before it is authenticated.
	slots platformAuthoritySlots

	mu          sync.Mutex
	inflight    map[string]int
	stats       map[string]*platformMachineStats
	touched     map[string]time.Time
	sourceDay   map[string]string
	descriptors map[string]platformMachineDescriptor
	order       []string
	support     platformMachineSupportState

	stopOnce sync.Once
	stop     chan struct{}
}

func (p *platformMachineState) stopped() <-chan struct{} {
	p.stopOnce.Do(func() { p.stop = make(chan struct{}) })
	return p.stop
}

func (p *platformMachineState) shutdown() {
	p.stopped()
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
}

func (p *platformMachineState) init() {
	if p.inflight == nil {
		p.inflight = map[string]int{}
		p.stats = map[string]*platformMachineStats{}
		p.touched = map[string]time.Time{}
		p.sourceDay = map[string]string{}
		p.descriptors = map[string]platformMachineDescriptor{}
	}
}

// beginBackend admits one backend call of an installation: at most limit at once.
func (p *platformMachineState) beginBackend(installation string, limit int) (end func(), ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if p.inflight[installation] >= limit {
		return nil, false
	}
	p.inflight[installation]++
	return func() {
		p.mu.Lock()
		p.inflight[installation]--
		if p.inflight[installation] <= 0 {
			delete(p.inflight, installation)
		}
		p.mu.Unlock()
	}, true
}

func (p *platformMachineState) concurrent(installation string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflight[installation]
}

func (p *platformMachineState) descriptor(release string) (platformMachineDescriptor, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	d, ok := p.descriptors[release]
	return d, ok
}

func (p *platformMachineState) keepDescriptor(release string, d platformMachineDescriptor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if _, ok := p.descriptors[release]; !ok {
		for len(p.order) >= platformMachineDescriptors {
			delete(p.descriptors, p.order[0])
			p.order = p.order[1:]
		}
		p.order = append(p.order, release)
	}
	p.descriptors[release] = d
}

// shouldTouch reports whether a token's last-used time is due: at most once a minute per token.
func (p *platformMachineState) shouldTouch(token string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if last, ok := p.touched[token]; ok && now.Sub(last) < platformMachineTouchEvery {
		return false
	}
	if len(p.touched) >= platformMachineStatsTokens {
		clear(p.touched)
	}
	p.touched[token] = now
	return true
}

// firstSourceToday reports whether this is the first request of the token in this process today, and
// remembers it. The caller still checks the audit record, so a restart does not write a second row.
func (p *platformMachineState) firstSourceToday(token string, now time.Time) bool {
	day := now.UTC().Format("2006-01-02")
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if p.sourceDay[token] == day {
		return false
	}
	if len(p.sourceDay) >= platformMachineSourceKeys {
		clear(p.sourceDay)
	}
	p.sourceDay[token] = day
	return true
}

// record counts one outcome. token is the authenticated token's identifier; an empty one is the shared
// bucket of requests that did not authenticate. When the token's window has run its course the counts
// are returned for audit and a new window starts.
func (p *platformMachineState) record(token, installation, handle string, now time.Time, class string, n int) (done *platformMachineStats) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if _, known := p.stats[token]; !known && len(p.stats) >= platformMachineStatsTokens {
		token, installation, handle = "", "", ""
	}
	st := p.stats[token]
	if st != nil && now.Sub(st.start) >= platformMachineSummaryWindow {
		done, st = st, nil
	}
	if st == nil {
		st = &platformMachineStats{start: now, installation: installation, handle: handle, refusals: map[string]int{}}
		p.stats[token] = st
	}
	switch class {
	case "request":
		st.requests++
	case "backend":
		st.backendCalls++
	case "bytes":
		st.bytes += n
	default:
		st.refusals[class]++
	}
	return done
}

func (p *platformMachineState) takeDue(now time.Time) []*platformMachineStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	var due []*platformMachineStats
	for token, st := range p.stats {
		if now.Sub(st.start) >= platformMachineSummaryWindow {
			due = append(due, st)
			delete(p.stats, token)
		}
	}
	return due
}

func (p *platformMachineState) takeAll() []*platformMachineStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	var all []*platformMachineStats
	for _, st := range p.stats {
		all = append(all, st)
	}
	p.stats = map[string]*platformMachineStats{}
	return all
}

// takeInstallation removes and returns the open windows of one installation's tokens.
func (p *platformMachineState) takeInstallation(installation string) []*platformMachineStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	var out []*platformMachineStats
	for token, st := range p.stats {
		if st.installation == installation && token != "" {
			out = append(out, st)
			delete(p.stats, token)
		}
	}
	return out
}

func (st *platformMachineStats) detail(now time.Time) map[string]any {
	end := now
	if limit := st.start.Add(platformMachineSummaryWindow); limit.Before(end) {
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
	out := map[string]any{"window_start": st.start.UTC().Format(time.RFC3339), "window_end": end.UTC().Format(time.RFC3339), "requests": st.requests, "backend_calls": st.backendCalls, "bytes_out": st.bytes, "refusals": refusals}
	if st.handle != "" {
		out["handle"] = st.handle
	}
	return out
}
