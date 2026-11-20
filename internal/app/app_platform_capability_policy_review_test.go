package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformCapabilityRunner stands a runner behind the review: obs is what the
// seam answers, and err replaces the answer when set. Tests change both between
// create and consume to model drift.
type platformCapabilityRunner struct {
	mu  sync.Mutex
	obs platformCapabilityObservation
	err error
}

func (r *platformCapabilityRunner) set(f func(*platformCapabilityObservation)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(&r.obs)
}

func platformCapabilityRunnerFor(t *testing.T, s *Server, installation string, networks []appplatform.PrivateNetwork) *platformCapabilityRunner {
	t.Helper()
	plan, install, _, _ := platformCapabilityFixtureFacts(t, s, installation)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(migExec{s: s.st, ex: s.st.db}, plan.ControllerEpoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := transport.NewNetworkTargets(networks)
	if err != nil {
		t.Fatal(err)
	}
	r := &platformCapabilityRunner{obs: platformCapabilityObservation{Runtime: runtime, Managed: install.Runtime.Profile, Network: install.Runtime.NetworkProfile, Targets: targets}}
	s.platformObserveCapability = func(context.Context, platformActorSession, platformRuntimeEndpoint) (platformCapabilityObservation, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.err != nil {
			return platformCapabilityObservation{}, r.err
		}
		return r.obs, nil
	}
	return r
}

func platformCapabilityRequestFor(t *testing.T, s *Server, installation string, add []appplatform.HTTPTarget, remove []string) platformCapabilityRequest {
	t.Helper()
	return platformCapabilityRequest{
		PolicyRevision:       scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation),
		PreviousPolicySHA256: platformDigest([]byte(scalar[string](t, s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation))),
		Add:                  add, Remove: remove,
	}
}

func platformCapabilityCreate(t *testing.T, s *Server, actor platformActorSession, installation string, add []appplatform.HTTPTarget, remove []string) (platformCapabilityPolicyResult, error) {
	t.Helper()
	return s.createPlatformCapabilityPolicyReview(context.Background(), actor, platformRuntimeEndpoint{}, installation, platformCapabilityRequestFor(t, s, installation, add, remove))
}

func platformCapabilityTableDump(t *testing.T, s *Server) []byte { return stripHeader(dumpOf(t, s.st)) }

