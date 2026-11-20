package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// fakeEgressRunner stands in for the runner's endpoint: it answers the capability
// query, verifies the signed session exactly as the runner does, and hands the
// test the runner's end of the multiplexed connection.
type fakeEgressRunner struct {
	mu       sync.Mutex
	profile  transport.ServiceEgressProfile
	queryErr error
	// proxyProfile and proxyErr are the PRPX0001 answer; proxyAsked counts the questions.
	proxyProfile transport.ProxyProfile
	proxyErr     error
	proxyAsked   int
	queries      int
	opens        int
	openErr      error
	sessions     chan *fakeEgressSession
}

type fakeEgressSession struct {
	runner    *transport.EgressMux
	command   control.ServiceEgressSessionCommand
	authority control.Authority
}

func newFakeEgressRunner() *fakeEgressRunner {
	return &fakeEgressRunner{profile: transport.NewServiceEgressProfile(4), proxyProfile: transport.NewProxyProfile(), sessions: make(chan *fakeEgressSession, 8)}
}

// QueryProxy answers the PRPX0001 query (doc 184).
func (f *fakeEgressRunner) QueryProxy(context.Context, platformRuntimeEndpoint) (transport.ProxyProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proxyAsked++
	return f.proxyProfile, f.proxyErr
}

func (f *fakeEgressRunner) setProxy(p transport.ProxyProfile, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proxyProfile, f.proxyErr = p, err
}

func (f *fakeEgressRunner) proxyQueries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proxyAsked
}

func (f *fakeEgressRunner) Query(context.Context, platformRuntimeEndpoint) (transport.ServiceEgressProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	if f.queryErr != nil {
		return transport.ServiceEgressProfile{}, f.queryErr
	}
	return f.profile, nil
}

func (f *fakeEgressRunner) Open(_ context.Context, _ platformRuntimeEndpoint, raw, signature []byte, a control.Authority, descriptor []byte) (*transport.EgressMux, error) {
	f.mu.Lock()
	err := f.openErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	verified, err := control.VerifyServiceEgressSession(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return nil, transport.ErrRefused
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != verified.Command().ReleaseSHA256 {
		return nil, transport.ErrRefused
	}
	f.mu.Lock()
	f.opens++
	f.mu.Unlock()
	runnerEnd, portalEnd := net.Pipe()
	runner := transport.NewEgressMux(runnerEnd, transport.EgressRunner)
	go func() { _ = runner.Run(context.Background()) }()
	f.sessions <- &fakeEgressSession{runner: runner, command: verified.Command(), authority: a}
	return transport.NewEgressMux(portalEnd, transport.EgressPortal), nil
}

func (f *fakeEgressRunner) counts() (queries, opens int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries, f.opens
}

type egressTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *egressTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *egressTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newEgressTestCoordinator(t *testing.T, s *Server, f *fakeEgressRunner) (*platformServiceEgressCoordinator, *egressTestClock) {
	t.Helper()
	c := newPlatformServiceEgressCoordinator(s, f)
	clock := &egressTestClock{now: time.Now()}
	c.now = clock.Now
	c.endpoint = func() (platformRuntimeEndpoint, bool) {
		return platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001}, true
	}
	t.Cleanup(c.shutdown)
	return c, clock
}

func nextEgressSession(t *testing.T, f *fakeEgressRunner) *fakeEgressSession {
	t.Helper()
	select {
	case session := <-f.sessions:
		return session
	case <-time.After(10 * time.Second):
		t.Fatal("no session was opened")
		return nil
	}
}

func requireNoEgressSession(t *testing.T, f *fakeEgressRunner) {
	t.Helper()
	select {
	case <-f.sessions:
		t.Fatal("a session was opened")
	default:
	}
}

func waitEgressEnded(t *testing.T, m *transport.EgressMux, reason byte) {
	t.Helper()
	select {
	case <-m.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("session did not end")
	}
	if m.PeerReason() != reason {
		t.Fatalf("session closed with reason %d, want %d", m.PeerReason(), reason)
	}
}

func egressAuditRows(t *testing.T, s *Server, action, installation string) []string {
	t.Helper()
	rows, err := s.st.query(`SELECT detail FROM audit_log WHERE action=? AND target_type='app_platform_installation' AND target_id=? AND actor='system' ORDER BY id`, action, installation)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return out
}

