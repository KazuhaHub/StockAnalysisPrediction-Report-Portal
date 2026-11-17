package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformServiceEgressFixture is an installation that declares background.run,
// network.http (with a reviewed target) and notifications.publish as required.
// writable selects the generation-1 writable install, which has a network grant;
// otherwise it is the generation-1 managed install, which does not (doc 174 F13).
func platformServiceEgressFixture(t *testing.T, pg, writable bool) (*Server, string) {
	t.Helper()
	platformFixtureManifestHook = func(m *appplatform.Manifest) {
		if writable {
			m.Capabilities.Required = []string{"background.run", "network.http", "notifications.publish"}
			m.Capabilities.Optional = nil
			return
		}
		// A managed install cannot carry a network target, so network.http stays
		// optional and ungranted.
		m.Capabilities.Required = []string{"background.run", "notifications.publish"}
		m.Capabilities.Optional = []string{"network.http"}
	}
	platformFixtureNetworkTarget = writable
	t.Cleanup(func() { platformFixtureManifestHook, platformFixtureNetworkTarget = nil, false })
	var s *Server
	var lease platformOperationLease
	if writable {
		s, _, lease = platformWritableAccessFixture(t, pg)
	} else {
		s, _, lease = platformManagedAccessFixture(t, pg)
	}
	// The fixtures stop short of activation, which is what turns the reviewed
	// selection into grants: do that part by hand, exactly as the selection says.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability<>'network.http'`, lease.installationID)
	if writable {
		mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, lease.installationID)
	}
	return s, lease.installationID
}

func platformServiceEgressState(t *testing.T, s *Server, installation string, resources ...string) platformServiceEgressSessionState {
	t.Helper()
	session := platformFixtureID(7701)
	identity, _, err := s.issuePlatformServiceIdentity(context.Background(), installation, session, resources, 5*time.Minute)
	if err != nil {
		t.Fatal("issue assertion:", err)
	}
	a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{identity: identity, resources: resources})
	if err != nil {
		t.Fatal("load current authority:", err)
	}
	defer a.Close()
	return platformServiceEgressSessionState{sessionID: session, identity: identity, resources: a.resources, pin: a.pin(),
		expiresMS: time.Now().Add(4 * time.Minute).UnixMilli()}
}

func platformServiceEgressAdmit() transport.EgressAdmit {
	return transport.EgressAdmit{Method: "GET", URL: "https://api.example.com/v1/x", Accept: "application/json",
		BodySHA256: platformDigest(nil)}
}

// platformServiceEgressSignFor signs one admitted call for the session state.
func platformServiceEgressSignFor(t *testing.T, s *Server, state platformServiceEgressSessionState) (control.NetworkHTTPRequestCommand, []byte, []byte) {
	t.Helper()
	a, err := s.loadServiceEgressForSession(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	command, raw, sig, err := a.signServiceEgressCommand(state.sessionID, platformServiceEgressAdmit(), time.Now().UnixMilli(), state.expiresMS)
	if err != nil {
		t.Fatal("sign call:", err)
	}
	return command, raw, sig
}

func testPlatformServiceEgressRequiresBackgroundAndNetworkGrant(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	ctx := context.Background()
	both := []string{"network.http", "notifications.publish"}
	a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{resources: both, history: true})
	if err != nil {
		t.Fatal("baseline authority:", err)
	}
	if !a.hasNetwork() || a.grant.review == "" || a.concurrency != 1 || a.history == nil || len(a.history.raw) == 0 {
		t.Fatal("incomplete authority", a.resources, a.concurrency)
	}
	a.Close()
	// Discovery plans the same set from the grants.
	d, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{})
	if err != nil || strings.Join(d.resources, ",") != strings.Join(both, ",") {
		t.Fatal("discovered resources", d.resources, err)
	}
	d.Close()
	// Without network.http granted the session is notification-only, and an
	// explicit request for the network is refused.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, installation)
	if d, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{}); err != nil || strings.Join(d.resources, ",") != "notifications.publish" {
		t.Fatal("notification-only discovery", d.resources, err)
	} else {
		d.Close()
	}
	if a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{resources: both}); err == nil {
		a.Close()
		t.Fatal("network resource accepted without a network grant")
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)
	// No background.run, no egress at all.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='background.run'`, installation)
	for _, o := range []platformServiceEgressLoad{{}, {resources: both}, {resources: []string{"notifications.publish"}}} {
		if a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, o); err == nil {
			a.Close()
			t.Fatal("egress authority without background.run", o.resources)
		}
	}
}

