package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

// The PostgreSQL liveness probe (doc 178). The runner terminates the protocol itself and performs
// one fixed exchange for a reviewed target: connect to a pinned, class-checked address, TLS with
// full verification, authenticate with the role Portal placed in the signed command, run the
// compiled-in statement once and terminate. No byte of the exchange, no server message and no
// version crosses back: only an outcome class and a latency.
//
// PostgresOutcome values.
const (
	PostgresReachable     = "reachable"
	PostgresConnectFailed = "connect_failed"
	PostgresTLSFailed     = "tls_failed"
	PostgresAuthFailed    = "auth_failed"
	PostgresQueryFailed   = "query_failed"
	PostgresTimeout       = "timeout"
)

const (
	// probeSQL is the one statement the probe ever sends. It is a constant, never built from input.
	probeSQL = "SELECT 1"
	// probeApplicationName is the one run-time parameter beside user and database.
	probeApplicationName = "portal-app-probe"
	// PostgresMaxReadBytes bounds what one check reads from the server.
	PostgresMaxReadBytes = 64 * 1024
	// postgresCloseBudget bounds Terminate and the close of a connection that worked.
	postgresCloseBudget = 2 * time.Second
	// MaxPostgresLookupAddresses bounds an accepted DNS answer.
	maxPostgresLookupAddresses = 16
)

// PostgresResult is what a finished check releases.
type PostgresResult struct {
	Outcome string
	Latency time.Duration
}

// PostgresCheck is the online authority check of one call. The caller (the transport) carries the
// challenge to Portal and verifies the signed proof; a non-nil error withholds the call.
type PostgresCheck func(ctx context.Context) error

// PostgresProbe runs liveness checks. One is shared by the runner: it owns the per-installation
// concurrency and draws every check from the pool's background share.
type PostgresProbe struct {
	pool        *NetworkPool
	networks    []appplatform.PrivateNetwork
	networksSHA string
	addressGuard
	lookup func(context.Context, string) ([]netip.Addr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
	// systemRoots replaces the system roots for a target without its own reviewed CA. Tests only.
	systemRoots *x509.CertPool

	mu      sync.Mutex
	running map[string]int
}

// NewPostgresProbe builds the probe on a network pool, whose private-network allowlist and
// background budget it shares.
func NewPostgresProbe(pool *NetworkPool) (*PostgresProbe, error) {
	if pool == nil {
		return nil, ErrDenied
	}
	networks, sum := pool.PrivateNetworks()
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &PostgresProbe{
		pool: pool, networks: networks, networksSHA: sum,
		addressGuard: addressGuard{networks: networks, now: time.Now, interfaces: hostnet.List},
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dial:    d.DialContext,
		running: map[string]int{},
	}, nil
}

// phaseState records how far one attempt got, so a failure is classified by where it happened and
// never by what its error says.
type phaseState struct {
	mu        sync.Mutex
	dialed    bool
	tlsDone   bool
	readBytes int
}

func (s *phaseState) set(f func()) {
	s.mu.Lock()
	f()
	s.mu.Unlock()
}

// boundedConn caps what is read from the server and carries the attempt's deadline.
type boundedConn struct {
	net.Conn
	state *phaseState
}

func (c *boundedConn) Read(b []byte) (int, error) {
	c.state.mu.Lock()
	remaining := PostgresMaxReadBytes - c.state.readBytes
	c.state.mu.Unlock()
	if remaining <= 0 {
		return 0, errors.New("postgres probe: read budget spent")
	}
	if len(b) > remaining {
		b = b[:remaining]
	}
	n, err := c.Conn.Read(b)
	c.state.set(func() { c.state.readBytes += n })
	return n, err
}

// classifyPostgresFailure turns a failed connect into an outcome by phase alone: before the dial
// finished it is connect_failed; after the dial and before TLS was established it is tls_failed
// (including a server that answers the SSL request with 'N'); after TLS and before the connection
// was ready it is auth_failed, unless the server itself answered with an error outside class 28
// (a database that does not exist, no free connection), which is connect_failed. A deadline wins.
func classifyPostgresFailure(deadlineHit bool, st *phaseState, err error) string {
	var netErr net.Error
	if deadlineHit || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
		return PostgresTimeout
	}
	st.mu.Lock()
	dialed, tlsDone := st.dialed, st.tlsDone
	st.mu.Unlock()
	switch {
	case !dialed:
		return PostgresConnectFailed
	case !tlsDone:
		return PostgresTLSFailed
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if len(pgErr.Code) >= 2 && pgErr.Code[:2] == "28" {
			return PostgresAuthFailed
		}
		return PostgresConnectFailed
	}
	return PostgresAuthFailed
}