func egressAuditField(t *testing.T, detail, key string) any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(detail), &v); err != nil {
		t.Fatal(err)
	}
	return v[key]
}

func testPlatformServiceEgressCoordinatorOpensRenewsCloses(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	ctx := context.Background()
	c.tick(ctx)
	first := nextEgressSession(t, f)
	cmd := first.command
	if strings.Join(cmd.Resources, ",") != "network.http,notifications.publish" || cmd.Concurrency != "1" || cmd.InstallationID != installation ||
		cmd.RatePerMinute != "120" || cmd.Burst != "20" || len(cmd.RegistrationRaw) == 0 {
		t.Fatal("session command", cmd.Resources, cmd.Concurrency)
	}
	if rows := egressAuditRows(t, s, platformServiceEgressAuditOpened, installation); len(rows) != 1 ||
		egressAuditField(t, rows[0], "session_id") != cmd.SessionID || egressAuditField(t, rows[0], "replaced_session_id") != "" {
		t.Fatal("opened audit", rows)
	}
	// Nothing to do until the renewal window: no second session.
	c.tick(ctx)
	if _, opens := f.counts(); opens != 1 {
		t.Fatal("a session was opened again before renewal", opens)
	}
	// 60 s before expiry the replacement is opened. Portal does not close the old
	// session: the runner drains it and closes it when nothing is in flight.
	clock.Advance(4*time.Minute + 10*time.Second)
	c.tick(ctx)
	second := nextEgressSession(t, f)
	if second.command.SessionID == cmd.SessionID {
		t.Fatal("renewal reused the session id")
	}
	select {
	case <-first.runner.Done():
		t.Fatal("Portal closed the replaced session itself")
	case <-time.After(200 * time.Millisecond):
	}
	first.runner.Close(transport.EgressCloseRenewed)
	waitEgress(t, func() bool { return len(egressAuditRows(t, s, platformServiceEgressAuditClosed, installation)) == 1 })
	rows := egressAuditRows(t, s, platformServiceEgressAuditOpened, installation)
	if len(rows) != 2 || egressAuditField(t, rows[1], "replaced_session_id") != cmd.SessionID {
		t.Fatal("renewal audit", rows)
	}
	if closed := egressAuditRows(t, s, platformServiceEgressAuditClosed, installation); len(closed) != 1 ||
		egressAuditField(t, closed[0], "session_id") != cmd.SessionID || egressAuditField(t, closed[0], "reason") != "renewed" {
		t.Fatal("renewal close audit", closed)
	}
	// Disabling the installation closes the session on the next tick, for good.
	mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, installation)
	c.tick(ctx)
	waitEgressEnded(t, second.runner, transport.EgressCloseDisabled)
	closed := egressAuditRows(t, s, platformServiceEgressAuditClosed, installation)
	if len(closed) != 2 || egressAuditField(t, closed[1], "reason") != "disabled" {
		t.Fatal("disable close audit", closed)
	}
	c.tick(ctx)
	requireNoEgressSession(t, f)
	if c.current(installation) != nil {
		t.Fatal("a closed session is still current")
	}
}

