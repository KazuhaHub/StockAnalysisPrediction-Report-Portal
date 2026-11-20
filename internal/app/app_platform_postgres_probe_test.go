package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

const (
	platformPostgresUsername       = "status_probe"
	platformPostgresPassword       = "correct horse battery"
	platformPostgresCredentialJSON = `{"username":"` + platformPostgresUsername + `","password":"` + platformPostgresPassword + `"}`
)

func platformPostgresTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Reviewed Test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := appplatform.CanonicalPostgresCA(string(pemOf(der)))
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func pemOf(der []byte) []byte {
	return []byte("-----BEGIN CERTIFICATE-----\n" + wrap64(base64.StdEncoding.EncodeToString(der)) + "\n-----END CERTIFICATE-----\n")
}

func wrap64(s string) string {
	var out []string
	for len(s) > 64 {
		out = append(out, s[:64])
		s = s[64:]
	}
	return strings.Join(append(out, s), "\n")
}

// platformPostgresState is the runner as a review sees it: whether it serves the probe and its
// private-network allowlist.
type platformPostgresState struct {
	supported bool
	networks  []appplatform.PrivateNetwork
}

func (st *platformPostgresState) observe(_ context.Context, _ platformRuntimeEndpoint, need bool) (platformPostgresObservation, error) {
	if !st.supported {
		return platformPostgresObservation{}, errPlatformPostgresUnsupported
	}
	obs := platformPostgresObservation{Probe: transport.NewPostgresProbeProfile()}
	if need {
		targets, err := transport.NewNetworkTargets(st.networks)
		if err != nil {
			return obs, err
		}
		obs.Targets = targets
	}
	return obs, nil
}

func platformPostgresTarget(ca string) appplatform.PostgresTarget {
	return appplatform.PostgresTarget{ID: "orders", Host: "db.example.com", Port: 5432, Database: "orders", Network: appplatform.HTTPNetworkPublic, CAPEM: ca}
}

// platformPostgresRequestFor is the request that replaces whatever the row holds now.
func platformPostgresRequestFor(t *testing.T, s *Server, installation string, targets ...appplatform.PostgresTarget) platformPostgresRequest {
	t.Helper()
	revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
	stored := scalar[string](t, s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.postgres'`, installation)
	return platformPostgresRequest{PolicyRevision: revision, PreviousPolicySHA256: platformDigest([]byte(stored)), Targets: targets, Deadline: 10 * time.Second, Concurrency: 2}
}

func platformPostgresGrant(t *testing.T, s *Server, admin platformActorSession, installation string, targets ...appplatform.PostgresTarget) platformPostgresPolicyResult {
	t.Helper()
	plan, err := s.createPlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, platformPostgresRequestFor(t, s, installation, targets...))
	if err != nil {
		t.Fatal("postgres plan:", err)
	}
	if _, err := s.consumePlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, plan.ID, plan.SHA256); err != nil {
		t.Fatal("postgres consume:", err)
	}
	return plan
}

