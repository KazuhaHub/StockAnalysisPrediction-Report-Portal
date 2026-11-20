package broker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform/proxyforwarder"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

// The runner side of the reviewed forward proxy (doc 184 section 5.5). A tunnel is a
// PRXY0001 stream on an installation's broker socket: admission, host match,
// resolution under the address guard, a pinned dial, the first-record server name
// check, then a byte splice with accounting. Nothing here decides authority: the
// transport layer hands in the policy of the installation's current egress session
// and cuts every tunnel when that authority ends.

// ProxyStreamMagic is the eight bytes that start a proxy stream on the broker socket.
const ProxyStreamMagic = proxyforwarder.OpenMagic

// Close reasons of a tunnel, as counted in the hourly rollup.
const (
	ProxyCloseEOF         = "eof"
	ProxyCloseIdle        = "idle"
	ProxyCloseLifetime    = "lifetime"
	ProxyCloseBudget      = "budget"
	ProxyCloseSession     = "session"
	ProxyClosePolicy      = "policy"
	ProxyCloseSNIMismatch = "sni_mismatch"
	ProxyCloseError       = "error"
)

// ProxyLimits are the platform-fixed limits of one installation's tunnels. The
// transport layer builds them from the signed session command; tests shorten the
// timers.
type ProxyLimits struct {
	Tunnels     int
	NewPerMin   int
	Burst       int
	Idle        time.Duration
	Lifetime    time.Duration
	Connect     time.Duration
	FirstRecord time.Duration
	Mbit        int
}

// DefaultProxyLimits are the values of doc 184 section 6.
func DefaultProxyLimits() ProxyLimits {
	return ProxyLimits{
		Tunnels: appplatform.ProxyTunnelsPerInstallation, NewPerMin: appplatform.ProxyNewPerMinute, Burst: appplatform.ProxyNewBurst,
		Idle: appplatform.ProxyIdleSeconds * time.Second, Lifetime: appplatform.ProxyLifetimeSeconds * time.Second,
		Connect: appplatform.ProxyConnectTimeoutSeconds * time.Second, FirstRecord: appplatform.ProxyFirstRecordSeconds * time.Second,
		Mbit: appplatform.ProxyMbitPerSecond,
	}
}

// ProxyBudget counts the bytes an installation moved in a UTC day. The runner
// keeps it where it survives a restart; the in-memory one below does not.
type ProxyBudget interface {
	// Used is the bytes counted for the installation on day (days since the epoch).
	Used(installation string, day int64) int64
	// Add counts n more bytes and returns the new total.
	Add(installation string, day int64, n int64) int64
}

type memoryBudget struct {
	mu   sync.Mutex
	used map[string]map[int64]int64
}

// NewMemoryProxyBudget is a budget held in memory only.
func NewMemoryProxyBudget() ProxyBudget { return &memoryBudget{used: map[string]map[int64]int64{}} }

func (b *memoryBudget) Used(installation string, day int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used[installation][day]
}

func (b *memoryBudget) Add(installation string, day int64, n int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	days := b.used[installation]
	if days == nil {
		days = map[int64]int64{}
		b.used[installation] = days
	}
	for old := range days {
		if old < day-1 {
			delete(days, old)
		}
	}
	days[day] += n
	return days[day]
}

// ProxyRunner is what every installation's tunnels share: the runner-wide tunnel
// cap, the budget store and the network seams.
type ProxyRunner struct {
	mu     sync.Mutex
	open   int
	max    int
	budget ProxyBudget

	Now        func() time.Time
	Lookup     func(ctx context.Context, host string) ([]netip.Addr, error)
	Dial       func(ctx context.Context, network, address string) (net.Conn, error)
	Interfaces func() ([]hostnet.Interface, error)
}

// NewProxyRunner builds the runner-wide state: at most maxTunnels open tunnels.
func NewProxyRunner(maxTunnels int, budget ProxyBudget) *ProxyRunner {
	if budget == nil {
		budget = NewMemoryProxyBudget()
	}
	d := &net.Dialer{}
	return &ProxyRunner{
		max: maxTunnels, budget: budget, Now: time.Now, Interfaces: hostnet.List,
		Lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		Dial: d.DialContext,
	}
}

