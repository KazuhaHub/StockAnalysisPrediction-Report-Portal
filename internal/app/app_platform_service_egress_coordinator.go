package app

import (
	"context"
	"encoding/hex"
	"errors"
	"log"
	"slices"
	"strings"
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
	platformServiceEgressReloadTimeout    = 2 * time.Second
	platformServiceEgressDriftParallel    = 8
	platformServiceEgressLogInterval      = 5 * time.Minute
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
	// loadSession reads the authority a session is still bound to; reloadTimeout
	// bounds each such reload of the drift loop. Both are seams for tests.
	loadSession   func(context.Context, platformServiceEgressSessionState) (platformServiceEgressAuthority, error)
	reloadTimeout time.Duration
	logf          func(format string, args ...any)

	root       context.Context
	rootCancel context.CancelFunc
	wg         sync.WaitGroup
	stopped    chan struct{}
	stopOnce   sync.Once

	mu               sync.Mutex
	live             map[string]*platformServiceEgressLive
	draining         map[string]*platformServiceEgressLive // replaced sessions, by session id, until the runner closes them
	retryAt          map[string]time.Time
	loggedAt         map[string]time.Time // last "opening a session failed" log, by installation
	unsupportedUntil time.Time
}

func newPlatformServiceEgressCoordinator(server *Server, t platformServiceEgressTransport) *platformServiceEgressCoordinator {
	root, cancel := context.WithCancel(context.Background())
	return &platformServiceEgressCoordinator{
		server: server, transport: t, loadSession: server.loadServiceEgressForSession, reloadTimeout: platformServiceEgressReloadTimeout, logf: log.Printf, endpoint: server.platformRuntimeEndpoint, now: time.Now,
		audit: newPlatformServiceEgressAggregator(), root: root, rootCancel: cancel, stopped: make(chan struct{}),
		live: map[string]*platformServiceEgressLive{}, draining: map[string]*platformServiceEgressLive{}, retryAt: map[string]time.Time{}, loggedAt: map[string]time.Time{},
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
	out := make([]*platformServiceEgressLive, 0, len(c.live)+len(c.draining))
	for _, l := range c.live {
		out = append(out, l)
	}
	for _, l := range c.draining {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b *platformServiceEgressLive) int {
		if r := strings.Compare(a.state.pin.installation, b.state.pin.installation); r != 0 {
			return r
		}
		return strings.Compare(a.state.sessionID, b.state.sessionID)
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
	c.checkDrift(ctx, now)
	if endpoint, ok := c.endpoint(); ok && now.After(c.unsupportedUntil) {
		eligible, listed := c.server.listPlatformServiceEgressInstallations(ctx)
		if listed {
			c.pruneRetry(eligible)
		}
		for _, installation := range eligible {
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
				logIt := now.Sub(c.loggedAt[installation]) >= platformServiceEgressLogInterval
				if logIt {
					c.loggedAt[installation] = now
				}
				c.mu.Unlock()
				if cur != nil && !cur.ended() && now.UnixMilli() >= cur.state.expiresMS {
					c.closeLive(cur, "expired")
				}
				// Once per installation per interval: a runner that is down fails
				// every retry and would otherwise fill the log.
				if logIt && !errors.Is(err, errPlatformServiceEgressUnavailable) && !errors.Is(err, transport.ErrServiceEgressUnsupported) {
					c.logf("application egress: opening a session for installation %s failed: %v", installation, err)
				}
			}
		}
	}
	c.flushSummaries(now, false)
}

// checkDrift closes every session whose current authority is no longer the one
// it was opened against, whatever the reason, and every session whose assertion
// has ended. Each reload has its own bounded context and the sessions are checked
// side by side, so one reload that never returns costs one session (it fails
// closed) and cannot hold up the renewals that follow.
func (c *platformServiceEgressCoordinator) checkDrift(ctx context.Context, now time.Time) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, platformServiceEgressDriftParallel)
	for _, l := range c.snapshot() {
		if l.ended() {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			load, cancel := context.WithTimeout(ctx, c.reloadTimeout)
			a, err := c.loadSession(load, l.state)
			if err != nil {
				if ctx.Err() != nil {
					cancel()
					return
				}
				// Classifying is a read too and has its own bound; when nothing
				// observable changed (a reload that merely timed out) it says drift.
				classify, stop := context.WithTimeout(ctx, c.reloadTimeout)
				reason := c.server.classifyPlatformServiceEgressLoss(classify, l.state.pin)
				stop()
				cancel()
				c.closeLive(l, reason)
				return
			}
			cancel()
			a.Close()
			if now.UnixMilli() >= l.state.expiresMS {
				c.closeLive(l, "expired")
			}
		}()
	}
	wg.Wait()
}

// listPlatformServiceEgressInstallations are the installations that could hold a
// session: enabled and ready with a runtime, background.run granted and at least
// one egress resource granted. It is a filter, not authority: opening re-reads
// everything. listed is false when the query failed, which says nothing about who
// is eligible.
func (s *Server) listPlatformServiceEgressInstallations(ctx context.Context) ([]string, bool) {
	rows, err := s.st.queryContext(ctx, `SELECT i.id FROM app_platform_installations i
		WHERE i.desired_state='enabled' AND i.phase='ready' AND i.runtime_id IS NOT NULL
		AND EXISTS(SELECT 1 FROM app_platform_capability_grants g WHERE g.installation_id=i.id AND g.capability='background.run' AND g.granted=1)
		AND EXISTS(SELECT 1 FROM app_platform_capability_grants g WHERE g.installation_id=i.id AND g.capability IN ('network.http','notifications.publish') AND g.granted=1)
		ORDER BY i.id LIMIT ?`, platformServiceEgressMaxInstallations)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err() == nil
}

