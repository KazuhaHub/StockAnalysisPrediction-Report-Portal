package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The production sequence of an update that must keep what an administrator granted: the release declares the
// optional network.proxy, it is declined at install, a proxy review grants it (hosts, a pin, a daily budget), and
// then an update to a newer release of the same application is reviewed and run.

func declareProxyOptional(m *appplatform.Manifest) {
	m.Capabilities.Required = []string{"background.run", "network.http", "notifications.publish"}
	m.Capabilities.Optional = []string{"network.proxy"}
	m.NetworkProxy = &appplatform.NetworkProxy{Hosts: []string{"*.eastmoney.com", "qt.gtimg.cn"}}
}

func declareNoProxy(m *appplatform.Manifest) {
	m.Capabilities.Required = []string{"background.run", "network.http", "notifications.publish"}
	m.Capabilities.Optional = nil
	m.NetworkProxy = nil
}

func declareNewOptional(m *appplatform.Manifest) { declareProxyOptional(m) }

// keptProxyGrant is what the administrator's review stored, read back for the comparison after the update.
type keptProxyGrant struct{ policy string }

// grantProxyLikeAdministrator runs the real proxy review and consume: 10 hosts, one pin, a daily budget.
func grantProxyLikeAdministrator(t *testing.T, kept *keptProxyGrant) func(*testing.T, *Server, string) {
	return func(t *testing.T, s *Server, installation string) {
		t.Helper()
		mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability<>'network.proxy'`, installation)
		actor := platformManagedFixtureSession(t, s)
		runner := &platformProxyRunner{profile: transport.NewProxyProfile()}
		runner.servePins(pinQuotes)
		s.platformObserveProxy = runner.observe
		hosts := []string{"*.internal.anchan.kazuhahub.com"}
		for i := 0; i < 9; i++ {
			hosts = append(hosts, fmt.Sprintf("feed%d.example.com", i))
		}
		req := platformProxyRequestFor(t, s, installation, hosts, 1<<30)
		req.Pins = []appplatform.ProxyPin{pinQuotes}
		review, err := s.createPlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, installation, req)
		if err != nil {
			t.Fatal("proxy review", err)
		}
		if _, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, review.ID, review.SHA256); err != nil {
			t.Fatal("proxy consume", err)
		}
		_, kept.policy, _ = platformProxyGrantRow(t, s, installation)
	}
}

func useUpdateKeepFixtures(t *testing.T, install, target func(*appplatform.Manifest), after func(*testing.T, *Server, string)) {
	t.Helper()
	platformFixtureManifestHook, platformFixtureNetworkTarget = install, true
	platformUpdateFixtureTargetHook, platformUpdateFixtureAfterInstall = target, after
	t.Cleanup(func() {
		platformFixtureManifestHook, platformFixtureNetworkTarget = nil, false
		platformUpdateFixtureTargetHook, platformUpdateFixtureAfterInstall = nil, nil
	})
	platformWritableUpdateFixtures(t)
}

func updateProjectionFor(t *testing.T, f platformManagedUpdateReviewFixture) platformUpdateProjection {
	t.Helper()
	p, err := platformBuildManagedUpdateProjection(
		f.server.st.poolExec(), f.installation, f.source, f.revision, f.policy, f.format,
		platformActorFacts{authorityFingerprint: f.sourcePlan.ActorAuthority},
		platformActorControl{epoch: f.activeEpoch, nowMS: 1}, f.target, f.release)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlatformUpdateReviewKeepsGrantedOptionalProxySQLite(t *testing.T) {
	var kept keptProxyGrant
	useUpdateKeepFixtures(t, declareProxyOptional, declareProxyOptional, grantProxyLikeAdministrator(t, &kept))
	f := platformManagedUpdateProjectionFixtureWith(t, false, declareProxyOptional)
	p := updateProjectionFor(t, f)
	if slices.Contains(p.Target.DeclinedCapabilities, appplatform.ProxyCapability) {
		t.Fatal("a granted optional capability is shown declined:", p.Target.DeclinedCapabilities)
	}
	if !slices.Contains(p.PreviousCapabilities, appplatform.ProxyCapability) || !slices.Contains(p.TargetCapabilities, appplatform.ProxyCapability) {
		t.Fatalf("the grant is not kept: previous %v target %v", p.PreviousCapabilities, p.TargetCapabilities)
	}
	digest, err := platformRemovalGrantsDigest(f.server.st.poolExec(), f.installation)
	if err != nil || digest != p.GrantsSHA256 {
		t.Fatal("grants digest does not bind the live rows, including the proxy policy", err)
	}
	// The review shows exactly what carries: the kept list and the proxy's hosts, pin and budget.
	if !slices.Equal(p.KeptCapabilities, []string{appplatform.ProxyCapability}) || p.KeptProxy == nil ||
		len(p.KeptProxy.Hosts) != 10 || len(p.KeptProxy.Pins) != 1 || p.KeptProxy.Pins[0] != pinQuotes || p.KeptProxy.DailyBytes != 1<<30 {
		t.Fatalf("the review does not show the carried proxy policy: %v %+v", p.KeptCapabilities, p.KeptProxy)
	}
	if len(p.PolicyReset) != 0 {
		t.Fatal("nothing was reset", p.PolicyReset)
	}
}

// A network.http policy that differs from the release default is not kept: the review lists it as reset.
func TestPlatformUpdateReviewListsResetNetworkPolicySQLite(t *testing.T) {
	var kept keptProxyGrant
	useUpdateKeepFixtures(t, declareProxyOptional, declareProxyOptional, grantProxyLikeAdministrator(t, &kept))
	f := platformManagedUpdateProjectionFixtureWith(t, false, declareProxyOptional)
	if p := updateProjectionFor(t, f); len(p.PolicyReset) != 0 {
		t.Fatal("an unchanged policy is listed as reset", p.PolicyReset)
	}
	mustExec(t, f.server.st, `UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='network.http'`, `{"reviewed":"later"}`, f.installation)
	p := updateProjectionFor(t, f)
	if !slices.Equal(p.PolicyReset, []string{"network.http"}) || slices.Contains(p.KeptCapabilities, "network.http") {
		t.Fatalf("a reset policy is not listed as reset or is labelled kept: reset %v kept %v", p.PolicyReset, p.KeptCapabilities)
	}
}

// A carried proxy grant must pass the checks a real approval runs: a pin the runner no longer lists, or a
// runner that fails, refuses the review instead of carrying the grant.
func TestPlatformUpdateReviewRefusesProxyThatTheRunnerNoLongerVouchesForSQLite(t *testing.T) {
	var kept keptProxyGrant
	useUpdateKeepFixtures(t, declareProxyOptional, declareProxyOptional, grantProxyLikeAdministrator(t, &kept))
	f := platformManagedUpdateProjectionFixtureWith(t, false, declareProxyOptional)
	build := func(check *platformProxyRunnerCheck) error {
		_, err := platformBuildManagedUpdateProjection(
			f.server.st.poolExec(), f.installation, f.source, f.revision, f.policy, f.format,
			platformActorFacts{authorityFingerprint: f.sourcePlan.ActorAuthority},
			platformActorControl{epoch: f.activeEpoch, nowMS: 1}, f.target, f.release, check)
		return err
	}
	listed := transport.ProxyRunnerState{Profile: transport.NewProxyProfile(), PinsSupported: true, Pins: []appplatform.ProxyPin{pinQuotes}}
	if err := build(&platformProxyRunnerCheck{state: listed}); err != nil {
		t.Fatal("a listed pin refused the review", err)
	}
	unlisted := transport.ProxyRunnerState{Profile: transport.NewProxyProfile(), PinsSupported: true, Pins: []appplatform.ProxyPin{pinOther}}
	if err := build(&platformProxyRunnerCheck{state: unlisted}); !errors.Is(err, transport.ErrProxyUnsupported) {
		t.Fatal("an unlisted pin was carried:", err)
	}
	if err := build(&platformProxyRunnerCheck{err: transport.ErrProxyUnsupported}); !errors.Is(err, transport.ErrProxyUnsupported) {
		t.Fatal("a failing runner did not refuse:", err)
	}
}

func TestPlatformUpdateReviewRemovesOptionalProxyTheReleaseDropsSQLite(t *testing.T) {
	var kept keptProxyGrant
	useUpdateKeepFixtures(t, declareProxyOptional, declareNoProxy, grantProxyLikeAdministrator(t, &kept))
	f := platformManagedUpdateProjectionFixtureWith(t, false, declareNoProxy)
	p := updateProjectionFor(t, f)
	if slices.Contains(p.TargetCapabilities, appplatform.ProxyCapability) || !slices.Contains(p.PreviousCapabilities, appplatform.ProxyCapability) {
		t.Fatalf("a dropped capability is not removed: previous %v target %v", p.PreviousCapabilities, p.TargetCapabilities)
	}
}

func TestPlatformUpdateReviewDeclinesNewlyDeclaredOptionalSQLite(t *testing.T) {
	useUpdateKeepFixtures(t, declareNoProxy, declareNewOptional, nil)
	f := platformManagedUpdateProjectionFixtureWith(t, false, declareNewOptional)
	p := updateProjectionFor(t, f)
	if !slices.Equal(p.Target.DeclinedCapabilities, []string{appplatform.ProxyCapability}) || slices.Contains(p.TargetCapabilities, appplatform.ProxyCapability) {
		t.Fatalf("a newly declared optional capability is not declined: declined %v target %v", p.Target.DeclinedCapabilities, p.TargetCapabilities)
	}
}

func TestPlatformUpdateKeepsOptionalProxyGrantAcrossActivationSQLite(t *testing.T) {
	var kept keptProxyGrant
	useUpdateKeepFixtures(t, declareProxyOptional, declareProxyOptional, grantProxyLikeAdministrator(t, &kept))
	s, lease, intent, endpoint, observation := platformWritableUpdateStartRegistrationCommitFixture(t, false)
	if kept.policy == "" || kept.policy == appplatform.EmptyProxyPolicyJSON {
		t.Fatal("the proxy review granted nothing")
	}
	registered, err := s.commitPlatformUpdateStartRegistration(context.Background(), lease, intent, endpoint, observation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(t, s, registered, endpoint)
	defer applied.registration.Close()
	listed := &platformProxyRunnerCheck{state: transport.ProxyRunnerState{Profile: transport.NewProxyProfile(), PinsSupported: true, Pins: []appplatform.ProxyPin{pinQuotes}}}
	unlisted := &platformProxyRunnerCheck{state: transport.ProxyRunnerState{Profile: transport.NewProxyProfile(), PinsSupported: true}}
	if _, err := s.commitPlatformUpdateStartActivationChecked(context.Background(), registered, applied, boot, unlisted); !errors.Is(err, transport.ErrProxyUnsupported) {
		t.Fatal("activation carried a pin the runner no longer lists:", err)
	}
	if scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, lease.installationID) != 1 {
		t.Fatal("a refused activation moved the generation")
	}
	if _, err := s.commitPlatformUpdateStartActivationChecked(context.Background(), registered, applied, boot, listed); err != nil {
		t.Fatal("update activation", err)
	}
	granted, policy, _ := platformProxyGrantRow(t, s, lease.installationID)
	if granted != 1 || policy != kept.policy {
		t.Fatalf("the new generation lost the proxy grant: granted %d policy %s", granted, policy)
	}
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, lease.installationID)
	revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, lease.installationID)
	if generation != 2 {
		t.Fatal("generation", generation)
	}
	grant, err := loadPlatformProxyGrant(s.st.poolExec(), lease.installationID, revision, applied.start.plan.TargetReleaseSHA256, lease.controllerEpoch)
	if err != nil {
		t.Fatal("the carried grant is not backed under the new release:", err)
	}
	if len(grant.Policy.Hosts) != 10 || len(grant.Policy.Pins) != 1 || grant.Policy.Pins[0] != pinQuotes || grant.Policy.DailyBytes != 1<<30 {
		t.Fatalf("hosts, pin or budget changed: %+v", grant.Policy)
	}
	// The synthesized plan is marked, follows the convention of a real plan, and is audited.
	carried, carriedID := loadPlatformLatestProxyPlanFor(t, s, lease.installationID)
	if carried.CarriedFromPlan == "" || carried.PolicyRevision != 2 || carried.Generation != 2 || carried.Policy != kept.policy || carried.ReleaseSHA256 != applied.start.plan.TargetReleaseSHA256 {
		t.Fatalf("carried plan %+v", carried)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_kept_capabilities' AND target_id=? AND detail LIKE ?`, lease.installationID, "%"+carriedID+"%") != 1 {
		t.Fatal("the carry was not audited with the plan id")
	}
	// Clock skew cannot make an older plan win: the order is by policy revision, not by consumption time.
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET consumed_ms=0 WHERE id=?`, carriedID)
	if _, err := loadPlatformProxyGrant(s.st.poolExec(), lease.installationID, revision, applied.start.plan.TargetReleaseSHA256, lease.controllerEpoch); err != nil {
		t.Fatal("plan order depends on consumed_ms:", err)
	}
	actor := platformManagedFixtureSession(t, s)
	backend, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal("launch after the update", err)
	}
	backend.Close()
	if launch, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err != nil || launch.generation != "2" {
		t.Fatal("launch inspection", launch, err)
	}
}

// A static app that declares principals.read, with the grant on: every update keeps it, the updated app
// launches, and the next update (which verifies the previous generation's activation proof) succeeds too.
func TestPlatformStaticUpdateKeepsPrincipalsReadAcrossTwoUpdatesSQLite(t *testing.T) {
	s, admin, reader, id := platformPrincipalsFixtureAdmin(t, false)
	platformGrantLaunchUser(t, s, reader, id)
	for n, version := range []string{"1.0.0-beta.2", "1.0.0-beta.3"} {
		raw, _, archive := platformSignedReleaseFixture(t, version)
		var manifest appplatform.Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
		manifest.Capabilities.Required = nil
		manifest.Capabilities.Optional = []string{"principals.read"}
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		target, err := s.stagePlatformRelease(admin, raw, platformSignTestDescriptor(t, raw), archive)
		if err != nil {
			t.Fatal(err)
		}
		review, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), admin, id, target.sha256, 1)
		if err != nil {
			t.Fatalf("update %d review: %v", n+1, err)
		}
		var projection platformUpdateProjection
		if err := json.Unmarshal([]byte(review.JSON), &projection); err != nil || slices.Contains(projection.Target.DeclinedCapabilities, "principals.read") {
			t.Fatal("the kept grant is declined in the review", err)
		}
		commit, err := s.consumePlatformUpdateReview(admin, review.ID, review.SHA256, platformFixtureID(6100+2*n))
		if err != nil {
			t.Fatal(err)
		}
		lease, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(6101+2*n))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.runPlatformStaticUpdate(context.Background(), lease); err != nil {
			t.Fatalf("update %d: %v", n+1, err)
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND capability='principals.read' AND granted=1`, id) != 1 {
			t.Fatalf("update %d dropped the grant", n+1)
		}
		if binding, err := s.inspectPlatformLaunchAccess(reader, id); err != nil || binding.releaseSHA != target.sha256 {
			t.Fatalf("launch after update %d: %v", n+1, err)
		}
	}
}

func loadPlatformLatestProxyPlanFor(t *testing.T, s *Server, installation string) (platformProxyPlan, string) {
	t.Helper()
	row, err := loadPlatformLatestProxyPlan(s.st.poolExec(), installation)
	if err != nil {
		t.Fatal(err)
	}
	return row.plan, row.id
}