// platformPostgresFixture is the egress installation of platformServiceEgressFixture whose release
// declares network.postgres as optional. With bind it also has the target granted by a review, a
// credential holding the monitor role and an active service binding named orders-probe.
func platformPostgresFixture(t *testing.T, pg, bind bool) (*Server, platformActorSession, string, *platformPostgresState, appplatform.PostgresTarget) {
	t.Helper()
	platformFixtureManifestHook = func(m *appplatform.Manifest) {
		m.Capabilities.Required = []string{"background.run", "network.http", "notifications.publish", "secrets.use"}
		m.Capabilities.Optional = []string{"network.postgres"}
	}
	platformFixtureNetworkTarget = true
	t.Cleanup(func() { platformFixtureManifestHook, platformFixtureNetworkTarget = nil, false })
	s, admin, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability<>'network.postgres'`, lease.installationID)
	state := &platformPostgresState{supported: true}
	s.platformObservePostgres = state.observe
	target := platformPostgresTarget(platformPostgresTestCA(t))
	if bind {
		platformPostgresGrant(t, s, admin, lease.installationID, target)
		credential, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Orders monitor", value: []byte(platformPostgresCredentialJSON)})
		if err != nil {
			t.Fatal("credential:", err)
		}
		plan, err := s.createPlatformSecretBindingReviewScoped(admin, lease.installationID, "orders-probe", credential.ID, "pg_password", platformPrincipalsServiceOnly, "orders", nil)
		if err != nil {
			t.Fatal("binding review:", err)
		}
		if _, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256); err != nil {
			t.Fatal("binding commit:", err)
		}
	}
	return s, admin, lease.installationID, state, target
}

func testPlatformPostgresPolicyReviewPair(t *testing.T, pg bool) {
	s, admin, installation, state, target := platformPostgresFixture(t, pg, false)
	ctx := context.Background()
	plan := func(req platformPostgresRequest) (platformPostgresPolicyResult, error) {
		return s.createPlatformPostgresPolicyReview(ctx, admin, platformRuntimeEndpoint{}, installation, req)
	}
	req := platformPostgresRequestFor(t, s, installation, target)
	revisionBefore := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)

	// Refusals before anything is written.
	state.supported = false
	if _, err := plan(req); !errors.Is(err, errPlatformPostgresUnsupported) {
		t.Fatalf("a runner that does not serve the probe: %v", err)
	}
	state.supported = true
	stale := req
	stale.PreviousPolicySHA256 = strings.Repeat("a", 64)
	if _, err := plan(stale); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("a plan against a policy that is not the stored one: %v", err)
	}
	private := req
	private.Targets = []appplatform.PostgresTarget{{ID: "lan", Host: "10.20.0.7", Port: 5432, Database: "orders", Network: appplatform.HTTPNetworkPrivate}}
	if _, err := plan(private); !errors.Is(err, errPlatformPrivateTargetNotAllowed) {
		t.Fatalf("a private target with no runner allowlist: %v", err)
	}
	if _, err := plan(platformPostgresRequest{PolicyRevision: req.PolicyRevision, PreviousPolicySHA256: req.PreviousPolicySHA256}); err == nil {
		t.Fatal("a withdrawal of a grant that is not there was planned")
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE plan_json LIKE ?`, `{"format":"`+platformPostgresPolicyFormat+`"%`); n != 0 {
		t.Fatalf("%d plans written by refused requests", n)
	}

	// The pair: the plan changes nothing, the consume updates the row in place.
	granted := plan0(t, plan, req)
	if scalar[int](t, s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.postgres'`, installation) != 0 {
		t.Fatal("a plan changed the grant")
	}
	if len(granted.Plan.Targets) != 1 || granted.Plan.Targets[0].CAFingerprint == "" || granted.Plan.Targets[0].Label != "postgres://db.example.com:5432/orders" {
		t.Fatalf("plan view: %+v", granted.Plan.Targets)
	}
	change, err := s.consumePlatformPostgresPolicyReview(ctx, admin, platformRuntimeEndpoint{}, installation, granted.ID, granted.SHA256)
	if err != nil || !change.Granted {
		t.Fatal("consume:", err)
	}
	row := scalar[string](t, s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.postgres' AND granted=1`, installation)
	if row != granted.Plan.Policy || !appplatform.ValidPostgresPolicy(row) {
		t.Fatal("the row does not hold the reviewed policy", row)
	}
	if scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation) != revisionBefore {
		t.Fatal("the review moved the installation's policy revision")
	}
	if !platformPolicyMatches("network.postgres", 1, row, "{}") || platformPolicyMatches("network.postgres", 1, `{"format":"x"}`, "{}") || platformPolicyMatches("network.postgres", 0, row, "{}") {
		t.Fatal("the capability match exceptions are wrong")
	}
	audit := platformServiceCredentialAuditText(t, s)
	if !strings.Contains(audit, platformPostgresPolicyAudit) || strings.Contains(audit, "BEGIN CERTIFICATE") || strings.Contains(audit, platformPostgresPassword) {
		t.Fatal("audit rows of the grant", audit)
	}
	if _, err := s.consumePlatformPostgresPolicyReview(ctx, admin, platformRuntimeEndpoint{}, installation, granted.ID, granted.SHA256); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("a plan consumed twice: %v", err)
	}

	// A plan made against a row that then changed is refused (compare and swap).
	next := platformPostgresRequestFor(t, s, installation, platformPostgresTarget(""))
	second := plan0(t, plan, next)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='network.postgres'`, strings.Replace(row, `"concurrency":2`, `"concurrency":1`, 1), installation)
	if _, err := s.consumePlatformPostgresPolicyReview(ctx, admin, platformRuntimeEndpoint{}, installation, second.ID, second.SHA256); err == nil {
		t.Fatal("a plan was consumed against a row that had changed")
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='network.postgres'`, row, installation)

	// A private target is bound to the allowlist digest of its review.
	state.networks = []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/16", Ports: []int{5432}}}
	lan := platformPostgresRequestFor(t, s, installation, appplatform.PostgresTarget{ID: "lan", Host: "10.20.0.7", Port: 5432, Database: "orders", Network: appplatform.HTTPNetworkPrivate})
	lanPlan := plan0(t, plan, lan)
	if lanPlan.Plan.PrivateNetworksSHA256 == "" || !strings.Contains(lanPlan.Plan.Policy, `"private_networks_sha256":"`+lanPlan.Plan.PrivateNetworksSHA256+`"`) {
		t.Fatal("the policy is not bound to the allowlist digest", lanPlan.Plan.Policy)
	}
	state.networks = []appplatform.PrivateNetwork{{Prefix: "10.30.0.0/16", Ports: []int{5432}}}
	if _, err := s.consumePlatformPostgresPolicyReview(ctx, admin, platformRuntimeEndpoint{}, installation, lanPlan.ID, lanPlan.SHA256); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("a plan consumed against another allowlist: %v", err)
	}

	// Withdrawing resets the row exactly.
	withdraw := plan0(t, plan, platformPostgresRequestFor(t, s, installation))
	if _, err := s.consumePlatformPostgresPolicyReview(ctx, admin, platformRuntimeEndpoint{}, installation, withdraw.ID, withdraw.SHA256); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.postgres' AND policy_json='{}'`, installation) != 0 {
		t.Fatal("the withdrawn grant is not reset")
	}
}

