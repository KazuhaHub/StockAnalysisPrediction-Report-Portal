package broker

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const MaxNetworkPoolActive = 8
const MaxNetworkPoolEntries = 32

// NetworkPool is owned once by the trusted broker service, not by an application
// or each request. It bounds concurrent signed invocations and cached executors.
// It is in-memory scheduling state, not grants, durable replay or a service lease.
// Reconstructing pools per request would bypass these limits and is forbidden.
type NetworkPool struct {
	slots chan struct{}
	// background is the share of slots that service (no-viewer) calls may hold:
	// active/2, nil when that is 0. Every background call takes one here and one
	// in slots, so foreground always keeps at least half of the active slots.
	background chan struct{}
	current    CurrentNetworkAuthority
	mu         sync.Mutex
	entries    map[networkExecutorKey]*networkExecutorEntry
	capacity   int
	create     func(HTTPPolicy) (*HTTP, error)
	// createPolicy builds the executor of a v2 policy from its exact bytes and the
	// pool's private-network allowlist. v1 policies keep using create.
	createPolicy func([]byte, []appplatform.PrivateNetwork) (*HTTP, error)
	// networks is the runner's operator-set allowlist and networksSHA its digest;
	// a command with a private target must name exactly this digest.
	networks    []appplatform.PrivateNetwork
	networksSHA string
	now         func() time.Time
}

// NetworkPoolOption configures trusted infrastructure state of a pool. Like the
// bounds, options are never request or package fields.
type NetworkPoolOption func(*NetworkPool) error

