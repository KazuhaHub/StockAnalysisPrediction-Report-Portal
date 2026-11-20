package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformCapabilityNarrowPolicy(t *testing.T) (string, appplatform.HTTPTarget, appplatform.HTTPTarget) {
	t.Helper()
	narrow := platformV2ExtraTarget()
	narrow.RequestBytes = 512
	private := platformTestTarget("lan", "https://10.20.0.5:8443", "private", []string{"/"}, []string{"GET"})
	return platformV2PolicyFixture(t, platformV2DefaultTarget(), narrow, private), narrow, private
}

func platformCapabilityPrepareIntent(t *testing.T, s *Server, actor platformActorSession, installation, url string, body []byte) (platformNetworkRequestIntent, error) {
	t.Helper()
	release := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: itoa64(revision), ReleaseSHA256: release}
	return s.preparePlatformNetworkRequest(context.Background(), actor, installation, binding, "GET", url, "application/json", "", "", body)
}

// Select decides the body limit of a foreground request: a narrower target
// limit applies to its own target only, and a private policy carries the runner
// allowlist digest the plan recorded.
func testPlatformCapabilityForegroundSelect(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	policy, _, _ := platformCapabilityNarrowPolicy(t)
	plan, _ := platformCapabilitySeedPlan(t, s, installation, platformCapabilitySeed{policy: policy, mutate: func(p *platformCapabilityPolicyPlan) {
		networks := []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{8443}}}
		sum, err := appplatform.PrivateNetworksSHA256(networks)
		if err != nil {
			t.Fatal(err)
		}
		p.PrivateNetworks, p.PrivateNetworksSHA256 = networks, sum
	}})
	_ = plan
	body := []byte(strings.Repeat("x", 600))
	if i, err := platformCapabilityPrepareIntent(t, s, actor, installation, "https://other.example.com/v2/x", body); err == nil {
		i.Close()
		t.Fatal("body above the target's narrower limit accepted")
	}
	small := []byte(strings.Repeat("x", 400))
	i, err := platformCapabilityPrepareIntent(t, s, actor, installation, "https://other.example.com/v2/x", small)
	if err != nil {
		t.Fatal("body within the target's limit refused", err)
	}
	i.Close()
	i, err = platformCapabilityPrepareIntent(t, s, actor, installation, "https://api.example.com/v1/x", body)
	if err != nil {
		t.Fatal("shared limit refused on the default target", err)
	}
	if i.command.PrivateNetworksSHA256 == "" {
		t.Fatal("a policy with a private target must carry the allowlist digest on every command")
	}
	want, _ := appplatform.PrivateNetworksSHA256([]appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{8443}}})
	if i.command.PrivateNetworksSHA256 != want || i.grant.privateNetworksSHA != want {
		t.Fatal("allowlist digest")
	}
	i.Close()
	i, err = platformCapabilityPrepareIntent(t, s, actor, installation, "https://10.20.0.5:8443/x", small)
	if err != nil {
		t.Fatal("private target refused", err)
	}
	i.Close()
}

func TestPlatformCapabilityForegroundSelectSQLite(t *testing.T) {
	testPlatformCapabilityForegroundSelect(t, false)
}
func TestPlatformCapabilityForegroundSelectPostgres(t *testing.T) {
	testPlatformCapabilityForegroundSelect(t, true)
}

func testPlatformCapabilityPublicPolicyHasNoDigest(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	platformCapabilitySeedPlan(t, s, installation, platformCapabilitySeed{})
	i, err := platformCapabilityPrepareIntent(t, s, actor, installation, "https://other.example.com/v2/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	if i.command.PrivateNetworksSHA256 != "" {
		t.Fatal("a public-only policy must not carry the digest")
	}
}

func TestPlatformCapabilityPublicPolicyHasNoDigestSQLite(t *testing.T) {
	testPlatformCapabilityPublicPolicyHasNoDigest(t, false)
}
func TestPlatformCapabilityPublicPolicyHasNoDigestPostgres(t *testing.T) {
	testPlatformCapabilityPublicPolicyHasNoDigest(t, true)
}

