package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const (
	// maxEgressSessions bounds the egress sessions a runner holds at once (current
	// and draining), whatever the installation count. Sessions are detached from the
	// control endpoint's eight-connection limit, so they need a limit of their own.
	maxEgressSessions = 64
	// maxEgressSessionsPerInstall is the current session and one draining one.
	maxEgressSessionsPerInstall = 2
	// egressSweepInterval is how often sessions are compared with the enrollment
	// and their own expiry. Every call also checks both, so this only bounds how
	// long an idle session outlives a change.
	egressSweepInterval = time.Second
	// egressAdmitTimeout bounds Portal's answer to an admit or a notice.
	egressAdmitTimeout = 8 * time.Second
	// egressCallTimeout bounds one broker call from request to reply, inside the
	// broker socket's 25 s write deadline.
	egressCallTimeout = 22 * time.Second
	// egressSeenRequests bounds the request IDs remembered per session. A session
	// lives five minutes at 120 calls a minute, so this is never reached by a
	// working Portal.
	egressSeenRequests = 4096
	// egressBrokerConnections bounds the connections one installation's broker
	// socket serves at once.
	egressBrokerConnections = 16
)

// EgressListener is what binding a broker socket yields: the listener, which
// owns the socket path and removes it on Close, and the check applied to every
// accepted connection (the kernel peer must be the application's own user).
type EgressListener struct {
	Listener net.Listener
	// PeerOK is applied to each accepted connection before any byte is read. Nil
	// admits every peer, which only tests do.
	PeerOK func(net.Conn) bool
	// Intact, when set, reports whether the socket still sits at the place and
	// with the identity it was bound with. It is checked before every call is
	// admitted, so a replaced directory or socket makes the broker unavailable.
	Intact func() error
}

// EgressBinder verifies a verified session against the runner's journal and the
// running container, and, when listen is true, binds the broker socket of the
// session's installation generation and returns it. With listen false it only
// verifies, and returns the zero value.
type EgressBinder func(ctx context.Context, v control.VerifiedServiceEgressSession, open EgressOpen, listen bool) (EgressListener, error)

// egressRegistry holds the egress sessions of one runner. Per installation it
// keeps one broker socket for the generation it is bound to, at most one current
// session, and any older session still draining. Everything in it is memory only,
// bound to signed commands that expire; authority is re-derived on every call.
type egressRegistry struct {
	provider *networkProvider
	bind     EgressBinder
	now      func() time.Time
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	// maxSessions is the bound on live sessions that a new installation meets.
	maxSessions int

	// openMu serialises opens, which are rare and may touch the journal.
	openMu sync.Mutex

	// mu guards installs, live and closed; an install's own fields are guarded by
	// its mu, and a writer of install.current holds both.
	mu       sync.Mutex
	installs map[string]*egressInstall
	live     int
	closed   bool
}

type egressInstall struct {
	registry   *egressRegistry
	id         string
	generation int64
	listener   net.Listener
	peerOK     func(net.Conn) bool
	intact     func() error
	server     *egressServer

	mu       sync.Mutex
	bucket   *broker.TokenBucket
	current  *egressSession
	sessions map[*egressSession]struct{}
	inflight int // network calls in flight over all sessions of the installation
	closed   bool
}

type egressSession struct {
	install *egressInstall
	command control.ServiceEgressSessionCommand
	// revision is the control revision the session was verified under.
	revision    int64
	concurrency int
	resources   []string
	expires     time.Time
	mux         *EgressMux

	// Guarded by install.mu.
	draining bool
	inflight int

	seenMu sync.Mutex
	seen   map[string]struct{}
}

func newEgressRegistry(parent context.Context, provider *networkProvider, bind EgressBinder) *egressRegistry {
	ctx, cancel := context.WithCancel(parent)
	r := &egressRegistry{provider: provider, bind: bind, now: time.Now, maxSessions: maxEgressSessions, ctx: ctx, cancel: cancel, installs: make(map[string]*egressInstall)}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(egressSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.sweep()
			case <-ctx.Done():
				return
			}
		}
	}()
	return r
}

// close ends every session and every broker socket, and waits for their
// goroutines. It is the runner-shutdown path.
func (r *egressRegistry) close() {
	r.cancel()
	r.mu.Lock()
	r.closed = true
	installs := make([]*egressInstall, 0, len(r.installs))
	for _, in := range r.installs {
		installs = append(installs, in)
	}
	r.mu.Unlock()
	for _, in := range installs {
		r.closeInstall(in, EgressCloseShutdown)
	}
	r.wg.Wait()
}