// WithPrivateNetworks gives the pool the runner's private-network allowlist.
// Without it (or with an empty list) no command with a private target is served.
func WithPrivateNetworks(networks []appplatform.PrivateNetwork) NetworkPoolOption {
	return func(p *NetworkPool) error {
		sum, err := appplatform.PrivateNetworksSHA256(networks)
		if err != nil {
			return ErrDenied
		}
		p.networks = slices.Clone(networks)
		for i := range p.networks {
			p.networks[i].Ports = slices.Clone(p.networks[i].Ports)
		}
		p.networksSHA = sum
		return nil
	}
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
func NewNetworkPool(active, entries int, current CurrentNetworkAuthority, options ...NetworkPoolOption) (*NetworkPool, error) {
	if active < 1 || active > MaxNetworkPoolActive || entries < 1 || entries > MaxNetworkPoolEntries || current == nil {
		return nil, ErrDenied
	}
	var background chan struct{}
	if active/2 > 0 {
		background = make(chan struct{}, active/2)
	}
	pool := &NetworkPool{slots: make(chan struct{}, active), background: background, current: current, entries: make(map[networkExecutorKey]*networkExecutorEntry), capacity: entries, now: time.Now, create: func(p HTTPPolicy) (*HTTP, error) {
		// Each signed invocation supplies the actual viewer-aware online check to
		// the shared executor through NetworkCall. No authority result is cached.
		return NewHTTP(p, func(context.Context) error { return nil })
	}, createPolicy: func(raw []byte, networks []appplatform.PrivateNetwork) (*HTTP, error) {
		return NewHTTPPolicy(raw, networks, func(context.Context) error { return nil })
	}}
	if err := WithPrivateNetworks(nil)(pool); err != nil {
		return nil, ErrDenied
	}
	for _, option := range options {
		if option == nil || option(pool) != nil {
			return nil, ErrDenied
		}
	}
	return pool, nil
}

// Do runs one signed foreground invocation.
func (p *NetworkPool) Do(ctx context.Context, raw, signature, body []byte, a control.Authority) (HTTPResponse, error) {
	return p.do(ctx, raw, signature, body, a, false)
}

// DoBackground runs one signed invocation made without a viewer (a service
// egress session). It is the same executor, checks and per-installation budget
// as Do; it differs only in admission: it needs a slot of the background share
// (at most half of the active slots; none when there is only one) and reports a
// full share, a full pool or a spent installation budget as ErrBusy, which the
// caller may retry. A pool with no background share reports ErrUnavailable.
func (p *NetworkPool) DoBackground(ctx context.Context, raw, signature, body []byte, a control.Authority) (HTTPResponse, error) {
	return p.do(ctx, raw, signature, body, a, true)
}

func (p *NetworkPool) do(ctx context.Context, raw, signature, body []byte, a control.Authority, background bool) (HTTPResponse, error) {
	var zero HTTPResponse
	if p == nil || p.current == nil || p.slots == nil || ctx == nil {
		return zero, ErrDenied
	}
	if ctx.Err() != nil {
		return zero, ErrUnavailable
	}
	full := ErrUnavailable
	if background {
		if p.background == nil {
			return zero, ErrUnavailable
		}
		full = ErrBusy
		select {
		case p.background <- struct{}{}:
			defer func() { <-p.background }()
		default:
			return zero, ErrBusy
		}
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return zero, full
	}
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(body) > 65536 {
		return zero, ErrDenied
	}
	raw, signature, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(body)
	defer clear(body)
	// The signed bytes and every decoded copy carry the credential value, if any.
	defer clear(raw)
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, a, p.now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPBody(verified, body) != nil {
		return zero, ErrDenied
	}
	defer verified.Clear()
	command := verified.Command()
	defer clear(command.CredentialValue)
	defer command.ClearSignedHeaders()
	view := verified.Command()
	defer clear(view.CredentialValue)
	defer view.ClearSignedHeaders()
	policy, err := appplatform.ParseHTTPPolicyAny(command.PolicyRaw)
	if err != nil {
		return zero, ErrDenied
	}
	target, err := policy.Policy.Select(command.Method, command.URL)
	if err != nil {
		return zero, ErrDenied
	}
	expires, err := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if err != nil {
		return zero, ErrDenied
	}
	deadline := p.now().Add(target.Deadline)
	if signed := time.UnixMilli(expires); signed.Before(deadline) {
		deadline = signed
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	// Refuse stale/revoked claims before allocating a cache entry. NetworkCall
	// additionally repeats current authority at all three actual IO checkpoints.
	current, err := p.current(ctx, view)
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
	lease, err := p.acquireWith(view, full)
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
	return p.acquireWith(c, ErrUnavailable)
}

// acquireWith is acquire with the error reported for a spent per-installation
// budget: ErrUnavailable for foreground, ErrBusy for background.
func (p *NetworkPool) acquireWith(c control.NetworkHTTPRequestCommand, spent error) (*networkExecutorLease, error) {
	policy, err := appplatform.ParseHTTPPolicyAny(c.PolicyRaw)
	if err != nil || policy.SHA256 != c.PolicySHA256 {
		return nil, ErrDenied
	}
	// A private target is only served against the allowlist the command was
	// reviewed with; an empty allowlist serves none. Nothing is allocated before.
	private := policy.Policy.HasPrivate()
	if private && (len(p.networks) == 0 || c.PrivateNetworksSHA256 != p.networksSHA) || !private && c.PrivateNetworksSHA256 != "" {
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
		return nil, spent
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
	var executor *HTTP
	if policy.Version == 1 {
		v1, parseErr := appplatform.ParseHTTPPolicy(c.PolicyRaw)
		if parseErr != nil {
			return nil, ErrDenied
		}
		executor, err = p.create(v1.Policy)
	} else {
		executor, err = p.createPolicy(c.PolicyRaw, p.networks)
	}
	if err != nil || executor == nil {
		return nil, ErrUnavailable
	}
	if len(p.entries) >= p.capacity {
		delete(p.entries, victim)
	}
	entry := &networkExecutorEntry{executor: executor, refs: 1}
	p.entries[key] = entry
	return &networkExecutorLease{pool: p, entry: entry}, nil
}