// A review consumed while a call is in flight denies it: the signed intent
// carries the old review and policy revision, and the check re-reads both.
func testPlatformCapabilityMidCallReview(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	platformCapabilityRunnerFor(t, s, installation, nil)
	i, err := platformCapabilityPrepareIntent(t, s, actor, installation, "https://api.example.com/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	if err := s.withPlatformNetworkRequestCurrent(context.Background(), actor, installation, i, nil); err != nil {
		t.Fatal("current check refused before the review", err)
	}
	plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platformCapabilityConsume(t, s, actor, plan); err != nil {
		t.Fatal(err)
	}
	if err := s.withPlatformNetworkRequestCurrent(context.Background(), actor, installation, i, nil); err == nil {
		t.Fatal("an in-flight call survived a consumed review")
	}
	if _, err := s.completePlatformNetworkRequest(context.Background(), actor, installation, i, transport.NetworkObservation{}, platformRuntimeEndpoint{}); err == nil {
		t.Fatal("result released after a consumed review")
	}
}

func TestPlatformCapabilityMidCallReviewSQLite(t *testing.T) {
	testPlatformCapabilityMidCallReview(t, false)
}
func TestPlatformCapabilityMidCallReviewPostgres(t *testing.T) {
	testPlatformCapabilityMidCallReview(t, true)
}

// The background egress path uses the same selection, and a review changes its
// pin so an open session closes and reopens.
func testPlatformCapabilityEgress(t *testing.T, pg bool) {
	s, _, installation := platformCapabilityFixture(t, pg)
	policy, _, _ := platformCapabilityNarrowPolicy(t)
	platformCapabilitySeedPlan(t, s, installation, platformCapabilitySeed{policy: policy, mutate: func(p *platformCapabilityPolicyPlan) {
		networks := []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{8443}}}
		sum, _ := appplatform.PrivateNetworksSHA256(networks)
		p.PrivateNetworks, p.PrivateNetworksSHA256 = networks, sum
	}})
	state := platformServiceEgressState(t, s, installation, "network.http", "notifications.publish")
	admit := func(url string, body int) error {
		a, err := s.loadServiceEgressForSession(context.Background(), state)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		admitCall := platformServiceEgressAdmit()
		admitCall.URL, admitCall.BodyBytes = url, body
		command, _, _, err := a.signServiceEgressCommand(state.sessionID, admitCall, time.Now().UnixMilli(), state.expiresMS)
		if err == nil && command.PrivateNetworksSHA256 == "" {
			t.Fatal("private policy command lacks the allowlist digest")
		}
		return err
	}
	if err := admit("https://other.example.com/v2/x", 600); err == nil {
		t.Fatal("egress body above the target's narrower limit signed")
	}
	if err := admit("https://other.example.com/v2/x", 400); err != nil {
		t.Fatal("egress body within the limit refused", err)
	}
	if err := admit("https://api.example.com/v1/x", 600); err != nil {
		t.Fatal("egress shared limit refused", err)
	}
	if err := admit("https://nowhere.example.com/x", 1); err == nil {
		t.Fatal("egress target outside the policy signed")
	}
	// A further review moves the pin.
	platformCapabilitySeedPlan(t, s, installation, platformCapabilitySeed{revision: 2, policy: platformV2PolicyFixture(t, platformV2DefaultTarget(), platformV2ExtraTarget(), platformTestTarget("more", "https://more.example.com", "public", []string{"/"}, []string{"GET"})), rowPolicy: ""})
	if a, err := s.loadServiceEgressForSession(context.Background(), state); err == nil {
		a.Close()
		t.Fatal("an open egress session survived a review")
	}
}

func TestPlatformCapabilityEgressSQLite(t *testing.T)   { testPlatformCapabilityEgress(t, false) }
func TestPlatformCapabilityEgressPostgres(t *testing.T) { testPlatformCapabilityEgress(t, true) }
