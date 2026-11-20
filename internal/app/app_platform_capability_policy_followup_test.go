package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformCapabilityTargetsFor(t *testing.T, networks []appplatform.PrivateNetwork) transport.NetworkTargets {
	t.Helper()
	targets, err := transport.NewNetworkTargets(networks)
	if err != nil {
		t.Fatal(err)
	}
	return targets
}

func mustParsePlatformPolicy(t *testing.T, raw string) appplatform.HTTPPolicyV2 {
	t.Helper()
	parsed, err := appplatform.ParseHTTPPolicyV2([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Policy
}

func platformCapabilityRowAccepted(t *testing.T, s *Server, installation string) bool {
	t.Helper()
	release := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, installation)
	review := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	policy := scalar[string](t, s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	return platformCapabilityOverlay(s.st.poolExec(), installation, release, epoch)(review, policy)
}

// platformCapabilityLoadGrant runs the foreground grant loader at the installation's current state.
func platformCapabilityLoadGrant(t *testing.T, s *Server, installation string) (platformNetworkGrant, error) {
	t.Helper()
	e := s.st.poolExec()
	release := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, installation)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(e, epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	return loadPlatformCapabilityNetworkGrant(e, installation, platformNetworkGrantTarget{
		release: release, runtime: runtime,
		generation: scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation),
		policy:     scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation),
	})
}

// advancePlatformGeneration is what enable, repair and restore do to the rows a
// capability review leaves behind: the generation moves, the grant rows stay.
func advancePlatformGeneration(t *testing.T, s *Server, installation string) {
	t.Helper()
	mustExec(t, s.st, `UPDATE app_platform_installations SET generation=generation+1 WHERE id=?`, installation)
	mustExec(t, s.st, `UPDATE app_platform_activations SET generation=generation+1 WHERE installation_id=? AND state='active'`, installation)
}

func testPlatformCapabilityPlanSurvivesLifecycle(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	platformCapabilityRunnerFor(t, s, installation, nil)
	plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platformCapabilityConsume(t, s, actor, plan); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"disable then enable", "repair"} {
		advancePlatformGeneration(t, s, installation)
		// The launch snapshot judges the row through this overlay; a backend launch also needs
		// the lifecycle operation's own history, which this fixture does not create.
		if !platformCapabilityRowAccepted(t, s, installation) {
			t.Fatal(step, "launch overlay refused the retained row")
		}
		if _, err := platformCapabilityLoadGrant(t, s, installation); err != nil {
			t.Fatal(step, "network grant:", err)
		}
	}
	// A new review after the lifecycle actions still works.
	more := platformTestTarget("more", "https://more.example.com", "public", []string{"/"}, []string{"GET"})
	again, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{more}, nil)
	if err != nil {
		t.Fatal("new review after enable:", err)
	}
	if _, err := platformCapabilityConsume(t, s, actor, again); err != nil {
		t.Fatal("consume after enable:", err)
	}
	if grant, err := platformCapabilityLoadGrant(t, s, installation); err != nil || !strings.Contains(grant.policy, "more.example.com") {
		t.Fatal("network grant after the second review", err)
	}
}

func TestPlatformCapabilityPlanSurvivesLifecycleSQLite(t *testing.T) {
	testPlatformCapabilityPlanSurvivesLifecycle(t, false)
}
func TestPlatformCapabilityPlanSurvivesLifecyclePostgres(t *testing.T) {
	testPlatformCapabilityPlanSurvivesLifecycle(t, true)
}

func testPlatformCapabilityPlanRefusedAfterUpdate(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	platformCapabilityRunnerFor(t, s, installation, nil)
	plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platformCapabilityConsume(t, s, actor, plan); err != nil {
		t.Fatal(err)
	}
	other := platformCapabilityOtherRelease(t, s, installation)
	mustExec(t, s.st, `UPDATE app_platform_activations SET release_sha256=? WHERE installation_id=? AND state='active'`, other, installation)
	advancePlatformGeneration(t, s, installation)
	if platformCapabilityRowAccepted(t, s, installation) {
		t.Fatal("launch overlay accepted the plan of another release")
	}
	if _, err := platformCapabilityLoadGrant(t, s, installation); err == nil {
		t.Fatal("network grant accepted the plan of another release")
	}
}