func (r *ProxyRunner) take() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open >= r.max {
		return false
	}
	r.open++
	return true
}

func (r *ProxyRunner) give() {
	r.mu.Lock()
	r.open--
	r.mu.Unlock()
}

// Open is the number of tunnels open on the runner.
func (r *ProxyRunner) Open() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open
}

// proxyAuth is the authority a tunnel was admitted under: the reviewed policy of the
// current egress session, and a context that ends when that authority does.
type proxyAuth struct {
	policy appplatform.ProxyPolicy
	digest string
	ctx    context.Context
	cancel context.CancelCauseFunc
}

// ProxyInstall is one installation's tunnels: its admission counters, throughput
// cap, current authority and the statistics Portal is told about.
type ProxyInstall struct {
	id     string
	runner *ProxyRunner
	limits ProxyLimits
	bucket *TokenBucket
	rate   *bandwidth

	mu   sync.Mutex
	open int
	// pending counts the streams whose open frame is still being read. A stream that has not said what it
	// wants holds no tunnel slot, so it is capped on its own.
	pending int
	auth    *proxyAuth

	stats    map[int64]map[string]*proxyHostStats
	refusals map[proxyRefusalKey]*proxyRefusalCount
	lastUse  time.Time
	usageDay int64
	usageAt  time.Time
	usageSet int64
}

type proxyHostStats struct {
	tunnels           int64
	bytesIn, bytesOut int64
	closes            map[string]int64
}

type proxyRefusalKey struct {
	host string
	code proxyforwarder.Refusal
}

type proxyRefusalCount struct {
	count int64
	last  time.Time
}

// NewProxyInstall builds an installation's proxy state.
func NewProxyInstall(id string, runner *ProxyRunner, limits ProxyLimits) *ProxyInstall {
	return &ProxyInstall{
		id: id, runner: runner, limits: limits,
		bucket: NewTokenBucket(limits.NewPerMin, limits.Burst, runner.Now),
		rate:   newBandwidth(int64(limits.Mbit)*1000*1000/8, runner.Now),
		stats:  map[int64]map[string]*proxyHostStats{}, refusals: map[proxyRefusalKey]*proxyRefusalCount{},
	}
}

// ProxyMaxPendingOpens bounds the streams of one installation whose open frame has not arrived yet.
const ProxyMaxPendingOpens = 32

// Inherit takes over the admission bucket and the throughput cap of the installation this one replaces, so a
// new generation of the same installation is not given a fresh burst. The tunnels stay with the old
// installation and end when it closes.
func (in *ProxyInstall) Inherit(prev *ProxyInstall) {
	if prev == nil {
		return
	}
	in.bucket, in.rate = prev.bucket, prev.rate
}

// Apply makes policy the installation's authority. The same policy digest and
// budget keep the tunnels that are open; any other cuts them, as a policy change.
func (in *ProxyInstall) Apply(policy appplatform.ParsedProxyPolicy) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.auth != nil && in.auth.digest == policy.SHA256 {
		return
	}
	if in.auth != nil {
		in.auth.cancel(errProxyPolicy)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	in.auth = &proxyAuth{policy: policy.Policy, digest: policy.SHA256, ctx: ctx, cancel: cancel}
}

// Revoke ends the installation's authority and cuts every tunnel: the session
// closed, was revoked or the runner is shutting down.
func (in *ProxyInstall) Revoke() {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.auth != nil {
		in.auth.cancel(errProxySession)
		in.auth = nil
	}
}

// Digest is the policy digest of the current authority, or "".
func (in *ProxyInstall) Digest() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.auth == nil {
		return ""
	}
	return in.auth.digest
}

// Open is the number of tunnels open for the installation.
func (in *ProxyInstall) Open() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.open
}

