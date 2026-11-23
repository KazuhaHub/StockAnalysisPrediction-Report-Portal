package app

import (
	"context"
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
	if _, err := s.commitPlatformUpdateStartActivation(context.Background(), registered, applied, boot); err != nil {
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