// pruneRetry forgets the retry and log state of installations that are no longer
// eligible, so the maps stay bounded by what can hold a session.
func (c *platformServiceEgressCoordinator) pruneRetry(eligible []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, state := range []map[string]time.Time{c.retryAt, c.loggedAt} {
		for installation := range state {
			if !slices.Contains(eligible, installation) {
				delete(state, installation)
			}
		}
	}
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
// session. The runner swaps it in only after verifying it and drains the session
// it replaces (renewed); Portal keeps serving that one until the runner closes it.
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
	if previous != nil && !previous.ended() {
		c.draining[previous.state.sessionID] = previous
	}
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
	// The replaced session is not closed here: calls in flight on it still need
	// their remaining checks answered. The runner marks it draining and closes it
	// once nothing is in flight; until then the coordinator keeps serving it, and
	// the drift loop still guards it and ends it when its assertion does.
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
	delete(c.draining, l.state.sessionID)
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
	// record counts one finished call. A call that named a binding is also counted
	// against that binding, but only once the binding resolved to a reviewed one:
	// a name the application made up is counted as unresolved, never by name.
	resolved := ""
	record := func(class byte, bytesOut int, bytesIn uint32) {
		c.audit.recordCall(installation, l.state.sessionID, c.now(), class, origin, bytesOut, bytesIn)
		if admit.Credential != "" {
			c.audit.recordBinding(installation, l.state.sessionID, c.now(), resolved, class)
		}
	}
	load, cancel := context.WithTimeout(ctx, platformServiceEgressAdmitLoadTimeout)
	a, err := c.server.loadServiceEgressForSessionAdmit(load, l.state, admit)
	cancel()
	if err != nil {
		code, class := transport.EgressRefusalUnavailable, transport.EgressOutcomeUnavailable
		if errors.Is(err, errPlatformServiceEgressDenied) {
			// An unknown, revoked, unreviewed, wrong-principal or wrong-target
			// binding: refused before anything was decrypted.
			code, class = transport.EgressRefusalDenied, transport.EgressOutcomeDenied
			c.recordDenied(installation, l, admit.Method, origin)
		}
		c.refuse(call, code)
		record(class, 0, 0)
		return
	}
	binding := a.binding
	if binding.id != "" {
		resolved = admit.Credential
	}
	command, raw, signature, err := a.signServiceEgressCommand(l.state.sessionID, admit, time.Now().UnixMilli(), l.state.expiresMS)
	a.Close()
	// The signed command is the one place the value lives in Portal outside the
	// transaction; it is wiped when the call ends.
	defer clear(command.CredentialValue)
	defer clear(raw)
	if err != nil {
		code, class := transport.EgressRefusalUnavailable, transport.EgressOutcomeUnavailable
		if errors.Is(err, errPlatformServiceEgressDenied) {
			code, class = transport.EgressRefusalDenied, transport.EgressOutcomeDenied
			c.recordDenied(installation, l, admit.Method, origin)
		}
		c.refuse(call, code)
		record(class, 0, 0)
		return
	}
	frame, err := transport.EncodeEgressSigned(raw, signature)
	if err != nil {
		return
	}
	sent := call.Send(transport.EgressFrameCommand, frame)
	// From here the checks need the command's fields and digest, not the value:
	// wipe the value, the signed bytes and the frame copy now, not at the end of a
	// call that may wait on the upstream for its whole deadline.
	kept := newPlatformServiceEgressCall(command, raw, binding)
	clear(frame)
	clear(raw)
	clear(command.CredentialValue)
	if sent != nil {
		return
	}
	// withheld is set when Portal itself refused a proof (the authority was lost
	// during the call): the runner then reports the call as denied, but that is
	// not a policy refusal and writes no denied row (it is still counted).
	withheld := false
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
			proof, proofSignature, err := c.server.approveServiceEgressCheck(ctx, l.state, kept, hex.EncodeToString(nonce[:]))
			if err != nil {
				// A refused check is the absence of a proof: answering twice would
				// close the session, and the runner treats silence as a denial.
				withheld = true
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
			record(outcome.Class, admit.BodyBytes, outcome.ResponseBytes)
			if outcome.Class == transport.EgressOutcomeDenied && !withheld {
				c.recordDenied(installation, l, admit.Method, origin)
			}
			return
		default:
			return
		}
	}
}

// recordDenied writes the individual denied row, at most
// platformServiceEgressDeniedPerWindow per installation in any sliding window
// (rows written before a restart count); later ones are only counted in the
// summary. It carries method, origin and session, never
// the path or the query.
func (c *platformServiceEgressCoordinator) recordDenied(installation string, l *platformServiceEgressLive, method, origin string) {
	now := c.now()
	if !c.audit.seeded(installation) {
		c.audit.seed(installation, c.server.recentPlatformServiceEgressDenied(installation, now))
	}
	if !c.audit.admitDenied(installation, l.state.sessionID, now) {
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