func TestPlatformCapabilityPlanRefusedAfterUpdateSQLite(t *testing.T) {
	testPlatformCapabilityPlanRefusedAfterUpdate(t, false)
}
func TestPlatformCapabilityPlanRefusedAfterUpdatePostgres(t *testing.T) {
	testPlatformCapabilityPlanRefusedAfterUpdate(t, true)
}

// A review keeps a setup binding and a kept target-scoped binding usable: the
// phase stays ready, the revision does not move and the audit row names the change.
func testPlatformCapabilityKeepsSetupBindings(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	appID := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation)
	platformSetupRequirements[appID] = []string{platformDigestFeedBinding}
	t.Cleanup(func() { delete(platformSetupRequirements, appID) })
	platformCapabilityRunnerFor(t, s, installation, nil)
	platformCapabilitySeedBinding(t, s, actor, installation, platformFixtureID(8201), platformDigestFeedBinding, `{"format":"portal.secret-binding-policy/1-dev.1","operation":"raw_read","network_policy_sha256":""}`)
	mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET operation='raw_read' WHERE id=?`, platformFixtureID(8201))
	platformCapabilitySeedBinding(t, s, actor, installation, platformFixtureID(8202), "kept", platformCapabilityV3Policy(t, platformV2DefaultTarget()))
	revision := scalar[int64](t, s.st, `SELECT revision FROM app_platform_installations WHERE id=?`, installation)
	plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platformCapabilityConsume(t, s, actor, plan); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, installation) != "ready" ||
		scalar[int64](t, s.st, `SELECT revision FROM app_platform_installations WHERE id=?`, installation) != revision {
		t.Fatal("a review moved the phase or the revision")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_secret_bindings WHERE installation_id=? AND state='active' AND policy_revision=2`, installation) != 2 {
		t.Fatal("kept bindings not re-stamped")
	}
	missing, err := platformMissingSetupBindings(s.st.poolExec(), installation, appID, 2)
	if err != nil || len(missing) != 0 {
		t.Fatal("setup binding counted as missing", missing, err)
	}
	if err := platformCapabilityLaunch(t, s, actor, installation); err != nil {
		t.Fatal("launch:", err)
	}
	detail := scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.capability_policy_changed' ORDER BY id DESC LIMIT 1`)
	for _, want := range []string{plan.ID, plan.Plan.PreviousPolicySHA256, platformDigest([]byte(plan.Plan.Runtime.NetworkPolicy)), `"id":"extra"`, "https://other.example.com", plan.Plan.PrivateNetworksSHA256, `"paused_bindings":[]`} {
		if !strings.Contains(detail, want) {
			t.Fatal("audit detail lacks", want, detail)
		}
	}
}

func TestPlatformCapabilityKeepsSetupBindingsSQLite(t *testing.T) {
	testPlatformCapabilityKeepsSetupBindings(t, false)
}
func TestPlatformCapabilityKeepsSetupBindingsPostgres(t *testing.T) {
	testPlatformCapabilityKeepsSetupBindings(t, true)
}

func testPlatformCapabilityPrivateRetained(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	allow := []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{8443}}}
	private := platformTestTarget("lan", "https://10.20.0.5:8443", "private", []string{"/"}, []string{"GET"})
	policy := platformV2PolicyFixture(t, platformV2DefaultTarget(), private)
	seeded := func() {
		platformCapabilitySeedPlan(t, s, installation, platformCapabilitySeed{policy: policy, mutate: func(p *platformCapabilityPolicyPlan) {
			p.PrivateNetworks = allow
			sum, err := appplatform.PrivateNetworksSHA256(allow)
			if err != nil {
				t.Fatal(err)
			}
			p.PrivateNetworksSHA256 = sum
			p.TargetsAdded = []platformCapabilityTarget{}
		}})
	}
	seeded()
	// The allowlist no longer admits the retained private target: an unrelated review is refused.
	runner := platformCapabilityRunnerFor(t, s, installation, []appplatform.PrivateNetwork{{Prefix: "10.30.0.0/24", Ports: []int{8443}}})
	if _, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil); !errors.Is(err, errPlatformPrivateTargetNotAllowed) {
		t.Fatal("retained private target not checked at create", err)
	}
	// Admitted at create, then the allowlist drifts away before consume: refused at consume
	// (the allowlist digest check comes first, so a plan only reaches the retained check when
	// the digest matches; the check still holds on its own).
	runner.set(func(o *platformCapabilityObservation) { o.Targets = platformCapabilityTargetsFor(t, allow) })
	plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := platformCheckCapabilityPrivate(nil, mustParsePlatformPolicy(t, plan.Plan.Runtime.NetworkPolicy)); !errors.Is(err, errPlatformPrivateTargetNotAllowed) {
		t.Fatal("consume-time check", err)
	}
}

