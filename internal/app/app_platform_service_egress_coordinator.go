package app

import (
	"context"
	"encoding/hex"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The egress coordinator (doc 174 section 3.2) runs in the task worker
// (RunPlatformTaskWorker), never under --once and never in the web process. It
// keeps one egress session open per eligible installation, answers the calls the
// runner admits on it, and closes it the moment Portal observes drift.
//
// ONLY ONE COORDINATOR MAY RUN. The runner keeps at most one current session per
// installation and a newer session replaces the older one, so two coordinators
// (two app-task-worker processes, say) would replace each other's sessions at
// every tick and every renewal. Task execution may be scaled out by running more
// workers; this part may not. There is no lease for it by design (doc 174 has no
// schema change): run `app-task-worker` once per Portal installation.

const (
	platformServiceEgressTick             = 5 * time.Second
	platformServiceEgressRenewBefore      = 60 * time.Second
	platformServiceEgressSessionLifetime  = 5 * time.Minute
	platformServiceEgressOpenTimeout      = 20 * time.Second
	platformServiceEgressCallTimeout      = 30 * time.Second
	platformServiceEgressAdmitLoadTimeout = 5 * time.Second
	platformServiceEgressRetry            = 15 * time.Second
	platformServiceEgressUnsupportedRetry = time.Minute
	platformServiceEgressMaxInstallations = 256
)

// platformServiceEgressTransport is the controller side of the runner endpoint.
// Linux talks to the real runner; elsewhere there is no runner, and tests
// substitute one.
type platformServiceEgressTransport interface {
	Query(ctx context.Context, e platformRuntimeEndpoint) (transport.ServiceEgressProfile, error)
	Open(ctx context.Context, e platformRuntimeEndpoint, raw, signature []byte, a control.Authority, descriptor []byte) (*transport.EgressMux, error)
}

// platformServiceEgressLive is one open session.
type platformServiceEgressLive struct {
	state platformServiceEgressSessionState
	mux   *transport.EgressMux

	cancel    context.CancelFunc
	calls     sync.WaitGroup
	finalized chan struct{}

	mu     sync.Mutex
	reason string
}

func (l *platformServiceEgressLive) ended() bool {
	select {
	case <-l.mux.Done():
		return true
	default:
		return false
	}
}

type platformServiceEgressCoordinator struct {
	server    *Server
	transport platformServiceEgressTransport
	endpoint  func() (platformRuntimeEndpoint, bool)
	now       func() time.Time
	audit     *platformServiceEgressAggregator

	root       context.Context
	rootCancel context.CancelFunc
	wg         sync.WaitGroup
	stopped    chan struct{}
	stopOnce   sync.Once

	mu               sync.Mutex
	live             map[string]*platformServiceEgressLive
	retryAt          map[string]time.Time
	unsupportedUntil time.Time
}

func newPlatformServiceEgressCoordinator(server *Server, t platformServiceEgressTransport) *platformServiceEgressCoordinator {
	root, cancel := context.WithCancel(context.Background())
	return &platformServiceEgressCoordinator{
		server: server, transport: t, endpoint: server.platformRuntimeEndpoint, now: time.Now,
		audit: newPlatformServiceEgressAggregator(), root: root, rootCancel: cancel, stopped: make(chan struct{}),
		live: map[string]*platformServiceEgressLive{}, retryAt: map[string]time.Time{},
	}
}

// run ticks until ctx is done or stop is called, then closes every session as
// shutdown.
func (c *platformServiceEgressCoordinator) run(ctx context.Context) {
	ticker := time.NewTicker(platformServiceEgressTick)
	defer ticker.Stop()
	defer c.shutdown()
	for {
		c.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-c.stopped:
			return
		case <-ticker.C:
		}
	}
}

// stop ends run. It is safe to call more than once.
func (c *platformServiceEgressCoordinator) stop() { c.stopOnce.Do(func() { close(c.stopped) }) }

// shutdown closes every session with reason shutdown, writes what is left of the
// summaries and joins everything the coordinator started.
func (c *platformServiceEgressCoordinator) shutdown() {
	for _, l := range c.snapshot() {
		c.closeLive(l, "shutdown")
	}
	c.rootCancel()
	c.wg.Wait()
	c.flushSummaries(c.now(), true)
}