// A call that is in flight when the session is renewed keeps its session: its
// remaining checks are answered on the old one until the runner closes it.
func testPlatformServiceEgressCoordinatorRenewalKeepsInFlightCalls(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	ctx := context.Background()
	c.tick(ctx)
	first := nextEgressSession(t, f)
	payload, _ := transport.EncodeEgressAdmit(platformServiceEgressAdmit())
	call, err := first.runner.NewCall()
	if err != nil {
		t.Fatal(err)
	}
	defer call.Finish()
	if err := call.Send(transport.EgressFrameAdmit, payload); err != nil {
		t.Fatal(err)
	}
	raw, signature, err := transport.DecodeEgressCommand(egressRecv(t, call, transport.EgressFrameCommand).Payload)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, first.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	command := verified.Command()
	check := func(seq byte) {
		t.Helper()
		var nonce [32]byte
		nonce[0], nonce[31] = 'n', seq
		if err := call.Send(transport.EgressFrameChallenge, transport.EncodeEgressChallenge(seq, nonce)); err != nil {
			t.Fatal(err)
		}
		proof, sig, err := transport.DecodeEgressProof(egressRecv(t, call, transport.EgressFrameProof).Payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := control.VerifyNetworkHTTPCheck(command, hex.EncodeToString(nonce[:]), proof, sig, first.authority, time.Now().UnixMilli()); err != nil {
			t.Fatal("proof does not verify:", err)
		}
	}
	check(1)
	// The renewal happens in the middle of the call.
	clock.Advance(4*time.Minute + 10*time.Second)
	c.tick(ctx)
	second := nextEgressSession(t, f)
	check(2)
	check(3)
	check(4)
	outcome, _ := transport.EncodeEgressOutcome(transport.EgressOutcome{Class: transport.EgressOutcomeOK, Status: 200, ResponseBytes: 5, RetryAfter: -1})
	if err := call.Send(transport.EgressFrameOutcome, outcome); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.runner.Done():
		t.Fatal("the replaced session was closed while a call was in flight")
	default:
	}
	// New calls go to the new session; the old one closes when the runner says so.
	if c.current(installation).state.sessionID != second.command.SessionID {
		t.Fatal("the replacement is not current")
	}
	first.runner.Close(transport.EgressCloseRenewed)
	waitEgress(t, func() bool { return len(egressAuditRows(t, s, platformServiceEgressAuditClosed, installation)) == 1 })
	if closed := egressAuditRows(t, s, platformServiceEgressAuditClosed, installation)[0]; egressAuditField(t, closed, "reason") != "renewed" ||
		egressAuditField(t, closed, "session_id") != first.command.SessionID {
		t.Fatal("close audit", closed)
	}
	select {
	case <-second.runner.Done():
		t.Fatal("the new session ended with the old one")
	default:
	}
}

// A replaced session that the runner has not closed yet is guarded like any
// other: drift closes it, and so does the end of its assertion.
func testPlatformServiceEgressCoordinatorReplacedSessionStillGuarded(t *testing.T, pg bool) {
	for _, name := range []string{"drift", "expiry"} {
		t.Run(name, func(t *testing.T) {
			s, installation := platformServiceEgressFixture(t, pg, true)
			f := newFakeEgressRunner()
			c, clock := newEgressTestCoordinator(t, s, f)
			ctx := context.Background()
			c.tick(ctx)
			first := nextEgressSession(t, f)
			clock.Advance(4*time.Minute + 10*time.Second)
			c.tick(ctx)
			second := nextEgressSession(t, f)
			if name == "drift" {
				mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, installation)
				c.tick(ctx)
				waitEgressEnded(t, first.runner, transport.EgressCloseDisabled)
				waitEgressEnded(t, second.runner, transport.EgressCloseDisabled)
				return
			}
			clock.Advance(time.Minute)
			c.tick(ctx)
			waitEgressEnded(t, first.runner, transport.EgressCloseExpired)
			waitEgressEnded(t, second.runner, transport.EgressCloseExpired)
		})
	}
}