func plan0(t *testing.T, plan func(platformPostgresRequest) (platformPostgresPolicyResult, error), req platformPostgresRequest) platformPostgresPolicyResult {
	t.Helper()
	result, err := plan(req)
	if err != nil {
		t.Fatal("plan:", err)
	}
	return result
}

func TestPlatformPostgresPolicyReviewPairSQLite(t *testing.T) {
	testPlatformPostgresPolicyReviewPair(t, false)
}
func TestPlatformPostgresPolicyReviewPairPostgres(t *testing.T) {
	testPlatformPostgresPolicyReviewPair(t, true)
}

func testPlatformPostgresBindingReviewAndUse(t *testing.T, pg bool) {
	s, admin, installation, _, target := platformPostgresFixture(t, pg, true)
	digest, err := appplatform.PostgresTargetSHA256(target)
	if err != nil {
		t.Fatal(err)
	}
	// What the review stored: always the target-scoped v3 policy, for the service only.
	policyJSON := scalar[string](t, s.st, `SELECT policy_json FROM app_platform_secret_bindings WHERE installation_id=? AND name='orders-probe'`, installation)
	if want := `{"format":"portal.secret-binding-policy/3-dev.1","operation":"pg_password","network_target_sha256":"` + digest + `","principals":["service"]}`; policyJSON != want {
		t.Fatalf("binding policy %s", policyJSON)
	}
	credential := scalar[string](t, s.st, `SELECT credential_id FROM app_platform_secret_bindings WHERE installation_id=? AND name='orders-probe'`, installation)
	// Reviews that must be refused: a viewer binding, an unknown target, a value that is no role.
	for name, review := range map[string]func() error{
		"viewer principal": func() error {
			_, err := s.createPlatformSecretBindingReviewScoped(admin, installation, "other-probe", credential, "pg_password", "", "orders", nil)
			return err
		},
		"both principals": func() error {
			_, err := s.createPlatformSecretBindingReviewScoped(admin, installation, "other-probe", credential, "pg_password", platformPrincipalsBoth, "orders", nil)
			return err
		},
		"unknown target": func() error {
			_, err := s.createPlatformSecretBindingReviewScoped(admin, installation, "other-probe", credential, "pg_password", platformPrincipalsServiceOnly, "nope", nil)
			return err
		},
		"a plain secret": func() error {
			plain, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Plain", value: []byte("not-a-role")})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.createPlatformSecretBindingReviewScoped(admin, installation, "other-probe", plain.ID, "pg_password", platformPrincipalsServiceOnly, "orders", nil)
			return err
		},
	} {
		if err := review(); err == nil {
			t.Errorf("%s: reviewed", name)
		}
	}
	use := func(principal string, dest platformBindingDestination) error {
		return s.withPlatformTestExec(t, func(e migExec) error {
			facts, err := loadPlatformSecretBindingFactsMode(e, installation, false, true)
			if err != nil {
				return err
			}
			_, _, err = loadPlatformSecretBindingUseFor(e, installation, "orders-probe", facts, principal, dest)
			return err
		})
	}
	good := platformBindingDestination{postgresTargetSHA: digest}
	if err := use(platformPrincipalService, good); err != nil {
		t.Fatal("the reviewed use was refused:", err)
	}
	for name, refused := range map[string]func() error{
		"the viewer":          func() error { return use(platformPrincipalViewer, good) },
		"an http destination": func() error { return use(platformPrincipalService, platformTargetDefaultURL) },
		"no destination":      func() error { return use(platformPrincipalService, platformBindingDestination{}) },
		"another target digest": func() error {
			return use(platformPrincipalService, platformBindingDestination{postgresTargetSHA: strings.Repeat("e", 64)})
		},
		"both kinds of destination": func() error {
			return use(platformPrincipalService, platformBindingDestination{postgresTargetSHA: digest, url: "https://api.example.com/v1/x", method: "GET"})
		},
	} {
		if refused() == nil {
			t.Errorf("%s: resolved", name)
		}
	}
	// Changing the target (its CA, here) leaves the binding on a digest the policy no longer has.
	other := target
	other.CAPEM = platformPostgresTestCA(t)
	platformPostgresGrant(t, s, admin, installation, other)
	if use(platformPrincipalService, good) == nil {
		t.Fatal("a binding followed a changed target")
	}
	// Withdrawing the grant, with the original target back, also ends it.
	platformPostgresGrant(t, s, admin, installation, target)
	if err := use(platformPrincipalService, good); err != nil {
		t.Fatal("the binding did not follow its own target back:", err)
	}
	withdraw, err := s.createPlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, platformPostgresRequestFor(t, s, installation))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, withdraw.ID, withdraw.SHA256); err != nil {
		t.Fatal(err)
	}
	if use(platformPrincipalService, good) == nil {
		t.Fatal("a binding resolved with no grant")
	}
}