func testPlatformServiceEgressRefusesAfterRevokeDisableUninstallEpochRestore(t *testing.T, pg bool) {
	for name, change := range map[string]string{
		"revoke network":    `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`,
		"revoke notify":     `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='notifications.publish'`,
		"revoke background": `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='background.run'`,
		"disable":           `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`,
		"uninstall":         `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`,
		"epoch":             `UPDATE app_platform_installations SET controller_epoch='` + platformFixtureID(7790) + `' WHERE id=?`,
		"restore pause":     `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused' AND ''<>?`,
	} {
		t.Run(name, func(t *testing.T) {
			s, installation := platformServiceEgressFixture(t, pg, true)
			state := platformServiceEgressState(t, s, installation, "network.http", "notifications.publish")
			command, raw, _ := platformServiceEgressSignFor(t, s, state)
			if _, _, err := s.approveServiceEgressCheck(context.Background(), state, command, raw, strings.Repeat("c", 64), platformSecretBindingUse{}); err != nil {
				t.Fatal("check before the change:", err)
			}
			mustExec(t, s.st, change, installation)
			if proof, sig, err := s.approveServiceEgressCheck(context.Background(), state, command, raw, strings.Repeat("d", 64), platformSecretBindingUse{}); err == nil || len(proof) != 0 || len(sig) != 0 {
				t.Fatal("a challenge was answered after the change")
			}
			if a, err := s.loadServiceEgressForSession(context.Background(), state); err == nil {
				a.Close()
				t.Fatal("session authority survived the change")
			}
		})
	}
}

func testPlatformServiceEgressFenceDriftRefuses(t *testing.T, pg bool) {
	for name, change := range map[string]string{
		"fence":      `UPDATE app_platform_installations SET control_fence=control_fence+1 WHERE id=?`,
		"operation":  `UPDATE app_platform_operations SET stage='start',completed_ms=NULL WHERE installation_id=?`,
		"activation": `UPDATE app_platform_activations SET id='` + platformFixtureID(7791) + `' WHERE installation_id=?`,
	} {
		t.Run(name, func(t *testing.T) {
			s, installation := platformServiceEgressFixture(t, pg, true)
			state := platformServiceEgressState(t, s, installation, "network.http", "notifications.publish")
			mustExec(t, s.st, change, installation)
			if a, err := s.loadServiceEgressForSession(context.Background(), state); err == nil {
				a.Close()
				t.Fatal("session authority accepted drift")
			}
		})
	}
}