// Every refusal leaves every table as it was.
func testPlatformCapabilityCreateRefusals(t *testing.T, pg bool) {
	extra := platformV2ExtraTarget()
	private := platformTestTarget("lan", "https://10.20.0.5:8443", "private", []string{"/"}, []string{"GET"})
	lanAllow := []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{8443}, PlainHTTP: false}}
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Server, actor platformActorSession, installation string, r *platformCapabilityRunner)
		add   []appplatform.HTTPTarget
		rm    []string
		want  error
	}{
		{name: "non-admin", setup: func(t *testing.T, s *Server, actor platformActorSession, i string, r *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
		}, add: []appplatform.HTTPTarget{extra}, want: errAppPlatformActorUnavailable},
		{name: "not ready", setup: func(t *testing.T, s *Server, _ platformActorSession, i string, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_installations SET phase='review_required' WHERE id=?`, i)
		}, add: []appplatform.HTTPTarget{extra}, want: errPlatformCapabilityPolicyBusy},
		{name: "unfinished operation", setup: func(t *testing.T, s *Server, _ platformActorSession, i string, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_operations SET stage='start',completed_ms=NULL WHERE installation_id=?`, i)
		}, add: []appplatform.HTTPTarget{extra}, want: errPlatformCapabilityPolicyBusy},
		{name: "grant withdrawn", setup: func(t *testing.T, s *Server, _ platformActorSession, i string, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, i)
		}, add: []appplatform.HTTPTarget{extra}, want: errPlatformCapabilityPolicyBusy},
		{name: "old runner", setup: func(t *testing.T, _ *Server, _ platformActorSession, _ string, r *platformCapabilityRunner) {
			r.err = transport.ErrNetworkTargetsUnsupported
		}, add: []appplatform.HTTPTarget{extra}, want: transport.ErrNetworkTargetsUnsupported},
		{name: "runtime identity", setup: func(t *testing.T, _ *Server, _ platformActorSession, _ string, r *platformCapabilityRunner) {
			r.set(func(o *platformCapabilityObservation) { o.Runtime.fingerprint = platformDigest([]byte("elsewhere")) })
		}, add: []appplatform.HTTPTarget{extra}, want: errAppPlatformRuntimeConflict},
		{name: "no-op", want: errPlatformManagementInput},
		{name: "remove the last target", rm: []string{"default"}, want: errPlatformManagementInput},
		{name: "remove unknown id", add: []appplatform.HTTPTarget{extra}, rm: []string{"nothing"}, want: errPlatformManagementInput},
		{name: "rename by reusing an id", add: []appplatform.HTTPTarget{platformTestTarget("default", "https://other.example.com", "public", []string{"/v2"}, []string{"GET"})}, want: errPlatformManagementInput},
		{name: "rename by moving an origin", add: []appplatform.HTTPTarget{extra, platformTestTarget("renamed", "https://api.example.com", "public", []string{"/v1"}, []string{"GET"})}, rm: []string{"default"}, want: errPlatformManagementInput},
		{name: "duplicate origin", add: []appplatform.HTTPTarget{platformTestTarget("again", "https://api.example.com", "public", []string{"/v9"}, []string{"GET"})}, want: errPlatformManagementInput},
		{name: "private target without allowlist", add: []appplatform.HTTPTarget{private}, want: errPlatformPrivateTargetNotAllowed},
		{name: "private literal outside the allowlist", setup: func(t *testing.T, _ *Server, _ platformActorSession, _ string, r *platformCapabilityRunner) {
			targets, err := transport.NewNetworkTargets([]appplatform.PrivateNetwork{{Prefix: "10.30.0.0/24", Ports: []int{8443}}})
			if err != nil {
				t.Fatal(err)
			}
			r.set(func(o *platformCapabilityObservation) { o.Targets = targets })
		}, add: []appplatform.HTTPTarget{private}, want: errPlatformPrivateTargetNotAllowed},
		{name: "private literal on a port the allowlist lacks", setup: func(t *testing.T, _ *Server, _ platformActorSession, _ string, r *platformCapabilityRunner) {
			targets, err := transport.NewNetworkTargets([]appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{443}}})
			if err != nil {
				t.Fatal(err)
			}
			r.set(func(o *platformCapabilityObservation) { o.Targets = targets })
		}, add: []appplatform.HTTPTarget{private}, want: errPlatformPrivateTargetNotAllowed},
		{name: "too many added targets", add: func() []appplatform.HTTPTarget {
			var list []appplatform.HTTPTarget
			for i := 0; i < 17; i++ {
				list = append(list, platformTestTarget("t"+strings.Repeat("a", i), "https://h"+strings.Repeat("a", i)+".example.com", "public", []string{"/"}, []string{"GET"}))
			}
			return list
		}(), want: errPlatformManagementInput},
	}
	_ = lanAllow
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, actor, installation := platformCapabilityFixture(t, pg)
			runner := platformCapabilityRunnerFor(t, s, installation, nil)
			if tc.setup != nil {
				tc.setup(t, s, actor, installation, runner)
			}
			before := platformCapabilityTableDump(t, s)
			plans := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`)
			_, err := platformCapabilityCreate(t, s, actor, installation, tc.add, tc.rm)
			if err == nil || !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`) != plans {
				t.Fatal("refused review wrote a plan")
			}
			if !bytes.Equal(before, platformCapabilityTableDump(t, s)) {
				t.Fatal("refused review changed storage")
			}
		})
	}
	t.Run("stale revision and digest", func(t *testing.T) {
		s, actor, installation := platformCapabilityFixture(t, pg)
		platformCapabilityRunnerFor(t, s, installation, nil)
		for _, mutate := range []func(*platformCapabilityRequest){
			func(r *platformCapabilityRequest) { r.PolicyRevision++ },
			func(r *platformCapabilityRequest) { r.PreviousPolicySHA256 = platformDigest([]byte("other")) },
		} {
			req := platformCapabilityRequestFor(t, s, installation, []appplatform.HTTPTarget{extra}, nil)
			mutate(&req)
			if _, err := s.createPlatformCapabilityPolicyReview(context.Background(), actor, platformRuntimeEndpoint{}, installation, req); !errors.Is(err, errAppPlatformPlanChanged) {
				t.Fatal("stale request accepted", err)
			}
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE plan_json LIKE '%capability-policy-review%'`) != 0 {
			t.Fatal("stale request wrote a plan")
		}
	})
	t.Run("per-target limits may only narrow", func(t *testing.T) {
		s, actor, installation := platformCapabilityFixture(t, pg)
		platformCapabilityRunnerFor(t, s, installation, nil)
		wide := platformV2ExtraTarget()
		wide.RequestBytes = 2048 // shared bound is 1024
		if _, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{wide}, nil); !errors.Is(err, errPlatformManagementInput) {
			t.Fatal("widening override accepted", err)
		}
		narrow := platformV2ExtraTarget()
		narrow.RequestBytes = 512
		if _, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{narrow}, nil); err != nil {
			t.Fatal("narrowing override refused", err)
		}
	})
}

func TestPlatformCapabilityCreateRefusalsSQLite(t *testing.T) {
	testPlatformCapabilityCreateRefusals(t, false)
}
func TestPlatformCapabilityCreateRefusalsPostgres(t *testing.T) {
	testPlatformCapabilityCreateRefusals(t, true)
}

func testPlatformCapabilityCreateSuccess(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	allow := []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{8443}}}
	platformCapabilityRunnerFor(t, s, installation, allow)
	private := platformTestTarget("lan", "https://10.20.0.5:8443", "private", []string{"/"}, []string{"GET"})
	dns := platformTestTarget("lan-name", "https://nas.internal.example:8443", "private", []string{"/"}, []string{"GET"})
	result, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget(), private, dns}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var action, source string
	var target *string
	var created, expires int64
	if err := s.st.queryRow(`SELECT action,source_release_sha256,target_release_sha256,created_ms,expires_ms FROM app_platform_review_plans WHERE id=?`, result.ID).Scan(&action, &source, &target, &created, &expires); err != nil {
		t.Fatal(err)
	}
	if action != "update" || target != nil || source != result.Plan.ReleaseSHA256 || expires-created != (5*time.Minute).Milliseconds() {
		t.Fatal("plan row shape", action, source, target, expires-created)
	}
	plan := result.Plan
	if len(plan.TargetsAdded) != 3 || len(plan.TargetsRemoved) != 0 || plan.Capability != "network.http" || plan.PolicyRevision != 1 ||
		plan.PrivateNetworksSHA256 == "" || len(plan.PrivateNetworks) != 1 || plan.Runtime.NetworkPolicy == "" {
		t.Fatalf("plan content %+v", plan)
	}
	parsed, err := appplatform.ParseHTTPPolicyV2([]byte(plan.Runtime.NetworkPolicy))
	if err != nil || len(parsed.Policy.Targets) != 4 {
		t.Fatal("merged policy", err)
	}
	if platformDigest([]byte(result.JSON)) != result.SHA256 {
		t.Fatal("digest")
	}
}

func TestPlatformCapabilityCreateSuccessSQLite(t *testing.T) {
	testPlatformCapabilityCreateSuccess(t, false)
}
func TestPlatformCapabilityCreateSuccessPostgres(t *testing.T) {
	testPlatformCapabilityCreateSuccess(t, true)
}

// platformCapabilitySeedBinding writes an active HTTP binding by raw SQL.
func platformCapabilitySeedBinding(t *testing.T, s *Server, actor platformActorSession, installation, id, name, policy string) {
	t.Helper()
	credential, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "create", label: "Upstream " + name, value: []byte("disposable-" + name)})
	if err != nil {
		t.Fatal(err)
	}
	review := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
	mustExec(t, s.st, `INSERT INTO app_platform_secret_bindings(id,installation_id,name,credential_id,credential_value_revision,operation,state,policy_json,policy_revision,review_plan_id,revision,created_ms,updated_ms,revoked_ms)
		VALUES(?,?,?,?,?,'http_bearer','active',?,?,?,1,1,1,NULL)`, id, installation, name, credential.ID, credential.ValueRevision, policy, revision, review)
}

func platformCapabilityV3Policy(t *testing.T, target appplatform.HTTPTarget) string {
	t.Helper()
	sum, err := appplatform.HTTPTargetSHA256(target)
	if err != nil {
		t.Fatal(err)
	}
	return `{"format":"portal.secret-binding-policy/3-dev.1","operation":"http_bearer","network_target_sha256":"` + sum + `","principals":["viewer"]}`
}

func platformCapabilityConsume(t *testing.T, s *Server, actor platformActorSession, plan platformCapabilityPolicyResult) (platformCapabilityPolicyChange, error) {
	t.Helper()
	return s.consumePlatformCapabilityPolicyReview(context.Background(), actor, platformRuntimeEndpoint{}, plan.ID, plan.SHA256)
}

func testPlatformCapabilityConsumeSuccess(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	platformCapabilityRunnerFor(t, s, installation, nil)
	platformCapabilitySeedBinding(t, s, actor, installation, platformFixtureID(8101), "whole-policy", `{"format":"portal.secret-binding-policy/2-dev.1","operation":"http_bearer"}`)
	platformCapabilitySeedBinding(t, s, actor, installation, platformFixtureID(8102), "on-default", platformCapabilityV3Policy(t, platformV2DefaultTargetForPolicy(t)))
	plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second pending plan of the installation, to be expired.
	pending, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformTestTarget("more", "https://more.example.com", "public", []string{"/"}, []string{"GET"})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	revisionBefore := scalar[int64](t, s.st, `SELECT revision FROM app_platform_installations WHERE id=?`, installation)
	grantsBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=?`, installation)
	auditBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.capability_policy_changed'`)
	change, err := platformCapabilityConsume(t, s, actor, plan)
	if err != nil {
		t.Fatal(err)
	}
	if change.PolicyRevision != 2 || len(change.Paused) != 1 || change.Paused[0] != "whole-policy" {
		t.Fatal("change", change)
	}
	if scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation) != 2 ||
		scalar[int64](t, s.st, `SELECT revision FROM app_platform_installations WHERE id=?`, installation) != revisionBefore {
		t.Fatal("only policy_revision may move")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision=2`, installation) != grantsBefore ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active' AND policy_revision=2`, installation) != 1 {
		t.Fatal("rows not re-stamped")
	}
	if scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation) != plan.ID ||
		scalar[string](t, s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation) != plan.Plan.Runtime.NetworkPolicy ||
		scalar[int](t, s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation) != 1 {
		t.Fatal("grant row")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE id=? AND consumed_ms IS NOT NULL`, plan.ID) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE id=? AND consumed_ms IS NULL AND expires_ms<=updated_ms`, pending.ID) != 1 {
		t.Fatal("plans")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.capability_policy_changed'`) != auditBefore+1 {
		t.Fatal("audit")
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_secret_bindings WHERE id=?`, platformFixtureID(8101)) != "review_required" ||
		scalar[int64](t, s.st, `SELECT revision FROM app_platform_secret_bindings WHERE id=?`, platformFixtureID(8101)) != 2 ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_secret_bindings WHERE id=?`, platformFixtureID(8102)) != "active" {
		t.Fatal("binding states: whole-policy pauses, a target-scoped binding on a kept target stays active")
	}
	// The other pending plan can no longer be consumed, and a replay is a conflict.
	if _, err := platformCapabilityConsume(t, s, actor, pending); !errors.Is(err, errAppPlatformPlanExpired) {
		t.Fatal("expired sibling consumed", err)
	}
	if _, err := platformCapabilityConsume(t, s, actor, plan); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatal("replay", err)
	}
	// The foreground loader and the launch overlay accept the new grant.
	if err := platformCapabilityLaunch(t, s, actor, installation); err != nil {
		t.Fatal("launch after review:", err)
	}
	for _, url := range []string{"https://api.example.com/v1/x", "https://other.example.com/v2/x"} {
		if err := platformCapabilityPrepare(t, s, actor, installation, url); err != nil {
			t.Fatal("request after review:", url, err)
		}
	}
}