func (c *platformServiceEgressCoordinator) snapshot() []*platformServiceEgressLive {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*platformServiceEgressLive, 0, len(c.live))
	for _, l := range c.live {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b *platformServiceEgressLive) int {
		switch {
		case a.state.pin.installation < b.state.pin.installation:
			return -1
		case a.state.pin.installation > b.state.pin.installation:
			return 1
		}
		return 0
	})
	return out
}

func (c *platformServiceEgressCoordinator) current(installation string) *platformServiceEgressLive {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live[installation]
}

func (c *platformServiceEgressCoordinator) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := c.now()
	// Drift: any session whose current authority is no longer the one it was
	// opened against is closed, whatever the reason.
	for _, l := range c.snapshot() {
		if l.ended() {
			continue
		}
		a, err := c.server.loadServiceEgressForSession(ctx, l.state)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.closeLive(l, c.server.classifyPlatformServiceEgressLoss(ctx, l.state.pin))
			continue
		}
		a.Close()
		if now.UnixMilli() >= l.state.expiresMS {
			c.closeLive(l, "expired")
		}
	}
	if endpoint, ok := c.endpoint(); ok && now.After(c.unsupportedUntil) {
		for _, installation := range c.server.listPlatformServiceEgressInstallations(ctx) {
			if ctx.Err() != nil {
				return
			}
			cur := c.current(installation)
			if cur != nil && !cur.ended() && now.UnixMilli() < cur.state.expiresMS-platformServiceEgressRenewBefore.Milliseconds() {
				continue
			}
			c.mu.Lock()
			wait := c.retryAt[installation]
			c.mu.Unlock()
			if now.Before(wait) {
				continue
			}
			if err := c.open(ctx, endpoint, installation, cur); err != nil {
				c.mu.Lock()
				c.retryAt[installation] = now.Add(platformServiceEgressRetry)
				c.mu.Unlock()
				if cur != nil && !cur.ended() && now.UnixMilli() >= cur.state.expiresMS {
					c.closeLive(cur, "expired")
				}
				if !errors.Is(err, errPlatformServiceEgressUnavailable) && !errors.Is(err, transport.ErrServiceEgressUnsupported) {
					log.Printf("application egress: opening a session failed: %v", err)
				}
			}
		}
	}
	c.flushSummaries(now, false)
}