func testPlatformServiceEgressActorScopeIsServiceNotViewer(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	state := platformServiceEgressState(t, s, installation, "network.http", "notifications.publish")
	command, raw, sig := platformServiceEgressSignFor(t, s, state)
	if command.ActorScope != platformDigest([]byte("portal-network-service/v1:"+state.sessionID)) {
		t.Fatal("actor scope is not the service session")
	}
	if command.CredentialName != "" || command.CredentialHeader != "" || len(command.CredentialValue) != 0 {
		t.Fatal("background command carries a credential")
	}
	a, err := s.loadServiceEgressForSession(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := control.VerifyNetworkHTTPRequest(raw, sig, a.authority, time.Now().UnixMilli()); err != nil {
		t.Fatal("runner would refuse the command:", err)
	}
	expires, err := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if err != nil || expires > state.expiresMS {
		t.Fatal("command outlives the session", err)
	}
	// A fresh Portal-minted request id per call.
	other, _, _, err := a.signServiceEgressCommand(state.sessionID, platformServiceEgressAdmit(), time.Now().UnixMilli(), state.expiresMS)
	if err != nil || other.RequestID == command.RequestID {
		t.Fatal("request id reused", err)
	}
	// What the reviewed policy does not allow is not signed.
	for name, admit := range map[string]transport.EgressAdmit{
		"other origin": {Method: "GET", URL: "https://evil.example.com/v1", BodySHA256: platformDigest(nil)},
		"other path":   {Method: "GET", URL: "https://api.example.com/admin", BodySHA256: platformDigest(nil)},
		"method":       {Method: "POST", URL: "https://api.example.com/v1/x", BodySHA256: platformDigest(nil)},
		"body":         {Method: "GET", URL: "https://api.example.com/v1/x", BodyBytes: 4096, BodySHA256: platformDigest(nil)},
	} {
		if _, _, _, err := a.signServiceEgressCommand(state.sessionID, admit, time.Now().UnixMilli(), state.expiresMS); !errors.Is(err, errPlatformServiceEgressDenied) {
			t.Fatal(name, "signed:", err)
		}
	}
	// A check for bytes that are not the command it names, or for a viewer-scoped
	// command, is not answered.
	forged := command
	forged.RequestID = platformFixtureID(7792)
	forgedRaw, _ := marshalPlatformServiceEgressCommand(forged)
	if _, _, err := s.approveServiceEgressCheck(context.Background(), state, forged, raw, strings.Repeat("c", 64), platformSecretBindingUse{}); err == nil {
		t.Fatal("check answered for a different command than the bytes it names")
	}
	viewer := command
	viewer.ActorScope = platformDigest([]byte("portal-network-viewer/v1:{}"))
	viewerRaw, _ := marshalPlatformServiceEgressCommand(viewer)
	if _, _, err := s.approveServiceEgressCheck(context.Background(), state, viewer, viewerRaw, strings.Repeat("c", 64), platformSecretBindingUse{}); err == nil {
		t.Fatal("check answered for a viewer-scoped command")
	}
	_ = forgedRaw
}

func testPlatformServiceEgressRefusesAdmitWhenAssertionNearExpiry(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, true)
	state := platformServiceEgressState(t, s, installation, "network.http", "notifications.publish")
	a, err := s.loadServiceEgressForSession(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Now().UnixMilli()
	if _, _, _, err := a.signServiceEgressCommand(state.sessionID, platformServiceEgressAdmit(), now, now+30000); !errors.Is(err, errPlatformServiceEgressExpiring) {
		t.Fatal("admit signed with 30 s left:", err)
	}
	if _, _, _, err := a.signServiceEgressCommand(state.sessionID, platformServiceEgressAdmit(), now, now+31000); err != nil {
		t.Fatal("admit refused with 31 s left:", err)
	}
}

func testPlatformServiceEgressManagedInitialInstallHasNoNetwork(t *testing.T, pg bool) {
	s, installation := platformServiceEgressFixture(t, pg, false)
	ctx := context.Background()
	d, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{history: true})
	if err != nil {
		t.Fatal("notification-only session not planned:", err)
	}
	defer d.Close()
	if d.hasNetwork() || strings.Join(d.resources, ",") != "notifications.publish" || d.history == nil {
		t.Fatal("managed initial install planned with network", d.resources)
	}
	if a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{resources: []string{"network.http", "notifications.publish"}}); err == nil {
		a.Close()
		t.Fatal("managed initial install accepted network.http")
	}
	if _, _, _, err := d.signServiceEgressCommand(platformFixtureID(7701), platformServiceEgressAdmit(), time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli()); err == nil {
		t.Fatal("notification-only authority signed an HTTP command")
	}
}