var (
	errProxyPolicy  = errors.New("proxy policy changed")
	errProxySession = errors.New("proxy session ended")
)

// admit is step 1: a usable session and authority, then the open-tunnel caps, the
// new-tunnel bucket and the daily budget, all before any lookup. On success it
// returns the authority and the release of the slots it took.
func (in *ProxyInstall) admit(usable func() bool) (*proxyAuth, func(), proxyforwarder.Refusal) {
	if usable != nil && !usable() {
		return nil, nil, proxyforwarder.RefusalNotGranted
	}
	in.mu.Lock()
	auth := in.auth
	if auth == nil {
		in.mu.Unlock()
		return nil, nil, proxyforwarder.RefusalNotGranted
	}
	if in.open >= in.limits.Tunnels {
		in.mu.Unlock()
		return nil, nil, proxyforwarder.RefusalBusy
	}
	in.mu.Unlock()
	if !in.bucket.Allow() {
		return nil, nil, proxyforwarder.RefusalBusy
	}
	if in.spent(auth) {
		return nil, nil, proxyforwarder.RefusalBudgetSpent
	}
	if !in.runner.take() {
		return nil, nil, proxyforwarder.RefusalBusy
	}
	in.mu.Lock()
	if in.auth != auth {
		// The authority changed between the checks: a policy change or a revocation, not a full house.
		in.mu.Unlock()
		in.runner.give()
		return nil, nil, proxyforwarder.RefusalUnavailable
	}
	if in.open >= in.limits.Tunnels {
		in.mu.Unlock()
		in.runner.give()
		return nil, nil, proxyforwarder.RefusalBusy
	}
	in.open++
	in.mu.Unlock()
	return auth, func() {
		in.mu.Lock()
		in.open--
		in.mu.Unlock()
		in.runner.give()
	}, ""
}

func (in *ProxyInstall) day() int64 { return in.runner.Now().UTC().Unix() / 86400 }

func (in *ProxyInstall) spent(auth *proxyAuth) bool {
	return in.runner.budget.Used(in.id, in.day()) >= auth.policy.DailyBytes
}

// Serve handles one proxy stream whose first bytes are still unread on r. usable
// reports whether the installation's egress session is current and unexpired; a
// stream is refused as not_granted when it is not. Serve closes conn.
func (in *ProxyInstall) Serve(conn net.Conn, r io.Reader, usable func() bool) {
	defer conn.Close()
	in.mu.Lock()
	if in.pending >= ProxyMaxPendingOpens {
		in.mu.Unlock()
		return
	}
	in.pending++
	in.mu.Unlock()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	host, port, err := proxyforwarder.ReadOpen(r, true)
	_ = conn.SetReadDeadline(time.Time{})
	in.mu.Lock()
	in.pending--
	in.mu.Unlock()
	if err != nil {
		return
	}
	auth, release, refusal := in.admit(usable)
	if refusal != "" {
		in.refuse(conn, host, refusal)
		return
	}
	defer release()
	// Step 2: the host must match, and the port must be 443. A name that is an IP
	// literal or not a plain lower-case DNS name matches nothing.
	if !auth.policy.Match(host) {
		in.refuse(conn, host, proxyforwarder.RefusalHostNotReviewed)
		return
	}
	if port != appplatform.ProxyPort {
		in.refuse(conn, host, proxyforwarder.RefusalPortNotPermitted)
		return
	}
	ctx, cancel := context.WithCancelCause(auth.ctx)
	defer cancel(nil)
	// The lifetime counts from admission, so resolving, dialling and the first record are inside it.
	lifetime := time.AfterFunc(in.limits.Lifetime, func() { cancel(errors.New(ProxyCloseLifetime)) })
	defer lifetime.Stop()
	// A cut authority closes the application's connection, whatever Serve is waiting on.
	defer context.AfterFunc(ctx, func() { _ = conn.Close() })()
	// Step 3: resolve, and judge every answer. Only an allowed address may be dialled.
	resolveCtx, stopResolve := context.WithTimeout(ctx, in.limits.Connect)
	addresses, err := in.runner.Lookup(resolveCtx, host)
	stopResolve()
	if err != nil || len(addresses) == 0 || len(addresses) > 16 {
		if ctx.Err() != nil {
			in.refuse(conn, host, proxyforwarder.RefusalUnavailable)
			return
		}
		in.refuse(conn, host, proxyforwarder.RefusalResolveFailed)
		return
	}
	pinned, ok := in.runner.firstAllowed(addresses)
	if !ok {
		in.refuse(conn, host, proxyforwarder.RefusalAddressDenied)
		return
	}
	// Step 4: one pinned dial.
	dialCtx, stopDial := context.WithTimeout(ctx, in.limits.Connect)
	upstream, err := in.runner.Dial(dialCtx, "tcp", net.JoinHostPort(pinned.String(), strconv.Itoa(appplatform.ProxyPort)))
	stopDial()
	if err != nil {
		in.refuse(conn, host, proxyforwarder.RefusalConnectFailed)
		return
	}
	defer upstream.Close()
	reply, _ := proxyforwarder.EncodeReply(proxyforwarder.RefusalNone)
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(reply); err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Time{})
	defer context.AfterFunc(ctx, func() { _ = upstream.Close() })()
	t := &proxyTunnel{in: in, host: host, conn: conn, upstream: upstream, ctx: ctx, cancel: cancel, auth: auth}
	in.opened(host)
	t.run(r)
}