// A reload that never returns is bounded, and reloads of different sessions run
// side by side, so one stuck session cannot hold up the rest of the tick.
func testPlatformServiceEgressCoordinatorBoundsDriftReloads(t *testing.T, pg bool) {
	s, _ := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	ctx := context.Background()
	c.tick(ctx)
	first := nextEgressSession(t, f)
	clock.Advance(4*time.Minute + 10*time.Second)
	c.tick(ctx)
	second := nextEgressSession(t, f)
	c.reloadTimeout = time.Second
	var started sync.WaitGroup
	started.Add(2)
	c.loadSession = func(ctx context.Context, _ platformServiceEgressSessionState) (platformServiceEgressAuthority, error) {
		started.Done()
		<-ctx.Done()
		return platformServiceEgressAuthority{}, ctx.Err()
	}
	begin := time.Now()
	done := make(chan struct{})
	go func() { c.tick(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a stuck reload stalled the tick")
	}
	if took := time.Since(begin); took > 1800*time.Millisecond {
		t.Fatal("reloads ran one after the other", took)
	}
	started.Wait()
	// A reload that cannot finish in time fails closed.
	waitEgressEnded(t, first.runner, transport.EgressCloseDrift)
	waitEgressEnded(t, second.runner, transport.EgressCloseDrift)
}

// A denial that comes from Portal withholding a proof (authority lost during the
// call) is not a policy refusal: A8 limits denied rows to those. It is still
// counted in the summary, and a runner-side policy denial still writes its row.
func testPlatformServiceEgressCoordinatorWithheldProofWritesNoDeniedRow(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	runCall := func(withhold bool) {
		payload, _ := transport.EncodeEgressAdmit(platformServiceEgressAdmit())
		call, err := session.runner.NewCall()
		if err != nil {
			t.Fatal(err)
		}
		defer call.Finish()
		if err := call.Send(transport.EgressFrameAdmit, payload); err != nil {
			t.Fatal(err)
		}
		egressRecv(t, call, transport.EgressFrameCommand)
		if withhold {
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, installation)
			defer mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)
			var nonce [32]byte
			if err := call.Send(transport.EgressFrameChallenge, transport.EncodeEgressChallenge(1, nonce)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(200 * time.Millisecond) // the check is refused: no proof comes back
		}
		outcome, _ := transport.EncodeEgressOutcome(transport.EgressOutcome{Class: transport.EgressOutcomeDenied, RetryAfter: -1})
		if err := call.Send(transport.EgressFrameOutcome, outcome); err != nil {
			t.Fatal(err)
		}
		waitEgress(t, func() bool {
			c.audit.mu.Lock()
			defer c.audit.mu.Unlock()
			w := c.audit.windows[installation]
			return w != nil && w.counts["denied"] >= 1
		})
	}
	runCall(true)
	if rows := egressAuditRows(t, s, platformServiceEgressAuditDenied, installation); len(rows) != 0 {
		t.Fatal("a withheld proof was recorded as a policy denial", rows)
	}
	runCall(false)
	waitEgress(t, func() bool { return len(egressAuditRows(t, s, platformServiceEgressAuditDenied, installation)) == 1 })
	clock.Advance(11 * time.Minute)
	c.flushSummaries(clock.Now(), false)
	rows := egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)
	if len(rows) != 1 || egressAuditField(t, rows[0], "counts").(map[string]any)["denied"] != float64(2) {
		t.Fatal("both denials are counted in the summary", rows)
	}
}

// The denied cap is a sliding one: it holds across the end of a summary window and
// across a restart of the coordinator (the rows already written count).
func testPlatformServiceEgressDeniedCapSlidesAndSurvivesRestart(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	nextEgressSession(t, f)
	l := c.current(installation)
	deniedRows := func() int { return len(egressAuditRows(t, s, platformServiceEgressAuditDenied, installation)) }
	// The window opens at the first call; the ten rows come half a minute later.
	c.audit.recordCall(installation, l.state.sessionID, clock.Now(), transport.EgressOutcomeDenied, "https://api.example.com", 0, 0)
	clock.Advance(30 * time.Second)
	for range 10 {
		c.recordDenied(installation, l, "GET", "https://api.example.com")
	}
	if deniedRows() != 10 {
		t.Fatal("first window", deniedRows())
	}
	// The summary window ends here; the cap does not reset with it.
	clock.Advance(9*time.Minute + 30*time.Second)
	c.flushSummaries(clock.Now(), false)
	if len(egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)) != 1 {
		t.Fatal("the summary window did not end")
	}
	c.recordDenied(installation, l, "GET", "https://api.example.com")
	if deniedRows() != 10 {
		t.Fatal("the cap reset at a window boundary", deniedRows())
	}
	// A restarted coordinator counts what is already in the audit log.
	restarted := newPlatformServiceEgressCoordinator(s, f)
	restarted.now = clock.Now
	t.Cleanup(restarted.shutdown)
	restarted.recordDenied(installation, l, "GET", "https://api.example.com")
	if deniedRows() != 10 {
		t.Fatal("the cap reset with a restart", deniedRows())
	}
	// Once the rows are more than a window old, writing resumes.
	clock.Advance(time.Minute + 5*time.Second)
	restarted.recordDenied(installation, l, "GET", "https://api.example.com")
	if deniedRows() != 11 {
		t.Fatal("the cap never slid", deniedRows())
	}
}