// Every admit, check and drift reload reads the authority in one transaction.
// Two of them must not serialise: one that is held open (before its commit)
// does not stop another from loading, so a slow load never queues the rest.
func TestPlatformServiceEgressConcurrentLoadsDoNotSerializePostgres(t *testing.T) {
	s, installation := platformServiceEgressFixture(t, true, true)
	state := platformServiceEgressState(t, s, installation, "network.http", "notifications.publish")
	held, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{
			identity: state.identity, resources: state.resources,
			beforeCommit: func() { close(held); <-release },
		})
		a.Close()
		first <- err
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the first load did not reach its commit")
	}
	second := make(chan error, 1)
	go func() {
		a, err := s.loadServiceEgressForSession(context.Background(), state)
		a.Close()
		second <- err
	}()
	select {
	case err := <-second:
		if err != nil {
			t.Fatal("second load:", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a second authority load waited for the first one's transaction")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal("first load:", err)
	}
}

// The notification-only fallback is for the one documented case, a generation-1
// managed install that cannot carry a network target (doc 174 F13). It must not
// hide a real failure to resolve the network grant of any other install.
func testPlatformServiceEgressNotificationOnlyFallbackIsScoped(t *testing.T, pg bool) {
	ctx := context.Background()
	// The documented case: a generation-1 managed install has no network grant
	// (its network.http is ungranted), so discovery plans notifications only.
	s, installation := platformServiceEgressFixture(t, pg, false)
	d, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{})
	if err != nil || d.hasNetwork() || strings.Join(d.resources, ",") != "notifications.publish" {
		t.Fatal("managed generation 1 was not planned notification-only", err)
	}
	d.Close()
	// It cannot be made to hold network egress by granting the capability.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)
	if a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{}); err == nil {
		a.Close()
		t.Fatal("a managed install was planned with a network grant it cannot have", a.resources)
	}
	// A writable install whose network grant stopped resolving fails the session.
	s, installation = platformServiceEgressFixture(t, pg, true)
	a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{})
	if err != nil || !a.hasNetwork() {
		t.Fatal("baseline", err)
	}
	a.Close()
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json='{"broken":true}' WHERE installation_id=? AND capability='network.http'`, installation)
	if a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{}); err == nil {
		a.Close()
		t.Fatal("a network grant failure was hidden behind a notification-only session", a.resources)
	}
}

func TestPlatformServiceEgressNotificationOnlyFallbackIsScopedSQLite(t *testing.T) {
	testPlatformServiceEgressNotificationOnlyFallbackIsScoped(t, false)
}
func TestPlatformServiceEgressNotificationOnlyFallbackIsScopedPostgres(t *testing.T) {
	testPlatformServiceEgressNotificationOnlyFallbackIsScoped(t, true)
}
func TestPlatformServiceEgressRequiresBackgroundAndNetworkGrantSQLite(t *testing.T) {
	testPlatformServiceEgressRequiresBackgroundAndNetworkGrant(t, false)
}
func TestPlatformServiceEgressRequiresBackgroundAndNetworkGrantPostgres(t *testing.T) {
	testPlatformServiceEgressRequiresBackgroundAndNetworkGrant(t, true)
}
func TestPlatformServiceEgressRefusesAfterRevokeDisableUninstallEpochRestoreSQLite(t *testing.T) {
	testPlatformServiceEgressRefusesAfterRevokeDisableUninstallEpochRestore(t, false)
}
func TestPlatformServiceEgressRefusesAfterRevokeDisableUninstallEpochRestorePostgres(t *testing.T) {
	testPlatformServiceEgressRefusesAfterRevokeDisableUninstallEpochRestore(t, true)
}
func TestPlatformServiceEgressFenceDriftRefusesSQLite(t *testing.T) {
	testPlatformServiceEgressFenceDriftRefuses(t, false)
}
func TestPlatformServiceEgressFenceDriftRefusesPostgres(t *testing.T) {
	testPlatformServiceEgressFenceDriftRefuses(t, true)
}
func TestPlatformServiceEgressActorScopeIsServiceNotViewerSQLite(t *testing.T) {
	testPlatformServiceEgressActorScopeIsServiceNotViewer(t, false)
}
func TestPlatformServiceEgressActorScopeIsServiceNotViewerPostgres(t *testing.T) {
	testPlatformServiceEgressActorScopeIsServiceNotViewer(t, true)
}
func TestPlatformServiceEgressRefusesAdmitWhenAssertionNearExpirySQLite(t *testing.T) {
	testPlatformServiceEgressRefusesAdmitWhenAssertionNearExpiry(t, false)
}
func TestPlatformServiceEgressRefusesAdmitWhenAssertionNearExpiryPostgres(t *testing.T) {
	testPlatformServiceEgressRefusesAdmitWhenAssertionNearExpiry(t, true)
}
func TestPlatformServiceEgressManagedInitialInstallHasNoNetworkSQLite(t *testing.T) {
	testPlatformServiceEgressManagedInitialInstallHasNoNetwork(t, false)
}
func TestPlatformServiceEgressManagedInitialInstallHasNoNetworkPostgres(t *testing.T) {
	testPlatformServiceEgressManagedInitialInstallHasNoNetwork(t, true)
}