// listPlatformServiceEgressInstallations are the installations that could hold a
// session: enabled and ready with a runtime, background.run granted and at least
// one egress resource granted. It is a filter, not authority: opening re-reads
// everything.
func (s *Server) listPlatformServiceEgressInstallations(ctx context.Context) []string {
	rows, err := s.st.queryContext(ctx, `SELECT i.id FROM app_platform_installations i
		WHERE i.desired_state='enabled' AND i.phase='ready' AND i.runtime_id IS NOT NULL
		AND EXISTS(SELECT 1 FROM app_platform_capability_grants g WHERE g.installation_id=i.id AND g.capability='background.run' AND g.granted=1)
		AND EXISTS(SELECT 1 FROM app_platform_capability_grants g WHERE g.installation_id=i.id AND g.capability IN ('network.http','notifications.publish') AND g.granted=1)
		ORDER BY i.id LIMIT ?`, platformServiceEgressMaxInstallations)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// classifyPlatformServiceEgressLoss says why a session whose authority is gone
// must close: the restore pause, the installation no longer enabled and ready,
// the controller epoch, or (anything else) drift of the pinned tuple.
func (s *Server) classifyPlatformServiceEgressLoss(ctx context.Context, pin platformServiceEgressPin) string {
	var epoch, paused string
	if err := s.st.queryRowContext(ctx, `SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'')`).Scan(&epoch, &paused); err != nil {
		return "drift"
	}
	if paused != "0" {
		return "restore_pause"
	}
	var desired, phase, installationEpoch string
	if err := s.st.queryRowContext(ctx, `SELECT desired_state,phase,controller_epoch FROM app_platform_installations WHERE id=?`, pin.installation).Scan(&desired, &phase, &installationEpoch); err != nil || desired != "enabled" || phase != "ready" {
		return "disabled"
	}
	if epoch != pin.epoch || installationEpoch != pin.epoch {
		return "epoch"
	}
	return "drift"
}

// open issues an assertion for exactly the resources granted now, reads the
// authority again with a fresh registration proof, and asks the runner for the
// session. The runner swaps it in only after verifying it; Portal then closes
// the session it replaces (renewed).
func (c *platformServiceEgressCoordinator) open(ctx context.Context, endpoint platformRuntimeEndpoint, installation string, replaced *platformServiceEgressLive) error {
	s := c.server
	plan, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{})
	if err != nil {
		return err
	}
	resources := slices.Clone(plan.resources)
	plan.Close()
	profile, err := c.transport.Query(ctx, endpoint)
	if err != nil {
		if errors.Is(err, transport.ErrServiceEgressUnsupported) {
			c.mu.Lock()
			c.unsupportedUntil = c.now().Add(platformServiceEgressUnsupportedRetry)
			c.mu.Unlock()
		}
		return err
	}
	if profile.BackgroundActive < 1 {
		c.mu.Lock()
		c.unsupportedUntil = c.now().Add(platformServiceEgressUnsupportedRetry)
		c.mu.Unlock()
		return transport.ErrServiceEgressUnsupported
	}
	sessionID, err := platformNewUUID()
	if err != nil {
		return err
	}
	identity, _, err := s.issuePlatformServiceIdentity(ctx, installation, sessionID, resources, platformServiceEgressSessionLifetime)
	if err != nil {
		return err
	}
	a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{identity: identity, resources: resources, history: true})
	if err != nil {
		clear(identity)
		return err
	}
	defer a.Close()
	raw, signature, expiresMS, err := a.signServiceEgressSession(sessionID, identity, time.Now().UnixMilli())
	if err != nil {
		clear(identity)
		return err
	}
	openCtx, cancel := context.WithTimeout(ctx, platformServiceEgressOpenTimeout)
	mux, err := c.transport.Open(openCtx, endpoint, raw, signature, a.history.authority, a.history.descriptor)
	cancel()
	if err != nil {
		clear(identity)
		return err
	}
	runCtx, runCancel := context.WithCancel(c.root)
	l := &platformServiceEgressLive{
		state: platformServiceEgressSessionState{sessionID: sessionID, identity: identity, resources: slices.Clone(a.resources), pin: a.pin(), expiresMS: expiresMS},
		mux:   mux, cancel: runCancel, finalized: make(chan struct{}),
	}
	c.mu.Lock()
	previous := c.live[installation]
	c.live[installation] = l
	delete(c.retryAt, installation)
	c.mu.Unlock()
	c.wg.Add(3)
	go func() { defer c.wg.Done(); _ = mux.Run(runCtx) }()
	go func() { defer c.wg.Done(); c.accept(runCtx, l) }()
	go func() { defer c.wg.Done(); c.watch(l) }()
	replacedID := ""
	if previous != nil {
		replacedID = previous.state.sessionID
	}
	c.auditLog(platformServiceEgressAuditOpened, installation, map[string]any{
		"session_id": sessionID, "replaced_session_id": replacedID, "resources": l.state.resources,
		"generation": a.facts.generation, "policy_revision": a.facts.policyRevision,
		"release_sha256": a.facts.release, "concurrency": a.concurrency,
	})
	if previous != nil {
		c.closeLive(previous, "renewed")
	}
	return nil
}

func (c *platformServiceEgressCoordinator) auditLog(action, installation string, detail map[string]any) {
	if err := c.server.writePlatformServiceEgressAudit(action, installation, c.now(), detail); err != nil {
		log.Printf("application egress: audit %s: %v", action, err)
	}
}

// closeLive ends a session with reason (the first reason given wins) and waits
// for its closing audit row.
func (c *platformServiceEgressCoordinator) closeLive(l *platformServiceEgressLive, reason string) {
	l.mu.Lock()
	if l.reason == "" {
		l.reason = reason
	}
	l.mu.Unlock()
	l.mux.Close(platformServiceEgressReasonByte(reason))
	select {
	case <-l.finalized:
	case <-time.After(5 * time.Second):
	}
}