func (s *Server) withPlatformTestExec(t *testing.T, f func(migExec) error) error {
	t.Helper()
	tx, err := s.st.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return f(migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: context.Background()}})
}

func TestPlatformPostgresBindingReviewAndUseSQLite(t *testing.T) {
	testPlatformPostgresBindingReviewAndUse(t, false)
}
func TestPlatformPostgresBindingReviewAndUsePostgres(t *testing.T) {
	testPlatformPostgresBindingReviewAndUse(t, true)
}

func testPlatformPostgresCoordinator(t *testing.T, pg bool) {
	s, admin, installation, _, target := platformPostgresFixture(t, pg, true)
	var logs syncLogBuffer
	previousOutput, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput); log.SetFlags(previousFlags) })

	// A runner that does not answer the probe query gets a session without the resource: it would
	// refuse the whole session otherwise.
	old := newFakeEgressRunner()
	oc, _ := newEgressTestCoordinator(t, s, old)
	oc.tick(context.Background())
	oldSession := nextEgressSession(t, old)
	if strings.Contains(strings.Join(oldSession.command.Resources, ","), "network.postgres") || old.queriedPostgres != 1 {
		t.Fatal("a session for a runner without the probe carries it", oldSession.command.Resources, old.queriedPostgres)
	}
	oc.shutdown()

	f := newFakeEgressRunner()
	f.postgres = true
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	if strings.Join(session.command.Resources, ",") != "network.http,network.postgres,notifications.publish,secrets.use" {
		t.Fatal("session resources", session.command.Resources)
	}
	start := func(targetID, credential string) *transport.EgressCall {
		payload, err := transport.EncodeEgressPostgresAdmit(transport.EgressPostgresAdmit{Target: targetID, Credential: credential})
		if err != nil {
			t.Fatal(err)
		}
		call, err := session.runner.NewCall()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(call.Finish)
		if err := call.Send(transport.EgressFramePostgresAdmit, payload); err != nil {
			t.Fatal(err)
		}
		return call
	}
	command := func(call *transport.EgressCall) control.PostgresProbeCommand {
		raw, signature, err := transport.DecodeEgressCommand(egressRecv(t, call, transport.EgressFrameCommand).Payload)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := control.VerifyPostgresProbeCommand(raw, signature, session.authority, time.Now().UnixMilli())
		if err != nil {
			t.Fatal("the runner would refuse the command:", err)
		}
		c := verified.Command()
		if c.TargetID != "orders" || c.CredentialName != "orders-probe" || string(c.Username) != platformPostgresUsername || string(c.Password) != platformPostgresPassword ||
			c.ActorScope != platformServiceEgressActorScope(session.command.SessionID) || c.PrivateNetworksSHA256 != "" {
			t.Fatal("the command does not carry the reviewed role for the service")
		}
		return c
	}
	ask := func(call *transport.EgressCall, c control.PostgresProbeCommand, seq byte) bool {
		var nonce [32]byte
		nonce[0], nonce[31] = 'p', seq
		if err := call.Send(transport.EgressFrameChallenge, transport.EncodeEgressChallenge(seq, nonce)); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		frame, err := call.Recv(ctx)
		if err != nil {
			return false
		}
		proof, sig, err := transport.DecodeEgressProof(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := control.VerifyPostgresProbeCheck(c, hex.EncodeToString(nonce[:]), proof, sig, session.authority, time.Now().UnixMilli()); err != nil {
			t.Fatal("proof does not verify:", err)
		}
		return true
	}
	outcome := func(call *transport.EgressCall, class byte, status uint16) {
		payload, _ := transport.EncodeEgressOutcome(transport.EgressOutcome{Class: class, Status: status, RetryAfter: -1})
		if err := call.Send(transport.EgressFrameOutcome, payload); err != nil {
			t.Fatal(err)
		}
	}

	// A good call: three checks answered, then the outcome.
	call := start("orders", "orders-probe")
	good := command(call)
	if !ask(call, good, 1) || !ask(call, good, 2) || !ask(call, good, 3) {
		t.Fatal("checks of a good probe call were not answered")
	}
	outcome(call, transport.EgressOutcomeOK, transport.EgressPostgresStatus("reachable"))

	// Revoking the binding between the checks: the next one is not answered.
	call = start("orders", "orders-probe")
	cmd := command(call)
	if !ask(call, cmd, 1) {
		t.Fatal("first check unanswered")
	}
	id := scalar[string](t, s.st, `SELECT id FROM app_platform_secret_bindings WHERE installation_id=? AND name='orders-probe'`, installation)
	if err := s.revokePlatformSecretBinding(admin, id, scalar[int64](t, s.st, `SELECT revision FROM app_platform_secret_bindings WHERE id=?`, id)); err != nil {
		t.Fatal(err)
	}
	if ask(call, cmd, 2) {
		t.Fatal("a check was answered after the binding was revoked")
	}
	outcome(call, transport.EgressOutcomeDenied, 0)

	// An unknown target, and the revoked binding, are refused before any decrypt.
	before := platformCredentialOpenCount.Load()
	for _, named := range [][2]string{{"nope", "orders-probe"}, {"orders", "orders-probe"}, {"orders", "unknown-binding"}} {
		refused := start(named[0], named[1])
		if code, err := transport.DecodeEgressRefusal(egressRecv(t, refused, transport.EgressFrameRefusal).Payload); err != nil || code != transport.EgressRefusalDenied {
			t.Fatalf("%v not denied: %d %v", named, code, err)
		}
	}
	if platformCredentialOpenCount.Load() != before {
		t.Fatal("a denied probe call reached the custody")
	}
	waitEgress(t, func() bool {
		c.audit.mu.Lock()
		defer c.audit.mu.Unlock()
		w := c.audit.windows[installation]
		return w != nil && w.postgres["orders"]["reachable"] == 1 && w.postgres["orders"]["denied"] == 1 && w.postgresUnresolved["denied"] == 3
	})
	clock.Advance(11 * time.Minute)
	c.flushSummaries(clock.Now(), false)
	rows := egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)
	if len(rows) != 1 {
		t.Fatal("summary rows", rows)
	}
	var summary struct {
		Postgres []struct {
			Target   string         `json:"target"`
			Outcomes map[string]int `json:"outcomes"`
		} `json:"postgres"`
		Unresolved map[string]int `json:"postgres_unresolved"`
	}
	if err := json.Unmarshal([]byte(rows[0]), &summary); err != nil || len(summary.Postgres) != 1 || summary.Postgres[0].Target != "orders" ||
		summary.Postgres[0].Outcomes["reachable"] != 1 || summary.Postgres[0].Outcomes["denied"] != 1 || summary.Unresolved["denied"] != 3 {
		t.Fatal("per-target summary", rows[0], err)
	}
	c.shutdown()
	secrets := []string{platformPostgresPassword, platformPostgresUsername, base64.StdEncoding.EncodeToString([]byte(platformPostgresPassword)), "BEGIN CERTIFICATE"}
	for _, secret := range secrets {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("Portal's log carries", secret)
		}
		if strings.Contains(platformServiceCredentialAuditText(t, s), secret) {
			t.Fatal("audit carries", secret)
		}
	}
	_ = target
}

