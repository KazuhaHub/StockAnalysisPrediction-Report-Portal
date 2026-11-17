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

// NetworkStreamPool owns the shared concurrency and executor cache for ordinary
// HTTPS response streams. It is separate from buffered HTTP authority so a
// stream policy cannot widen or reuse a buffered executor.
type NetworkStreamPool struct {
	slots    chan struct{}
	current  CurrentNetworkAuthority
	mu       sync.Mutex
	entries  map[networkExecutorKey]*networkStreamExecutorEntry
	capacity int
	create   func(HTTPStreamPolicy) (*HTTPStream, error)
	now      func() time.Time
}

type networkStreamExecutorEntry struct {
	executor *HTTPStream
	refs     int
	idleAt   time.Time
}

type networkStreamExecutorLease struct {
	pool     *NetworkStreamPool
	entry    *networkStreamExecutorEntry
	released atomic.Bool
}

func (n *networkStreamExecutorLease) release() {
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

func NewNetworkStreamPool(active, entries int, current CurrentNetworkAuthority) (*NetworkStreamPool, error) {
	if active < 1 || active > MaxNetworkPoolActive || entries < 1 || entries > MaxNetworkPoolEntries || current == nil {
		return nil, ErrDenied
	}
	return &NetworkStreamPool{
		slots:    make(chan struct{}, active),
		current:  current,
		entries:  make(map[networkExecutorKey]*networkStreamExecutorEntry),
		capacity: entries,
		now:      time.Now,
		create: func(p HTTPStreamPolicy) (*HTTPStream, error) {
			return NewHTTPStream(p, func(context.Context) error { return nil })
		},
	}, nil
}

func (p *NetworkStreamPool) Do(
	ctx context.Context,
	raw, signature, body []byte,
	a control.Authority,
	extra func(context.Context) error,
	callbacks HTTPStreamCallbacks,
) (HTTPStreamResult, error) {
	var zero HTTPStreamResult
	if p == nil || p.current == nil || p.slots == nil || ctx == nil || callbacks.Start == nil {
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
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(body) > appplatform.MaxHTTPRequestBytes {
		return zero, ErrDenied
	}
	raw, signature, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(body)
	defer clear(body)
	verified, err := control.VerifyNetworkHTTPStreamRequest(raw, signature, a, p.now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPStreamBody(verified, body) != nil {
		return zero, ErrDenied
	}
	defer verified.Clear()
	command := verified.Command()
	defer clear(command.CredentialValue)
	policy, err := appplatform.ParseHTTPStreamPolicy(command.PolicyRaw)
	if err != nil {
		return zero, ErrDenied
	}
	expires, err := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if err != nil {
		return zero, ErrDenied
	}
	deadline := p.now().Add(policy.Policy.Duration)
	if signed := time.UnixMilli(expires); signed.Before(deadline) {
		deadline = signed
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	current, err := p.current(ctx, verified.Command())
	if ctx.Err() != nil {
		return zero, ErrUnavailable
	}
	if err != nil {
		return zero, ErrDenied
	}
	if _, err := control.VerifyNetworkHTTPStreamRequest(raw, signature, current, p.now().UnixMilli()); err != nil {
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
	verify := func(checkCtx context.Context) error {
		currentCommand := verified.Command()
		defer clear(currentCommand.CredentialValue)
		current, err := p.current(checkCtx, currentCommand)
		if err != nil {
			return ErrDenied
		}
		if _, err := control.VerifyNetworkHTTPStreamRequest(raw, signature, current, p.now().UnixMilli()); err != nil {
			return ErrDenied
		}
		if extra != nil {
			return extra(checkCtx)
		}
		return nil
	}
	return lease.entry.executor.Do(ctx, HTTPRequest{
		Method:      command.Method,
		URL:         command.URL,
		Accept:      command.Accept,
		ContentType: command.ContentType,
		Body:        body,
		HeaderName:  command.CredentialHeader,
		HeaderValue: command.CredentialValue,
	}, verify, callbacks)
}

func (p *NetworkStreamPool) acquire(c control.NetworkHTTPRequestCommand) (*networkStreamExecutorLease, error) {
	policy, err := appplatform.ParseHTTPStreamPolicy(c.PolicyRaw)
	if err != nil || policy.SHA256 != c.PolicySHA256 {
		return nil, ErrDenied
	}
	key := networkExecutorKey{c.DeploymentID, c.ControllerEpoch, c.ControlRevision, c.InstallationID, c.ReviewPlanID, c.Generation, c.PolicyRevision, c.ReleaseSHA256, c.PolicySHA256}
	p.mu.Lock()
	defer p.mu.Unlock()
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
		return &networkStreamExecutorLease{pool: p, entry: entry}, nil
	}
	var victim networkExecutorKey
	if len(p.entries) >= p.capacity {
		found := false
		var oldest time.Time
		for candidate, entry := range p.entries {
			if entry.refs == 0 && (!found || entry.idleAt.Before(oldest)) {
				victim = candidate
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
	entry := &networkStreamExecutorEntry{executor: executor, refs: 1}
	p.entries[key] = entry
	return &networkStreamExecutorLease{pool: p, entry: entry}, nil
}