// watch finalizes a session when it ends, whoever ended it: it records why,
// stops the session's calls, forgets it and writes the closing audit row.
func (c *platformServiceEgressCoordinator) watch(l *platformServiceEgressLive) {
	<-l.mux.Done()
	l.cancel()
	l.calls.Wait()
	l.mu.Lock()
	reason := l.reason
	if reason == "" {
		switch peer := l.mux.PeerReason(); {
		case platformServiceEgressReasonNames[peer] != "":
			reason = platformServiceEgressReasonNames[peer]
		case c.root.Err() != nil:
			reason = "shutdown"
		case c.now().UnixMilli() >= l.state.expiresMS:
			reason = "expired"
		default:
			reason = "connection_lost"
		}
		l.reason = reason
	}
	l.mu.Unlock()
	installation := l.state.pin.installation
	c.mu.Lock()
	if c.live[installation] == l {
		delete(c.live, installation)
	}
	c.mu.Unlock()
	c.auditLog(platformServiceEgressAuditClosed, installation, map[string]any{"session_id": l.state.sessionID, "reason": reason})
	clear(l.state.identity)
	close(l.finalized)
}

func (c *platformServiceEgressCoordinator) flushSummaries(now time.Time, flush bool) {
	for _, row := range c.audit.due(now, flush) {
		if err := c.server.writePlatformServiceEgressAuditRaw(platformServiceEgressAuditSummary, row.installation, now, row.detail); err != nil {
			log.Printf("application egress: audit summary: %v", err)
		}
	}
}

// accept serves the calls a session's runner starts. Every call is answered
// exactly once, on its own goroutine, so a slow authority check never stalls
// the read loop (which belongs to the mux and never waits for SQL).
func (c *platformServiceEgressCoordinator) accept(ctx context.Context, l *platformServiceEgressLive) {
	for {
		call, err := l.mux.Accept(ctx)
		if err != nil {
			return
		}
		l.calls.Add(1)
		go func() {
			defer l.calls.Done()
			defer call.Finish()
			c.serveCall(ctx, l, call)
		}()
	}
}

func (c *platformServiceEgressCoordinator) refuse(call *transport.EgressCall, code byte) {
	_ = call.Send(transport.EgressFrameRefusal, []byte{code})
}

func (c *platformServiceEgressCoordinator) serveCall(ctx context.Context, l *platformServiceEgressLive, call *transport.EgressCall) {
	ctx, cancel := context.WithTimeout(ctx, platformServiceEgressCallTimeout)
	defer cancel()
	first, err := call.Recv(ctx)
	if err != nil {
		return
	}
	switch first.Type {
	case transport.EgressFrameAdmit:
		c.serveAdmit(ctx, l, call, first.Payload)
	case transport.EgressFrameNotice:
		c.serveNotice(ctx, l, call, first.Payload)
	}
}