func TestPlatformPostgresCoordinatorSQLite(t *testing.T)   { testPlatformPostgresCoordinator(t, false) }
func TestPlatformPostgresCoordinatorPostgres(t *testing.T) { testPlatformPostgresCoordinator(t, true) }

func testPlatformPostgresGrantWithdrawalClosesTheSession(t *testing.T, pg bool) {
	s, admin, installation, _, _ := platformPostgresFixture(t, pg, true)
	f := newFakeEgressRunner()
	f.postgres = true
	c, _ := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	withdraw, err := s.createPlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, platformPostgresRequestFor(t, s, installation))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, withdraw.ID, withdraw.SHA256); err != nil {
		t.Fatal(err)
	}
	c.checkDrift(context.Background(), time.Now())
	waitEgressEnded(t, session.runner, transport.EgressCloseDrift)
}

func TestPlatformPostgresGrantWithdrawalClosesTheSessionSQLite(t *testing.T) {
	testPlatformPostgresGrantWithdrawalClosesTheSession(t, false)
}
func TestPlatformPostgresGrantWithdrawalClosesTheSessionPostgres(t *testing.T) {
	testPlatformPostgresGrantWithdrawalClosesTheSession(t, true)
}

// The call keeps neither the username nor the password once it has been sent.
func TestPlatformServiceEgressPostgresCallKeepsNoRole(t *testing.T) {
	command := control.PostgresProbeCommand{Format: control.PostgresProbeCommandFormat, PolicyRaw: []byte("{}"), Username: []byte(platformPostgresUsername), Password: []byte(platformPostgresPassword)}
	raw, err := marshalPlatformServiceEgressPostgres(command)
	if err != nil {
		t.Fatal(err)
	}
	kept := newPlatformServiceEgressPostgresCall(command, raw, platformSecretBindingUse{}, strings.Repeat("a", 64))
	encoded, _ := json.Marshal(kept.command)
	for _, secret := range []string{platformPostgresPassword, platformPostgresUsername, base64.StdEncoding.EncodeToString([]byte(platformPostgresPassword))} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("the kept call holds", secret)
		}
	}
	if !kept.consistent || kept.sha != platformDigest(raw) {
		t.Fatal("the kept call lost its digest")
	}
}