// "The runner is down" is logged once per installation per interval, not at every
// retry, and the retry delay of an installation that is no longer eligible is
// forgotten.
func testPlatformServiceEgressCoordinatorLogsOpenFailuresOncePerInterval(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	f.openErr = transport.ErrRefused
	c, clock := newEgressTestCoordinator(t, s, f)
	var mu sync.Mutex
	var logged []string
	c.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, format)
	}
	ctx := context.Background()
	for range 10 { // every 16 s: ten retries in under three minutes
		c.tick(ctx)
		clock.Advance(16 * time.Second)
	}
	mu.Lock()
	n := len(logged)
	mu.Unlock()
	if n != 1 {
		t.Fatal("open failures were logged", n, "times")
	}
	clock.Advance(platformServiceEgressLogInterval)
	c.tick(ctx)
	mu.Lock()
	n = len(logged)
	mu.Unlock()
	if n != 2 {
		t.Fatal("open failures were not logged again after the interval", n)
	}
	// The installation stops being eligible: its retry state goes away.
	c.mu.Lock()
	if _, ok := c.retryAt[installation]; !ok {
		c.mu.Unlock()
		t.Fatal("no retry state to prune")
	}
	c.mu.Unlock()
	mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, installation)
	c.tick(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.retryAt) != 0 || len(c.loggedAt) != 0 {
		t.Fatal("state of an ineligible installation was kept", c.retryAt, c.loggedAt)
	}
}