func (c *platformServiceEgressCoordinator) serveAdmit(ctx context.Context, l *platformServiceEgressLive, call *transport.EgressCall, payload []byte) {
	installation := l.state.pin.installation
	admit, err := transport.DecodeEgressAdmit(payload)
	if err != nil {
		c.refuse(call, transport.EgressRefusalInvalid)
		return
	}
	origin := platformServiceEgressOrigin(admit.URL)
	load, cancel := context.WithTimeout(ctx, platformServiceEgressAdmitLoadTimeout)
	a, err := c.server.loadServiceEgressForSession(load, l.state)
	cancel()
	if err != nil {
		c.refuse(call, transport.EgressRefusalUnavailable)
		c.audit.recordCall(installation, l.state.sessionID, c.now(), transport.EgressOutcomeUnavailable, origin, 0, 0)
		return
	}
	command, raw, signature, err := a.signServiceEgressCommand(l.state.sessionID, admit, time.Now().UnixMilli(), l.state.expiresMS)
	a.Close()
	if err != nil {
		code, class := transport.EgressRefusalUnavailable, transport.EgressOutcomeUnavailable
		if errors.Is(err, errPlatformServiceEgressDenied) {
			code, class = transport.EgressRefusalDenied, transport.EgressOutcomeDenied
			c.recordDenied(installation, l, admit.Method, origin)
		}
		c.refuse(call, code)
		c.audit.recordCall(installation, l.state.sessionID, c.now(), class, origin, 0, 0)
		return
	}
	frame, err := transport.EncodeEgressSigned(raw, signature)
	if err != nil || call.Send(transport.EgressFrameCommand, frame) != nil {
		return
	}
	for {
		f, err := call.Recv(ctx)
		if err != nil {
			return
		}
		switch f.Type {
		case transport.EgressFrameChallenge:
			_, nonce, err := transport.DecodeEgressChallenge(f.Payload)
			if err != nil {
				return
			}
			proof, proofSignature, err := c.server.approveServiceEgressCheck(ctx, l.state, command, raw, hex.EncodeToString(nonce[:]))
			if err != nil {
				// A refused check is the absence of a proof: answering twice would
				// close the session, and the runner treats silence as a denial.
				continue
			}
			payload, err := transport.EncodeEgressSigned(proof, proofSignature)
			if err != nil || call.Send(transport.EgressFrameProof, payload) != nil {
				return
			}
		case transport.EgressFrameOutcome:
			outcome, err := transport.DecodeEgressOutcome(f.Payload)
			if err != nil {
				return
			}
			c.audit.recordCall(installation, l.state.sessionID, c.now(), outcome.Class, origin, admit.BodyBytes, outcome.ResponseBytes)
			if outcome.Class == transport.EgressOutcomeDenied {
				c.recordDenied(installation, l, admit.Method, origin)
			}
			return
		default:
			return
		}
	}
}

// recordDenied writes the individual denied row, at most
// platformServiceEgressDeniedPerWindow per installation per window; later ones
// are only counted in the summary. It carries method, origin and session, never
// the path or the query.
func (c *platformServiceEgressCoordinator) recordDenied(installation string, l *platformServiceEgressLive, method, origin string) {
	if !c.audit.admitDenied(installation, l.state.sessionID, c.now()) {
		return
	}
	c.auditLog(platformServiceEgressAuditDenied, installation, map[string]any{"method": method, "origin": origin, "session_id": l.state.sessionID})
}

func (c *platformServiceEgressCoordinator) serveNotice(ctx context.Context, l *platformServiceEgressLive, call *transport.EgressCall, payload []byte) {
	installation := l.state.pin.installation
	notice, err := transport.DecodeEgressNotice(payload)
	if err != nil {
		c.refuse(call, transport.EgressRefusalInvalid)
		return
	}
	if !slices.Contains(l.state.resources, platformServiceEgressNotice) {
		c.refuse(call, transport.EgressRefusalUnavailable)
		return
	}
	load, cancel := context.WithTimeout(ctx, platformServiceEgressAdmitLoadTimeout)
	a, err := c.server.loadServiceEgressForSession(load, l.state)
	cancel()
	if err != nil {
		c.refuse(call, transport.EgressRefusalUnavailable)
		return
	}
	appID := a.facts.appID
	a.Close()
	id, created, err := c.server.publishPlatformServiceNotification(appID, notice.JSON())
	switch {
	case errors.Is(err, errPlatformManagementInput):
		c.refuse(call, transport.EgressRefusalInvalid)
		return
	case errors.Is(err, errNotificationConflict):
		c.refuse(call, transport.EgressRefusalConflict)
		return
	case errors.Is(err, errNotificationCapacity):
		c.refuse(call, transport.EgressRefusalCapacity)
		return
	case err != nil:
		c.refuse(call, transport.EgressRefusalUnavailable)
		return
	}
	result, err := transport.EncodeEgressNoticeResult(transport.EgressNoticeResult{EventID: id, Created: created})
	if err != nil || call.Send(transport.EgressFrameNoticeResult, result) != nil {
		return
	}
	c.audit.recordNotice(installation, l.state.sessionID, c.now())
}