// open verifies, binds, acknowledges and installs one session. The connection
// belongs to the registry from the call on: it is closed on refusal, and
// otherwise carries the session's frames until the session ends. open returns
// once the session is installed; it then runs on its own goroutines.
//
// A session replaces the current one of its installation only after every check
// has passed and the acknowledgement was written, so a refused or failed open
// never disturbs a working session.
func (r *egressRegistry) open(ctx context.Context, conn net.Conn, open EgressOpen) error {
	r.openMu.Lock()
	defer r.openMu.Unlock()
	refuse := func(err error) error {
		_ = conn.SetWriteDeadline(r.now().Add(EgressWriteTimeout))
		_, _ = conn.Write(EncodeEgressAck(open.FrameSHA256, EgressAckRefused))
		_ = conn.Close()
		return err
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if r.provider == nil || r.bind == nil || closed {
		return refuse(broker.ErrUnavailable)
	}
	authority, err := r.provider.enrollment(ctx)
	if err != nil {
		return refuse(err)
	}
	verified, err := control.VerifyServiceEgressSession(open.Raw, open.Signature, authority, r.now().UnixMilli())
	if err != nil {
		return refuse(broker.ErrDenied)
	}
	command := verified.Command()
	parsed, err := appplatform.ParseDescriptor(open.Descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != command.ReleaseSHA256 {
		return refuse(broker.ErrDenied)
	}
	generation, _ := strconv.ParseInt(command.Generation, 10, 64)
	revision, _ := strconv.ParseInt(command.ControlRevision, 10, 64)
	concurrency, _ := strconv.Atoi(command.Concurrency)
	expires, _ := strconv.ParseInt(command.ExpiresMS, 10, 64)
	r.mu.Lock()
	existing := r.installs[command.InstallationID]
	live := r.live
	sessions := 0
	if existing != nil {
		existing.mu.Lock()
		sessions = len(existing.sessions)
		existing.mu.Unlock()
	}
	r.mu.Unlock()
	if existing != nil && existing.generation > generation {
		return refuse(broker.ErrDenied)
	}
	// The bound is on installations: a renewal or replacement does not raise the
	// steady-state count, so only a new installation is refused at it. An
	// installation holds its current session and at most one draining.
	if existing == nil && live >= r.maxSessions || existing != nil && sessions >= maxEgressSessionsPerInstall {
		return refuse(broker.ErrUnavailable)
	}
	listen := existing == nil || existing.generation < generation
	binding, err := r.bind(ctx, verified, open, listen)
	if err != nil || listen && binding.Listener == nil {
		if err == nil {
			err = broker.ErrUnavailable
		}
		return refuse(err)
	}
	session := &egressSession{command: command, revision: revision, concurrency: concurrency, resources: slices.Clone(command.Resources), expires: time.UnixMilli(expires), mux: NewEgressMux(conn, EgressRunner), seen: make(map[string]struct{})}
	// The last checks and the acknowledgement happen under the registry lock, so
	// an installation closed meanwhile (a disabled close, shutdown) is refused
	// here instead of being acknowledged as open and dropped. The write is one
	// small frame bounded by its deadline, and opens are serialised anyway.
	r.mu.Lock()
	install := r.installs[command.InstallationID]
	if r.closed || !listen && (install == nil || install.generation != generation) {
		r.mu.Unlock()
		if listen {
			_ = binding.Listener.Close()
		}
		return refuse(broker.ErrUnavailable)
	}
	_ = conn.SetWriteDeadline(r.now().Add(EgressWriteTimeout))
	if _, err := conn.Write(EncodeEgressAck(open.FrameSHA256, EgressAckOpen)); err != nil {
		r.mu.Unlock()
		if listen {
			_ = binding.Listener.Close()
		}
		_ = conn.Close()
		return err
	}
	_ = conn.SetWriteDeadline(time.Time{})
	var replaced *egressInstall
	if listen {
		bucket := broker.NewTokenBucket(120, 20, r.now)
		if install != nil {
			// The budget belongs to the installation, not the generation: a new
			// generation does not refill it.
			install.mu.Lock()
			bucket = install.bucket
			install.mu.Unlock()
			replaced = install
		}
		install = &egressInstall{registry: r, id: command.InstallationID, generation: generation, listener: binding.Listener, peerOK: binding.PeerOK, intact: binding.Intact, bucket: bucket, sessions: make(map[*egressSession]struct{})}
		r.installs[command.InstallationID] = install
	}
	session.install = install
	install.mu.Lock()
	previous := install.current
	install.current = session
	install.sessions[session] = struct{}{}
	if previous != nil {
		previous.draining = true
	}
	drainNow := previous != nil && previous.inflight == 0
	install.mu.Unlock()
	r.live++
	if listen {
		r.startServer(install)
	}
	// Add under the lock that close() takes before it waits.
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		_ = session.mux.Run(r.ctx)
		r.detach(session)
	}()
	if replaced != nil {
		r.closeInstall(replaced, EgressCloseRenewed)
	}
	if drainNow {
		go previous.mux.Close(EgressCloseRenewed)
	}
	return nil
}

// detach removes an ended session. A close frame from Portal with the disabled
// reason is terminal: the container is going away, so its socket goes too.
// Every other end leaves the listener, which then answers unavailable.
func (r *egressRegistry) detach(s *egressSession) {
	in := s.install
	r.mu.Lock()
	in.mu.Lock()
	if _, ok := in.sessions[s]; ok {
		delete(in.sessions, s)
		r.live--
	}
	if in.current == s {
		in.current = nil
	}
	in.mu.Unlock()
	r.mu.Unlock()
	if s.mux.PeerReason() == EgressCloseDisabled {
		r.closeInstall(in, EgressCloseDisabled)
	}
}

// closeInstall ends an installation's sessions and its broker socket.
func (r *egressRegistry) closeInstall(in *egressInstall, reason byte) {
	r.mu.Lock()
	in.mu.Lock()
	if in.closed {
		in.mu.Unlock()
		r.mu.Unlock()
		return
	}
	in.closed = true
	sessions := make([]*egressSession, 0, len(in.sessions))
	for s := range in.sessions {
		sessions = append(sessions, s)
	}
	if r.installs[in.id] == in {
		delete(r.installs, in.id)
	}
	in.mu.Unlock()
	r.mu.Unlock()
	if in.server != nil {
		in.server.close()
	}
	_ = in.listener.Close()
	for _, s := range sessions {
		s.mux.Close(reason)
	}
}

// sweep closes sessions that are past their expiry or were verified under an
// enrollment that no longer holds (another controller epoch, deployment or
// control revision). It does not re-verify the registration proof: that proof
// is only meaningful at open, and every call is signed afresh.
func (r *egressRegistry) sweep() {
	r.mu.Lock()
	var sessions []*egressSession
	for _, in := range r.installs {
		in.mu.Lock()
		for s := range in.sessions {
			sessions = append(sessions, s)
		}
		in.mu.Unlock()
	}
	r.mu.Unlock()
	if len(sessions) == 0 {
		return // nothing to compare: do not read the journal for an idle runner
	}
	authority, err := r.provider.enrollment(r.ctx)
	now := r.now()
	for _, s := range sessions {
		switch {
		case !now.Before(s.expires):
			s.mux.Close(EgressCloseExpired)
		case err == nil && !s.enrolled(authority):
			s.mux.Close(EgressCloseEpoch)
		}
	}
}

func (s *egressSession) enrolled(a control.Authority) bool {
	return a.DeploymentID == s.command.DeploymentID && a.ControllerEpoch == s.command.ControllerEpoch && a.ControlRevision == s.revision
}

// allows reports whether the session carries a resource.
func (s *egressSession) allows(resource string) bool { return slices.Contains(s.resources, resource) }

// release ends one call's accounting. A draining session with nothing left in
// flight closes: its replacement carries on.
func (s *egressSession) release(network bool) {
	in := s.install
	in.mu.Lock()
	s.inflight--
	if network {
		in.inflight--
	}
	done := s.draining && s.inflight == 0
	in.mu.Unlock()
	if done {
		go s.mux.Close(EgressCloseRenewed)
	}
}

var errEgressCommand = errors.New("service egress command does not match its session")

// checkCommand compares a verified command, and the body the runner holds,
// with the session it arrived on and the admit that asked for it. Portal signs
// the request; the runner never trusts it to be for this installation, this
// session or this request without checking.
func (s *egressSession) checkCommand(c control.NetworkHTTPRequestCommand, admit EgressAdmit, body []byte) error {
	scope := sha256.Sum256([]byte("portal-network-service/v1:" + s.command.SessionID))
	digest := sha256.Sum256(body)
	expires, ok := parseCounter(c.ExpiresMS)
	switch {
	case c.DeploymentID != s.command.DeploymentID, c.ControllerEpoch != s.command.ControllerEpoch, c.ControlRevision != s.command.ControlRevision,
		c.InstallationID != s.command.InstallationID, c.Generation != s.command.Generation, c.PolicyRevision != s.command.PolicyRevision,
		c.ReleaseSHA256 != s.command.ReleaseSHA256,
		// A service call is made as the service of this session, never as a viewer.
		c.ActorScope != hex.EncodeToString(scope[:]),
		// Credentials belong to a later capability; none may ride on this one.
		c.CredentialName != "", c.CredentialHeader != "", len(c.CredentialValue) != 0,
		c.Method != admit.Method, c.URL != admit.URL, c.Accept != admit.Accept, c.ContentType != admit.ContentType,
		c.BodyBytes != strconv.Itoa(admit.BodyBytes), c.BodySHA256 != admit.BodySHA256,
		len(body) != admit.BodyBytes, hex.EncodeToString(digest[:]) != admit.BodySHA256,
		!ok, time.UnixMilli(expires).After(s.expires):
		return errEgressCommand
	}
	return nil
}

func parseCounter(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n >= 0 && strconv.FormatInt(n, 10) == s
}

// claim records a request ID, refusing one the session has already seen: Portal
// mints a fresh ID per call, so a repeat is a replay.
func (s *egressSession) claim(requestID string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if _, replay := s.seen[requestID]; replay || len(s.seen) >= egressSeenRequests {
		return false
	}
	s.seen[requestID] = struct{}{}
	return true
}