func testPlatformServiceEgressCoordinatorSkipsRunnerWithoutProfile(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	f.queryErr = transport.ErrServiceEgressUnsupported
	c, clock := newEgressTestCoordinator(t, s, f)
	ctx := context.Background()
	c.tick(ctx)
	c.tick(ctx)
	if queries, opens := f.counts(); queries != 1 || opens != 0 {
		t.Fatal("an old runner was asked again or opened", queries, opens)
	}
	// A minute later it is asked again (it may have been upgraded); a runner that
	// reports no background share is skipped just the same.
	f.mu.Lock()
	f.queryErr, f.profile.BackgroundActive = nil, 0
	f.mu.Unlock()
	clock.Advance(2 * time.Minute)
	c.tick(ctx)
	if _, opens := f.counts(); opens != 0 {
		t.Fatal("a runner with egress disabled was opened against")
	}
	requireNoEgressSession(t, f)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action LIKE 'app_platform.service_egress_%' AND target_id=?`, installation) != 0 {
		t.Fatal("skipping a runner wrote audit")
	}
	// Once it can host sessions, it gets one.
	f.mu.Lock()
	f.profile = transport.NewServiceEgressProfile(4)
	f.mu.Unlock()
	clock.Advance(2 * time.Minute)
	c.tick(ctx)
	nextEgressSession(t, f)
}

func testPlatformServiceEgressCoordinatorClosesOnRestorePause(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	c, _ := newEgressTestCoordinator(t, s, f)
	ctx := context.Background()
	c.tick(ctx)
	session := nextEgressSession(t, f)
	mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
	c.tick(ctx)
	waitEgressEnded(t, session.runner, transport.EgressCloseRestorePause)
	if closed := egressAuditRows(t, s, platformServiceEgressAuditClosed, installation); len(closed) != 1 || egressAuditField(t, closed[0], "reason") != "restore_pause" {
		t.Fatal("restore pause close audit", closed)
	}
	c.tick(ctx)
	requireNoEgressSession(t, f)
	mustExec(t, s.st, `UPDATE meta SET v='0' WHERE k='app_platform_restore_paused'`)
	c.mu.Lock()
	delete(c.retryAt, installation)
	c.mu.Unlock()
	c.tick(ctx)
	nextEgressSession(t, f)
}

func egressRecv(t *testing.T, call *transport.EgressCall, want byte) transport.EgressFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := call.Recv(ctx)
	if err != nil || f.Type != want {
		t.Fatalf("frame type %d, want %d: %v", f.Type, want, err)
	}
	return f
}

func testPlatformServiceEgressCoordinatorServesCalls(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	admit := transport.EgressAdmit{Method: "GET", URL: "https://api.example.com/v1/x", Accept: "application/json", BodySHA256: platformDigest(nil)}
	payload, err := transport.EncodeEgressAdmit(admit)
	if err != nil {
		t.Fatal(err)
	}
	call, err := session.runner.NewCall()
	if err != nil {
		t.Fatal(err)
	}
	defer call.Finish()
	if err := call.Send(transport.EgressFrameAdmit, payload); err != nil {
		t.Fatal(err)
	}
	raw, signature, err := transport.DecodeEgressCommand(egressRecv(t, call, transport.EgressFrameCommand).Payload)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, session.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal("the runner would refuse the command:", err)
	}
	command := verified.Command()
	if command.ActorScope != platformServiceEgressActorScope(session.command.SessionID) || command.URL != admit.URL ||
		command.BodySHA256 != admit.BodySHA256 || command.InstallationID != installation {
		t.Fatal("command does not match the admit and the session")
	}
	// Each of the first checks is answered by a proof that verifies; a check that
	// no longer passes is answered by no proof at all.
	ask := func(seq byte) (proof, sig []byte, answered bool) {
		var nonce [32]byte
		copy(nonce[:], strings.Repeat("n", 32))
		nonce[31] = seq
		if err := call.Send(transport.EgressFrameChallenge, transport.EncodeEgressChallenge(seq, nonce)); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		frame, err := call.Recv(ctx)
		if err != nil {
			return nil, nil, false
		}
		if frame.Type != transport.EgressFrameProof {
			t.Fatal("unexpected frame", frame.Type)
		}
		proof, sig, err = transport.DecodeEgressProof(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := control.VerifyNetworkHTTPCheck(command, hex.EncodeToString(nonce[:]), proof, sig, session.authority, time.Now().UnixMilli()); err != nil {
			t.Fatal("proof does not verify:", err)
		}
		return proof, sig, true
	}
	if _, _, ok := ask(1); !ok {
		t.Fatal("first check unanswered")
	}
	if _, _, ok := ask(2); !ok {
		t.Fatal("second check unanswered")
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, installation)
	if _, _, ok := ask(3); ok {
		t.Fatal("a check was answered after network.http was revoked")
	}
	outcome, _ := transport.EncodeEgressOutcome(transport.EgressOutcome{Class: transport.EgressOutcomeUncertain, RetryAfter: -1})
	if err := call.Send(transport.EgressFrameOutcome, outcome); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)

	// A second call that the reviewed policy does not allow is refused, and the
	// refusal names method and origin, never the path or query.
	denied, err := session.runner.NewCall()
	if err != nil {
		t.Fatal(err)
	}
	defer denied.Finish()
	bad, _ := transport.EncodeEgressAdmit(transport.EgressAdmit{Method: "GET", URL: "https://api.example.com/admin?token=hunter2", BodySHA256: platformDigest(nil)})
	if err := denied.Send(transport.EgressFrameAdmit, bad); err != nil {
		t.Fatal(err)
	}
	if code, err := transport.DecodeEgressRefusal(egressRecv(t, denied, transport.EgressFrameRefusal).Payload); err != nil || code != transport.EgressRefusalDenied {
		t.Fatal("policy refusal", code, err)
	}
	waitEgress(t, func() bool { return len(egressAuditRows(t, s, platformServiceEgressAuditDenied, installation)) == 1 })
	row := egressAuditRows(t, s, platformServiceEgressAuditDenied, installation)[0]
	if strings.Contains(row, "admin") || strings.Contains(row, "hunter2") || egressAuditField(t, row, "origin") != "https://api.example.com" ||
		egressAuditField(t, row, "method") != "GET" || egressAuditField(t, row, "session_id") != session.command.SessionID {
		t.Fatal("denied row", row)
	}

	// A successful call is counted from its outcome frame, and the window's one
	// summary row lists counts by class, bytes and the sorted origins.
	ok, err := session.runner.NewCall()
	if err != nil {
		t.Fatal(err)
	}
	defer ok.Finish()
	good, _ := transport.EncodeEgressAdmit(transport.EgressAdmit{Method: "GET", URL: "https://api.example.com/v1/y", BodySHA256: platformDigest(nil)})
	if err := ok.Send(transport.EgressFrameAdmit, good); err != nil {
		t.Fatal(err)
	}
	egressRecv(t, ok, transport.EgressFrameCommand)
	done, _ := transport.EncodeEgressOutcome(transport.EgressOutcome{Class: transport.EgressOutcomeOK, Status: 200, ResponseBytes: 123, RetryAfter: -1})
	if err := ok.Send(transport.EgressFrameOutcome, done); err != nil {
		t.Fatal(err)
	}
	waitEgress(t, func() bool {
		c.audit.mu.Lock()
		defer c.audit.mu.Unlock()
		w := c.audit.windows[installation]
		return w != nil && w.counts["ok"] == 1
	})
	clock.Advance(11 * time.Minute)
	c.flushSummaries(clock.Now(), false)
	rows := egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)
	if len(rows) != 1 {
		t.Fatal("summary rows", rows)
	}
	var summary struct {
		Counts   map[string]int `json:"counts"`
		BytesIn  int64          `json:"bytes_in"`
		BytesOut int64          `json:"bytes_out"`
		Origins  []string       `json:"origins"`
	}
	if err := json.Unmarshal([]byte(rows[0]), &summary); err != nil || summary.Counts["ok"] != 1 || summary.Counts["uncertain"] != 1 || summary.Counts["denied"] != 1 ||
		summary.BytesIn != 123 || len(summary.Origins) != 1 || summary.Origins[0] != "https://api.example.com" {
		t.Fatal("summary", rows[0], err)
	}
	if strings.Contains(rows[0], "/v1") {
		t.Fatal("summary carries a path")
	}
}

func waitEgress(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testPlatformServiceEgressCoordinatorServesNotices(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	resetPlatformServiceEgressNotices(t, s)
	f := newFakeEgressRunner()
	c, _ := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	send := func(occurrence, title string) (byte, transport.EgressNoticeResult) {
		raw, _ := json.Marshal(map[string]any{"occurrenceId": occurrence, "title": title, "message": "m", "severity": "info"})
		notice, err := broker.DecodeAppNotice(raw)
		if err != nil {
			t.Fatal(err)
		}
		call, err := session.runner.NewCall()
		if err != nil {
			t.Fatal(err)
		}
		defer call.Finish()
		if err := call.Send(transport.EgressFrameNotice, notice.JSON()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		frame, err := call.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == transport.EgressFrameRefusal {
			code, err := transport.DecodeEgressRefusal(frame.Payload)
			if err != nil {
				t.Fatal(err)
			}
			return code, transport.EgressNoticeResult{}
		}
		result, err := transport.DecodeEgressNoticeResult(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		return 0, result
	}
	code, first := send("egress-n1", "Hello")
	if code != 0 || !first.Created || first.EventID == "" {
		t.Fatal("first notice", code, first)
	}
	if code, again := send("egress-n1", "Hello"); code != 0 || again.Created || again.EventID != first.EventID {
		t.Fatal("replay", code, again)
	}
	if code, _ := send("egress-n1", "Changed"); code != transport.EgressRefusalConflict {
		t.Fatal("conflict", code)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`) != 1 {
		t.Fatal("replay or conflict captured an event")
	}
	// Without notifications.publish a notice is refused and nothing is captured.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='notifications.publish'`, installation)
	if code, _ := send("egress-n2", "Revoked"); code != transport.EgressRefusalUnavailable {
		t.Fatal("revoked", code)
	}
}