func platformV2DefaultTargetForPolicy(t *testing.T) appplatform.HTTPTarget {
	t.Helper()
	return platformV2DefaultTarget()
}

func TestPlatformCapabilityConsumeSuccessSQLite(t *testing.T) {
	testPlatformCapabilityConsumeSuccess(t, false)
}
func TestPlatformCapabilityConsumeSuccessPostgres(t *testing.T) {
	testPlatformCapabilityConsumeSuccess(t, true)
}

// A target-scoped binding whose target leaves the policy pauses, and re-adding an
// identical target does not revive it.
func testPlatformCapabilityTargetBindingLifecycle(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	platformCapabilityRunnerFor(t, s, installation, nil)
	platformCapabilitySeedBinding(t, s, actor, installation, platformFixtureID(8201), "on-default", platformCapabilityV3Policy(t, platformV2DefaultTarget()))
	platformCapabilitySeedBinding(t, s, actor, installation, platformFixtureID(8202), "on-extra", platformCapabilityV3Policy(t, platformV2ExtraTarget()))
	state := func(id int) string {
		return scalar[string](t, s.st, `SELECT state FROM app_platform_secret_bindings WHERE id=?`, platformFixtureID(id))
	}
	step := func(add []appplatform.HTTPTarget, remove []string) {
		plan, err := platformCapabilityCreate(t, s, actor, installation, add, remove)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := platformCapabilityConsume(t, s, actor, plan); err != nil {
			t.Fatal(err)
		}
	}
	step([]appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
	if state(8201) != "active" || state(8202) != "active" {
		t.Fatal("adding a target paused a target-scoped binding")
	}
	step(nil, []string{"default"})
	if state(8201) != "review_required" || state(8202) != "active" {
		t.Fatal("removing a target must pause exactly its bindings", state(8201), state(8202))
	}
	step([]appplatform.HTTPTarget{platformV2DefaultTarget()}, nil)
	if state(8201) != "review_required" {
		t.Fatal("re-adding an identical target revived a paused binding")
	}
	if scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation) != 4 {
		t.Fatal("policy revision")
	}
}