func TestPlatformCapabilityPrivateRetainedSQLite(t *testing.T) {
	testPlatformCapabilityPrivateRetained(t, false)
}
func TestPlatformCapabilityPrivateRetainedPostgres(t *testing.T) {
	testPlatformCapabilityPrivateRetained(t, true)
}

// A review that only re-binds the policy to a changed runner allowlist, and a
// diff that shows the bounds and the narrowing a change makes.
func testPlatformCapabilityRebindAndDiff(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	oldAllow := []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{8443}}}
	newAllow := []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/16", Ports: []int{8443}}}
	private := platformTestTarget("lan", "https://10.20.0.5:8443", "private", []string{"/"}, []string{"GET"})
	platformCapabilitySeedPlan(t, s, installation, platformCapabilitySeed{policy: platformV2PolicyFixture(t, platformV2DefaultTarget(), private), mutate: func(p *platformCapabilityPolicyPlan) {
		p.PrivateNetworks = oldAllow
		sum, err := appplatform.PrivateNetworksSHA256(oldAllow)
		if err != nil {
			t.Fatal(err)
		}
		p.PrivateNetworksSHA256 = sum
		p.TargetsAdded = []platformCapabilityTarget{}
	}})
	runner := platformCapabilityRunnerFor(t, s, installation, oldAllow)
	// Same allowlist: nothing to change, refused.
	if _, err := platformCapabilityCreate(t, s, actor, installation, nil, nil); !errors.Is(err, errPlatformManagementInput) {
		t.Fatal("a no-op review was accepted", err)
	}
	runner.set(func(o *platformCapabilityObservation) { o.Targets = platformCapabilityTargetsFor(t, newAllow) })
	plan, err := platformCapabilityCreate(t, s, actor, installation, nil, nil)
	if err != nil {
		t.Fatal("rebind refused:", err)
	}
	if len(plan.Plan.TargetsAdded) != 0 || len(plan.Plan.TargetsRemoved) != 0 || plan.Plan.PrivateNetworksSHA256 == plan.PreviousNetworksSHA256 {
		t.Fatal("rebind plan shape", plan.Plan.PrivateNetworksSHA256, plan.PreviousNetworksSHA256)
	}
	if _, err := platformCapabilityConsume(t, s, actor, plan); err != nil {
		t.Fatal("rebind consume:", err)
	}
	if err := platformCapabilityPrepare(t, s, actor, installation, "https://10.20.0.5:8443/x"); err != nil {
		t.Fatal("private request after rebind:", err)
	}
	// The diff shows the shared bounds and the per-target narrowing.
	narrow := platformV2ExtraTarget()
	req := platformCapabilityRequestFor(t, s, installation, []appplatform.HTTPTarget{narrow}, nil)
	req.Bounds = platformCapabilityBounds{RequestBytes: 512, Deadline: 500 * time.Millisecond}
	result, err := s.createPlatformCapabilityPolicyReview(t.Context(), actor, platformRuntimeEndpoint{}, installation, req)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := platformCapabilityDiff(result)
	if err != nil {
		t.Fatal(err)
	}
	bounds := diff["bounds"].(map[string]any)
	if bounds["old"].(platformCapabilityLimits).RequestBytes != 1024 || bounds["new"].(platformCapabilityLimits).RequestBytes != 512 ||
		bounds["new"].(platformCapabilityLimits).DeadlineMS != 500 {
		t.Fatal("bounds", bounds)
	}
	narrowing := diff["narrowing"].([]platformCapabilityNarrowing)
	if len(narrowing) != 2 || narrowing[0].New.RequestBytes != 512 {
		t.Fatal("narrowing", narrowing)
	}
	if diff["allowlist"].(map[string]any)["new_sha256"] != plan.Plan.PrivateNetworksSHA256 {
		t.Fatal("allowlist", diff["allowlist"])
	}
}

func TestPlatformCapabilityRebindAndDiffSQLite(t *testing.T) {
	testPlatformCapabilityRebindAndDiff(t, false)
}
func TestPlatformCapabilityRebindAndDiffPostgres(t *testing.T) {
	testPlatformCapabilityRebindAndDiff(t, true)
}