func testPlatformServiceEgressAuditOpenedClosedSummaryDeniedCap(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	l := c.current(installation)
	if l == nil || l.state.sessionID != session.command.SessionID {
		t.Fatal("session is not current")
	}
	for range 13 {
		c.recordDenied(installation, l, "POST", "https://api.example.com")
	}
	if n := len(egressAuditRows(t, s, platformServiceEgressAuditDenied, installation)); n != platformServiceEgressDeniedPerWindow {
		t.Fatal("denied rows are not capped per window", n)
	}
	c.audit.recordCall(installation, l.state.sessionID, clock.Now(), transport.EgressOutcomeDenied, "https://b.example.com", 5, 0)
	c.audit.recordCall(installation, l.state.sessionID, clock.Now(), transport.EgressOutcomeBusy, "https://a.example.com", 0, 0)
	c.flushSummaries(clock.Now().Add(9*time.Minute), false)
	if len(egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)) != 0 {
		t.Fatal("summary written before its window ended")
	}
	c.flushSummaries(clock.Now().Add(10*time.Minute), false)
	rows := egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)
	if len(rows) != 1 {
		t.Fatal("one summary per window", rows)
	}
	var summary struct {
		Counts     map[string]int `json:"counts"`
		BytesOut   int64          `json:"bytes_out"`
		Origins    []string       `json:"origins"`
		Suppressed int            `json:"denied_suppressed"`
	}
	if err := json.Unmarshal([]byte(rows[0]), &summary); err != nil || summary.Counts["denied"] != 1 || summary.Counts["busy"] != 1 || summary.BytesOut != 5 ||
		strings.Join(summary.Origins, ",") != "https://a.example.com,https://b.example.com" || summary.Suppressed != 3 {
		t.Fatal("summary", rows[0], err)
	}
	// The next window starts empty, so an idle one writes nothing.
	c.flushSummaries(clock.Now().Add(30*time.Minute), false)
	if len(egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)) != 1 {
		t.Fatal("an empty window wrote a summary")
	}
	// Shutdown closes the session as shutdown and flushes what is left.
	c.audit.recordCall(installation, l.state.sessionID, clock.Now(), transport.EgressOutcomeOK, "https://a.example.com", 0, 7)
	c.shutdown()
	waitEgressEnded(t, session.runner, transport.EgressCloseShutdown)
	if closed := egressAuditRows(t, s, platformServiceEgressAuditClosed, installation); len(closed) != 1 || egressAuditField(t, closed[0], "reason") != "shutdown" {
		t.Fatal("shutdown close audit", closed)
	}
	if len(egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)) != 2 {
		t.Fatal("shutdown did not flush the summary")
	}
}