func TestPlatformCapabilityTargetBindingLifecycleSQLite(t *testing.T) {
	testPlatformCapabilityTargetBindingLifecycle(t, false)
}
func TestPlatformCapabilityTargetBindingLifecyclePostgres(t *testing.T) {
	testPlatformCapabilityTargetBindingLifecycle(t, true)
}

func testPlatformCapabilityConsumeRefusals(t *testing.T, pg bool) {
	cases := []struct {
		name  string
		drift func(t *testing.T, s *Server, actor platformActorSession, installation string, plan platformCapabilityPolicyResult, r *platformCapabilityRunner)
		want  error
	}{
		{"installation revision", func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_installations SET revision=revision+1 WHERE id=?`, i)
		}, errAppPlatformPlanChanged},
		{"release change", func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			other := platformCapabilityOtherRelease(t, s, i)
			mustExec(t, s.st, `UPDATE app_platform_activations SET release_sha256=? WHERE installation_id=? AND state='active'`, other, i)
		}, errAppPlatformInstallUnavailable},
		{"trust revision", func(t *testing.T, s *Server, _ platformActorSession, i string, plan platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_publishers SET revision=revision+1 WHERE id=?`, plan.Plan.PublisherID)
		}, errAppPlatformPlanChanged},
		{"profile drift", func(t *testing.T, s *Server, _ platformActorSession, _ string, _ platformCapabilityPolicyResult, r *platformCapabilityRunner) {
			r.set(func(o *platformCapabilityObservation) { o.Network.ActiveRequests++ })
		}, errAppPlatformPlanChanged},
		{"managed profile drift", func(t *testing.T, s *Server, _ platformActorSession, _ string, _ platformCapabilityPolicyResult, r *platformCapabilityRunner) {
			r.set(func(o *platformCapabilityObservation) { o.Managed.RootInode = "999" })
		}, errAppPlatformPlanChanged},
		{"allowlist change", func(t *testing.T, s *Server, _ platformActorSession, _ string, _ platformCapabilityPolicyResult, r *platformCapabilityRunner) {
			targets, err := transport.NewNetworkTargets([]appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{8443}}})
			if err != nil {
				t.Fatal(err)
			}
			r.set(func(o *platformCapabilityObservation) { o.Targets = targets })
		}, errAppPlatformPlanChanged},
		{"runtime identity", func(t *testing.T, s *Server, _ platformActorSession, _ string, _ platformCapabilityPolicyResult, r *platformCapabilityRunner) {
			r.set(func(o *platformCapabilityObservation) { o.Runtime.fingerprint = platformDigest([]byte("elsewhere")) })
		}, errAppPlatformPlanChanged},
		{"old runner", func(t *testing.T, s *Server, _ platformActorSession, _ string, _ platformCapabilityPolicyResult, r *platformCapabilityRunner) {
			r.err = transport.ErrNetworkTargetsUnsupported
		}, transport.ErrNetworkTargetsUnsupported},
		{"previous grant changed", func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json='{"x":1}' WHERE installation_id=? AND capability='network.http'`, i)
		}, errPlatformCapabilityPolicyBusy},
		{"policy revision", func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=policy_revision+1 WHERE id=?`, i)
		}, errPlatformCapabilityPolicyBusy},
		{"unfinished operation", func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_operations SET stage='start',completed_ms=NULL WHERE installation_id=?`, i)
		}, errPlatformCapabilityPolicyBusy},
		{"expired", func(t *testing.T, s *Server, _ platformActorSession, _ string, plan platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=1 WHERE id=?`, plan.ID)
		}, errAppPlatformPlanExpired},
		{"already consumed", func(t *testing.T, s *Server, _ platformActorSession, _ string, plan platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET consumed_ms=1 WHERE id=?`, plan.ID)
		}, errAppPlatformOperationConflict},
		{"plan tampered", func(t *testing.T, s *Server, _ platformActorSession, _ string, plan platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=? WHERE id=?`, platformDigest([]byte("x")), plan.ID)
		}, errAppPlatformPlanChanged},
		{"not an administrator", func(t *testing.T, s *Server, actor platformActorSession, _ string, _ platformCapabilityPolicyResult, _ *platformCapabilityRunner) {
			mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
		}, errAppPlatformActorUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, actor, installation := platformCapabilityFixture(t, pg)
			runner := platformCapabilityRunnerFor(t, s, installation, nil)
			plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			tc.drift(t, s, actor, installation, plan, runner)
			before := platformCapabilityTableDump(t, s)
			if _, err := platformCapabilityConsume(t, s, actor, plan); err == nil || !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if !bytes.Equal(before, platformCapabilityTableDump(t, s)) {
				t.Fatal("refused consume changed storage")
			}
		})
	}
	t.Run("wrong digest argument", func(t *testing.T) {
		s, actor, installation := platformCapabilityFixture(t, pg)
		platformCapabilityRunnerFor(t, s, installation, nil)
		plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		before := platformCapabilityTableDump(t, s)
		if _, err := s.consumePlatformCapabilityPolicyReview(context.Background(), actor, platformRuntimeEndpoint{}, plan.ID, platformDigest([]byte("other"))); !errors.Is(err, errAppPlatformPlanChanged) {
			t.Fatal(err)
		}
		if !bytes.Equal(before, platformCapabilityTableDump(t, s)) {
			t.Fatal("refused consume changed storage")
		}
	})
}

func TestPlatformCapabilityConsumeRefusalsSQLite(t *testing.T) {
	testPlatformCapabilityConsumeRefusals(t, false)
}
func TestPlatformCapabilityConsumeRefusalsPostgres(t *testing.T) {
	testPlatformCapabilityConsumeRefusals(t, true)
}

// Two consumes of one plan: exactly one wins.
func testPlatformCapabilityConsumeRace(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	platformCapabilityRunnerFor(t, s, installation, nil)
	plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	audit := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.capability_policy_changed'`)
	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = platformCapabilityConsume(t, s, actor, plan)
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatal("exactly one consume may win", results)
	}
	if scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation) != 2 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.capability_policy_changed'`) != audit+1 {
		t.Fatal("a lost race still wrote")
	}
}

func TestPlatformCapabilityConsumeRaceSQLite(t *testing.T) {
	testPlatformCapabilityConsumeRace(t, false)
}
func TestPlatformCapabilityConsumeRacePostgres(t *testing.T) {
	testPlatformCapabilityConsumeRace(t, true)
}