// firstAllowed is the address guard applied to every answer (doc 184 step 3): a
// public address that is not one of this host's own and not inside a bridge's
// subnet. The first allowed one is dialled; denied ones are only skipped.
func (r *ProxyRunner) firstAllowed(addresses []netip.Addr) (netip.Addr, bool) {
	var interfaces []hostnet.Interface
	if r.Interfaces != nil {
		list, err := r.Interfaces()
		if err != nil {
			return netip.Addr{}, false
		}
		interfaces = list
	}
	for _, ip := range addresses {
		ip = ip.Unmap()
		if !publicAddress(ip) || appplatform.AlwaysDenied(ip) || onHost(interfaces, ip) {
			continue
		}
		return ip, true
	}
	return netip.Addr{}, false
}

// refuse answers a refusal, counts it and, for the rollup, notes it.
func (in *ProxyInstall) refuse(conn net.Conn, host string, refusal proxyforwarder.Refusal) {
	if reply, err := proxyforwarder.EncodeReply(refusal); err == nil {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write(reply)
	}
	in.noteRefusal(host, refusal)
}

type proxyTunnel struct {
	in       *ProxyInstall
	host     string
	conn     net.Conn
	upstream net.Conn
	ctx      context.Context
	cancel   context.CancelCauseFunc
	auth     *proxyAuth

	last     atomic.Int64 // unix nanoseconds of the last byte in either direction
	bytesIn  atomic.Int64 // upstream to application
	bytesOut atomic.Int64 // application to upstream
	reason   atomic.Value // the first close reason
}

func (t *proxyTunnel) fail(reason string) {
	t.reason.CompareAndSwap(nil, reason)
	t.cancel(errors.New(reason))
}

// run reads the first record (step 5) and splices (step 6).
func (t *proxyTunnel) run(app io.Reader) {
	in := t.in
	now := in.runner.Now
	t.last.Store(now().UnixNano())
	// The first record: one ClientHello whose outer server name is the CONNECT host.
	// Nothing reaches the upstream before it has been judged.
	_ = t.conn.SetReadDeadline(time.Now().Add(in.limits.FirstRecord))
	record, err := readFirstRecord(app)
	_ = t.conn.SetReadDeadline(time.Time{})
	if err != nil {
		t.finish(in, mismatchOrError(err, t.ctx))
		return
	}
	name, err := parseClientHelloSNI(record)
	if err != nil || strings.ToLower(name) != t.host {
		t.finish(in, ProxyCloseSNIMismatch)
		return
	}
	if !t.charge(len(record), true) || !in.rate.wait(t.ctx, len(record)) {
		t.finish(in, t.cause())
		return
	}
	_ = t.upstream.SetWriteDeadline(time.Now().Add(in.limits.Idle))
	if _, err := t.upstream.Write(record); err != nil {
		t.finish(in, ProxyCloseError)
		return
	}
	_ = t.upstream.SetWriteDeadline(time.Time{})
	t.last.Store(now().UnixNano())
	t.finish(in, t.splice(app))
}