// Run performs one check. raw and signature are Portal's signed command; check is the online
// authority check, run three times: before any resolution, before the dial and before the outcome
// is released. Errors are ErrDenied (the command, the target or an authority check refused it),
// ErrBusy (no slot) and ErrUnavailable (the caller went away, or the lookup was cancelled); a
// check that ran returns an outcome, never an error, whatever the server did.
func (p *PostgresProbe) Run(parent context.Context, raw, signature []byte, a control.Authority, check PostgresCheck) (PostgresResult, error) {
	var zero PostgresResult
	if p == nil || p.pool == nil || parent == nil || check == nil {
		return zero, ErrDenied
	}
	if parent.Err() != nil {
		return zero, ErrUnavailable
	}
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return zero, ErrDenied
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	defer clear(raw)
	verified, err := control.VerifyPostgresProbeCommand(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return zero, ErrDenied
	}
	defer verified.Clear()
	command := verified.Command()
	defer command.Clear()
	policy, err := appplatform.ParsePostgresPolicy(command.PolicyRaw)
	if err != nil {
		return zero, ErrDenied
	}
	target, ok := policy.Policy.Select(command.TargetID)
	if !ok {
		return zero, ErrDenied
	}
	// A private target is only served against the allowlist the policy was reviewed with; an empty
	// allowlist serves none. Nothing is acquired before.
	private := target.Network == appplatform.HTTPNetworkPrivate
	if private && (len(p.networks) == 0 || command.PrivateNetworksSHA256 != p.networksSHA) {
		return zero, ErrDenied
	}
	expires, err := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if err != nil {
		return zero, ErrDenied
	}
	release, err := p.admit(command.InstallationID, policy.Policy.Concurrency)
	if err != nil {
		return zero, err
	}
	defer release()
	deadline := time.Now().Add(policy.Policy.Deadline)
	if signed := time.UnixMilli(expires); signed.Before(deadline) {
		deadline = signed
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	// Authority check 1, before anything is resolved.
	if check(ctx) != nil {
		return zero, p.refusal(parent, ctx)
	}
	start := time.Now()
	finish := func(outcome string) (PostgresResult, error) {
		// Authority check 3, before the outcome is released: a revoked grant, binding or credential
		// withholds even a finished check.
		cctx, ccancel := context.WithTimeout(parent, time.Second)
		defer ccancel()
		if parent.Err() != nil || check(cctx) != nil {
			return zero, ErrDenied
		}
		return PostgresResult{Outcome: outcome, Latency: time.Since(start)}, nil
	}
	addresses := []netip.Addr{target.Literal()}
	if !addresses[0].IsValid() {
		addresses, err = p.lookup(ctx, target.Host)
		if err != nil {
			if parent.Err() != nil {
				return zero, ErrUnavailable
			}
			if ctx.Err() != nil {
				return finish(PostgresTimeout)
			}
			return finish(PostgresConnectFailed)
		}
	}
	if len(addresses) > maxPostgresLookupAddresses {
		return zero, ErrDenied
	}
	if len(addresses) == 0 {
		return finish(PostgresConnectFailed)
	}
	if !p.addressGuard.addressesAllowed(private, uint16(target.Port), "https", addresses) {
		return zero, ErrDenied
	}
	// Authority check 2, before the dial.
	if check(ctx) != nil {
		return zero, p.refusal(parent, ctx)
	}
	outcome := p.exchange(ctx, parent, target, addresses[0].Unmap(), command.Username, command.Password, deadline)
	if parent.Err() != nil {
		return zero, ErrUnavailable
	}
	return finish(outcome)
}

// refusal is the error of a check that did not pass: a caller that went away or a deadline that
// ran out before anything was dispatched is unavailable, anything else a denial.
func (p *PostgresProbe) refusal(parent, ctx context.Context) error {
	if parent.Err() != nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	return ErrDenied
}

// admit takes a background slot and the installation's concurrency.
func (p *PostgresProbe) admit(installation string, limit int) (func(), error) {
	if limit < 1 || limit > appplatform.MaxPostgresConcurrency {
		return nil, ErrDenied
	}
	p.mu.Lock()
	if p.running[installation] >= limit {
		p.mu.Unlock()
		return nil, ErrBusy
	}
	p.running[installation]++
	p.mu.Unlock()
	slot, err := p.pool.AcquireBackground()
	if err != nil {
		p.mu.Lock()
		p.running[installation]--
		if p.running[installation] == 0 {
			delete(p.running, installation)
		}
		p.mu.Unlock()
		return nil, err
	}
	return func() {
		slot()
		p.mu.Lock()
		p.running[installation]--
		if p.running[installation] == 0 {
			delete(p.running, installation)
		}
		p.mu.Unlock()
	}, nil
}

// exchange is the one connection: it returns an outcome class for everything that can happen on it.
// username and password are the command's own slices; pgconn only takes strings, so a copy of each
// lives in the config until the garbage collector reclaims it (a limit of the library).
func (p *PostgresProbe) exchange(ctx, parent context.Context, target appplatform.PostgresTarget, address netip.Addr, username, password []byte, deadline time.Time) string {
	cfg, err := pgconn.ParseConfigWithOptions("sslmode=verify-full", pgconn.ParseConfigOptions{ConnStringAllowedKeys: []string{"sslmode"}})
	if err != nil {
		return PostgresConnectFailed
	}
	state := &phaseState{}
	pinned := net.JoinHostPort(address.String(), strconv.Itoa(target.Port))
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: target.Host}
	if roots, ok := appplatform.PostgresCARoots(target); ok {
		tlsConfig.RootCAs = roots
	} else if target.CAPEM != "" {
		return PostgresConnectFailed
	} else {
		tlsConfig.RootCAs = p.systemRoots
	}
	// Every field is set here, so nothing the connection string, the environment or the library's
	// defaults supplied can change where this goes or what it sends.
	cfg.Host, cfg.Port, cfg.Database = target.Host, uint16(target.Port), target.Database
	cfg.User, cfg.Password = string(username), string(password)
	cfg.RuntimeParams = map[string]string{"application_name": probeApplicationName}
	cfg.TLSConfig = tlsConfig
	cfg.Fallbacks = nil
	cfg.ConnectTimeout = 0
	cfg.ValidateConnect, cfg.AfterConnect, cfg.OnNotice, cfg.OnNotification = nil, nil, nil, nil
	cfg.OAuthTokenProvider = nil
	cfg.RequireAuth = "scram-sha-256,password,none"
	cfg.ChannelBinding = "prefer"
	cfg.SSLNegotiation = "postgres"
	cfg.SSLCertMode = "disable"
	cfg.MinProtocolVersion, cfg.MaxProtocolVersion = "3.0", "3.0"
	cfg.KerberosSrvName, cfg.KerberosSpn = "", ""
	cfg.MaxProtocolMessageBodyLen = PostgresMaxReadBytes
	// A deadline-only watcher: the library's other watcher opens a second connection to cancel.
	cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.DeadlineContextWatcherHandler{Conn: c.Conn()}
	}
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) {
		if host != target.Host {
			return nil, ErrDenied
		}
		return []string{address.String()}, nil
	}
	cfg.DialFunc = func(dctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := p.dial(dctx, "tcp", pinned)
		if err != nil {
			return nil, err
		}
		_ = conn.SetDeadline(deadline)
		state.set(func() { state.dialed = true })
		return &boundedConn{Conn: conn, state: state}, nil
	}
	cfg.AfterNetConnect = func(hctx context.Context, _ *pgconn.Config, conn net.Conn) (net.Conn, error) {
		// The library starts the handshake lazily, with the first write of the startup message, so
		// it is finished here: a certificate or protocol failure is then a TLS failure by phase.
		if tc, ok := conn.(*tls.Conn); ok {
			if err := tc.HandshakeContext(hctx); err != nil {
				// The connection comes back with the error: the library closes whatever it holds.
				return conn, err
			}
		}
		state.set(func() { state.tlsDone = true })
		return conn, nil
	}
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return classifyPostgresFailure(ctx.Err() != nil && parent.Err() == nil, state, err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), postgresCloseBudget)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	results, err := conn.Exec(ctx, probeSQL).ReadAll()
	if err != nil {
		return classifyQueryFailure(ctx, parent, err)
	}
	if len(results) != 1 || results[0].Err != nil || len(results[0].Rows) != 1 || len(results[0].Rows[0]) != 1 || string(results[0].Rows[0][0]) != "1" {
		return PostgresQueryFailed
	}
	return PostgresReachable
}

func classifyQueryFailure(ctx, parent context.Context, err error) string {
	var netErr net.Error
	if ctx.Err() != nil && parent.Err() == nil || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
		return PostgresTimeout
	}
	return PostgresQueryFailed
}
