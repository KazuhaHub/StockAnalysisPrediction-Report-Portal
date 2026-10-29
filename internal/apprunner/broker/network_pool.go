package broker

import (
	"bytes"
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// NetworkPool is owned once by the trusted broker service, not by an application
// or each request. It bounds concurrent signed invocations and cached executors.
// It is in-memory scheduling state, not grants, durable replay or a service lease.
// Reconstructing pools per request would bypass these limits and is forbidden.
type NetworkPool struct {
	slots    chan struct{}
	current  CurrentNetworkAuthority
	mu       sync.Mutex
	entries  map[networkExecutorKey]*networkExecutorEntry
	capacity int
	create   func(HTTPPolicy) (*HTTP, error)
	now      func() time.Time
}

type networkExecutorKey struct {
	deployment, epoch, control, installation, review, generation, revision, release, policy string
}
type networkExecutorEntry struct {
	executor *HTTP
	refs     int
	idleAt   time.Time
}
type networkExecutorLease struct {
	pool     *NetworkPool
	entry    *networkExecutorEntry
	released atomic.Bool
}

func (n *networkExecutorLease) release() {
	if n == nil || !n.released.CompareAndSwap(false, true) {
		return
	}
	n.pool.mu.Lock()
	defer n.pool.mu.Unlock()
	n.entry.refs--
	if n.entry.refs == 0 {
		n.entry.idleAt = n.pool.now()
	}
}

// Bounds are trusted infrastructure settings, never request/package fields.
// Current authority is mandatory for all calls, including cached executors.
func NewNetworkPool(active, entries int, current CurrentNetworkAuthority) (*NetworkPool, error) {
	if active < 1 || active > 8 || entries < 1 || entries > 32 || current == nil {
		return nil, ErrDenied
	}
	return &NetworkPool{slots: make(chan struct{}, active), current: current, entries: make(map[networkExecutorKey]*networkExecutorEntry), capacity: entries, now: time.Now, create: func(p HTTPPolicy) (*HTTP, error) {
		// Each signed invocation supplies the actual viewer-aware online check to
		// the shared executor through NetworkCall. No authority result is cached.
		return NewHTTP(p, func(context.Context) error { return nil })
	}}, nil
}

func (p *NetworkPool) Do(ctx context.Context, raw, signature, body []byte, a control.Authority) (HTTPResponse, error) {
	var zero HTTPResponse
	if p == nil || p.current == nil || p.slots == nil || ctx == nil {
		return zero, ErrDenied
	}
	if ctx.Err() != nil {
		return zero, ErrUnavailable
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return zero, ErrUnavailable
	}
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(body) > 65536 {
		return zero, ErrDenied
	}
	raw, signature, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(body)
	defer clear(body)
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, a, p.now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPBody(verified, body) != nil {
		return zero, ErrDenied
	}
	command := verified.Command()
	policy, err := appplatform.ParseHTTPPolicy(command.PolicyRaw)
	if err != nil {
		return zero, ErrDenied
	}
	expires, err := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if err != nil {
		return zero, ErrDenied
	}
	deadline := p.now().Add(policy.Policy.Deadline)
	if signed := time.UnixMilli(expires); signed.Before(deadline) {
		deadline = signed
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	// Refuse stale/revoked claims before allocating a cache entry. NetworkCall
	// additionally repeats current authority at all three actual IO checkpoints.
	current, err := p.current(ctx, verified.Command())
	if ctx.Err() != nil {
		return zero, ErrUnavailable
	}
	if err != nil {
		return zero, ErrDenied
	}
	if _, err := control.VerifyNetworkHTTPRequest(raw, signature, current, p.now().UnixMilli()); err != nil {
		return zero, ErrDenied
	}
	if ctx.Err() != nil {
		return zero, ErrUnavailable
	}
	lease, err := p.acquire(verified.Command())
	if err != nil {
		return zero, err
	}
	defer lease.release()
	call, err := NewNetworkCall(lease.entry.executor, raw, signature, body, current, p.current)
	if err != nil {
		return zero, ErrDenied
	}
	return call.Do(ctx)
}

func (p *NetworkPool) acquire(c control.NetworkHTTPRequestCommand) (*networkExecutorLease, error) {
	policy, err := appplatform.ParseHTTPPolicy(c.PolicyRaw)
	if err != nil || policy.SHA256 != c.PolicySHA256 {
		return nil, ErrDenied
	}
	key := networkExecutorKey{c.DeploymentID, c.ControllerEpoch, c.ControlRevision, c.InstallationID, c.ReviewPlanID, c.Generation, c.PolicyRevision, c.ReleaseSHA256, c.PolicySHA256}
	p.mu.Lock()
	defer p.mu.Unlock()
	// Old and new policy/epoch executors for an installation share the current
	// request's installation budget; changing keys cannot multiply active slots.
	active := 0
	for k, entry := range p.entries {
		if k.deployment == key.deployment && k.installation == key.installation {
			active += entry.refs
		}
	}
	if active >= policy.Policy.Concurrency {
		return nil, ErrUnavailable
	}
	if entry := p.entries[key]; entry != nil {
		entry.refs++
		return &networkExecutorLease{pool: p, entry: entry}, nil
	}
	var victim networkExecutorKey
	if len(p.entries) >= p.capacity {
		found := false
		var oldest time.Time
		for k, entry := range p.entries {
			if entry.refs == 0 && (!found || entry.idleAt.Before(oldest)) {
				victim = k
				oldest = entry.idleAt
				found = true
			}
		}
		if !found {
			return nil, ErrUnavailable
		}
	}
	executor, err := p.create(policy.Policy)
	if err != nil {
		return nil, ErrUnavailable
	}
	if len(p.entries) >= p.capacity {
		delete(p.entries, victim)
	}
	entry := &networkExecutorEntry{executor: executor, refs: 1}
	p.entries[key] = entry
	return &networkExecutorLease{pool: p, entry: entry}, nil
}