// mismatchOrError names why the first record could not be read: a cut tunnel is the
// session's reason, a first record that never came, or was not TLS, is a mismatch.
func mismatchOrError(err error, ctx context.Context) string {
	if ctx.Err() != nil {
		return reasonOf(ctx)
	}
	return ProxyCloseSNIMismatch
}

func reasonOf(ctx context.Context) string {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errProxyPolicy):
		return ProxyClosePolicy
	case errors.Is(cause, errProxySession):
		return ProxyCloseSession
	case cause != nil && cause != context.Canceled:
		return cause.Error()
	}
	return ProxyCloseSession
}

func (t *proxyTunnel) cause() string {
	if r, ok := t.reason.Load().(string); ok {
		return r
	}
	return reasonOf(t.ctx)
}

// charge counts n bytes against the installation's daily budget. It reports false
// when the budget is spent, after which the tunnel is cut.
func (t *proxyTunnel) charge(n int, out bool) bool {
	if out {
		t.bytesOut.Add(int64(n))
	} else {
		t.bytesIn.Add(int64(n))
	}
	total := t.in.runner.budget.Add(t.in.id, t.in.day(), int64(n))
	if total >= t.auth.policy.DailyBytes {
		t.fail(ProxyCloseBudget)
		return false
	}
	return true
}

// splice copies both ways until a side ends, the idle or lifetime timer fires, the
// budget is spent or the authority ends, and returns the close reason.
func (t *proxyTunnel) splice(app io.Reader) string {
	in := t.in
	now := in.runner.Now
	var wg sync.WaitGroup
	copyDir := func(dst net.Conn, src io.Reader, out bool) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				t.last.Store(now().UnixNano())
				if !t.charge(n, out) || !in.rate.wait(t.ctx, n) {
					return
				}
				_ = dst.SetWriteDeadline(time.Now().Add(in.limits.Idle))
				if _, werr := dst.Write(buf[:n]); werr != nil {
					t.fail(ProxyCloseError)
					return
				}
				_ = dst.SetWriteDeadline(time.Time{})
				t.last.Store(now().UnixNano())
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && t.ctx.Err() == nil {
					t.fail(ProxyCloseError)
					return
				}
				// A clean end of one side is a half-close: the other side may still answer.
				if closer, ok := dst.(interface{ CloseWrite() error }); ok {
					_ = closer.CloseWrite()
				} else {
					t.fail(ProxyCloseEOF)
				}
				return
			}
		}
	}
	wg.Add(2)
	go copyDir(t.upstream, app, true)
	go copyDir(t.conn, t.upstream, false)
	// The watchdog ends an idle tunnel; ending any tunnel closes both connections,
	// which is what unblocks the copies.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(max(in.limits.Idle/10, time.Millisecond))
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.ctx.Done():
				return
			case <-tick.C:
				if time.Duration(now().UnixNano()-t.last.Load()) >= in.limits.Idle {
					t.fail(ProxyCloseIdle)
				}
			}
		}
	}()
	wg.Wait()
	if r, ok := t.reason.Load().(string); ok {
		return r
	}
	if t.ctx.Err() != nil {
		return reasonOf(t.ctx)
	}
	return ProxyCloseEOF
}

func (t *proxyTunnel) finish(in *ProxyInstall, reason string) {
	t.reason.CompareAndSwap(nil, reason)
	if r, ok := t.reason.Load().(string); ok {
		reason = r
	}
	in.closed(t.host, reason, t.bytesIn.Load(), t.bytesOut.Load())
}