func TestPlatformServiceEgressCoordinatorOpensRenewsClosesSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorOpensRenewsCloses(t, false)
}
func TestPlatformServiceEgressCoordinatorOpensRenewsClosesPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorOpensRenewsCloses(t, true)
}
func TestPlatformServiceEgressCoordinatorRenewalKeepsInFlightCallsSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorRenewalKeepsInFlightCalls(t, false)
}
func TestPlatformServiceEgressCoordinatorRenewalKeepsInFlightCallsPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorRenewalKeepsInFlightCalls(t, true)
}
func TestPlatformServiceEgressCoordinatorReplacedSessionStillGuardedSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorReplacedSessionStillGuarded(t, false)
}
func TestPlatformServiceEgressCoordinatorReplacedSessionStillGuardedPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorReplacedSessionStillGuarded(t, true)
}
func TestPlatformServiceEgressCoordinatorBoundsDriftReloadsSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorBoundsDriftReloads(t, false)
}
func TestPlatformServiceEgressCoordinatorBoundsDriftReloadsPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorBoundsDriftReloads(t, true)
}
func TestPlatformServiceEgressCoordinatorWithheldProofWritesNoDeniedRowSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorWithheldProofWritesNoDeniedRow(t, false)
}
func TestPlatformServiceEgressCoordinatorWithheldProofWritesNoDeniedRowPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorWithheldProofWritesNoDeniedRow(t, true)
}
func TestPlatformServiceEgressDeniedCapSlidesAndSurvivesRestartSQLite(t *testing.T) {
	testPlatformServiceEgressDeniedCapSlidesAndSurvivesRestart(t, false)
}
func TestPlatformServiceEgressDeniedCapSlidesAndSurvivesRestartPostgres(t *testing.T) {
	testPlatformServiceEgressDeniedCapSlidesAndSurvivesRestart(t, true)
}
func TestPlatformServiceEgressCoordinatorLogsOpenFailuresOncePerIntervalSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorLogsOpenFailuresOncePerInterval(t, false)
}
func TestPlatformServiceEgressCoordinatorLogsOpenFailuresOncePerIntervalPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorLogsOpenFailuresOncePerInterval(t, true)
}
func TestPlatformServiceEgressCoordinatorSkipsRunnerWithoutProfileSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorSkipsRunnerWithoutProfile(t, false)
}
func TestPlatformServiceEgressCoordinatorSkipsRunnerWithoutProfilePostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorSkipsRunnerWithoutProfile(t, true)
}
func TestPlatformServiceEgressCoordinatorClosesOnRestorePauseSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorClosesOnRestorePause(t, false)
}
func TestPlatformServiceEgressCoordinatorClosesOnRestorePausePostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorClosesOnRestorePause(t, true)
}
func TestPlatformServiceEgressCoordinatorServesCallsSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorServesCalls(t, false)
}
func TestPlatformServiceEgressCoordinatorServesCallsPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorServesCalls(t, true)
}
func TestPlatformServiceEgressCoordinatorServesNoticesSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorServesNotices(t, false)
}
func TestPlatformServiceEgressCoordinatorServesNoticesPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorServesNotices(t, true)
}
func TestPlatformServiceEgressAuditOpenedClosedSummaryDeniedCapSQLite(t *testing.T) {
	testPlatformServiceEgressAuditOpenedClosedSummaryDeniedCap(t, false)
}
func TestPlatformServiceEgressAuditOpenedClosedSummaryDeniedCapPostgres(t *testing.T) {
	testPlatformServiceEgressAuditOpenedClosedSummaryDeniedCap(t, true)
}
