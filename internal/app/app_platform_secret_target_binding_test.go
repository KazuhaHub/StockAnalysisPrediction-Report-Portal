package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var (
	platformTargetDefaultURL = platformBindingDestination{method: "GET", url: "https://api.example.com/v1/x"}
	platformTargetOtherURL   = platformBindingDestination{method: "GET", url: "https://other.example.com/v1/x"}
	platformTargetLANURL     = platformBindingDestination{method: "GET", url: "http://10.0.0.5/v1/x"}
)

func platformTargetV2(t *testing.T, targets ...appplatform.HTTPTarget) string {
	t.Helper()
	raw, err := appplatform.CanonicalHTTPPolicyV2(appplatform.HTTPPolicyV2{Targets: targets, RequestBytes: 1024, ResponseBytes: 4096, Deadline: time.Second, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func platformTargetDefault() appplatform.HTTPTarget {
	return appplatform.HTTPTarget{ID: "default", Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"GET"}, Network: appplatform.HTTPNetworkPublic}
}
func platformTargetOther() appplatform.HTTPTarget {
	return appplatform.HTTPTarget{ID: "other", Origin: "https://other.example.com", Paths: []string{"/v1"}, Methods: []string{"GET"}, Network: appplatform.HTTPNetworkPublic}
}
func platformTargetLAN() appplatform.HTTPTarget {
	return appplatform.HTTPTarget{ID: "lan", Origin: "http://10.0.0.5", Paths: []string{"/v1"}, Methods: []string{"GET"}, Network: appplatform.HTTPNetworkPrivate}
}

// platformTargetSetGrant stands in for a policy review (which lands with the
// review routes): it writes the network policy and moves the installation, the
// activation and the grants to the next policy revision.
func platformTargetSetGrant(t *testing.T, s *Server, installation, policy string) {
	t.Helper()
	revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation) + 1
	now := time.Now().UnixMilli()
	mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=?,updated_ms=? WHERE id=?`, revision, now, installation)
	mustExec(t, s.st, `UPDATE app_platform_activations SET policy_revision=? WHERE installation_id=?`, revision, installation)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=? WHERE installation_id=?`, revision, installation)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='network.http'`, policy, installation)
}

func platformTargetBind(t *testing.T, s *Server, admin platformActorSession, installation, name, credential, target, principals string) platformSecretBindingMetadata {
	t.Helper()
	plan, err := s.createPlatformSecretBindingReviewTarget(admin, installation, name, credential, "http_bearer", principals, target)
	if err != nil {
		t.Fatal("binding review:", err)
	}
	binding, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256)
	if err != nil {
		t.Fatal("binding commit:", err)
	}
	return binding
}

// platformTargetUse reads a binding for a destination the way a call does: the
// viewer path resolves the command (custody access) and the service path loads
// the binding for the service principal.
func platformTargetUse(s *Server, installation, name, principal string, dest platformBindingDestination) error {
	e := s.st.poolExec()
	_, _, value, err := s.resolvePlatformSecretBindingCommandFor(e, installation, name, principal, dest)
	clear(value)
	return err
}

func platformTargetExpectDenied(t *testing.T, s *Server, installation, name, principal string, dest platformBindingDestination, what string) {
	t.Helper()
	before := platformCredentialOpenCount.Load()
	if err := platformTargetUse(s, installation, name, principal, dest); err == nil {
		t.Fatal(what, "was allowed")
	}
	if platformCredentialOpenCount.Load() != before {
		t.Fatal(what, "reached the custody")
	}
}

func platformTargetExpectUsable(t *testing.T, s *Server, installation, name, principal string, dest platformBindingDestination, what string) {
	t.Helper()
	if err := platformTargetUse(s, installation, name, principal, dest); err != nil {
		t.Fatal(what, "was refused:", err)
	}
}

func platformTargetPolicy(t *testing.T, s *Server, id string) platformSecretBindingPolicy {
	t.Helper()
	p, err := parsePlatformSecretBindingPolicy([]byte(scalar[string](t, s.st, `SELECT policy_json FROM app_platform_secret_bindings WHERE id=?`, id)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A binding review of an HTTP operation writes the v3 policy, bound to the
// digest of one target. Against today's v1 grant that target is "default", with
// the same digest as its v2 equivalent.
func testPlatformTargetBindingAgainstV1Grant(t *testing.T, pg bool) {
	s, admin, installation, credential := platformSecretBindingFixture(t, pg)
	plan, err := s.createPlatformSecretBindingReview(admin, installation, "upstream-api", credential.ID, "http_bearer")
	if err != nil {
		t.Fatal(err)
	}
	var review platformSecretBindingReview
	if json.Unmarshal([]byte(plan.JSON), &review) != nil || review.NetworkTarget != "https://api.example.com" || !platformValidSHA(review.NetworkTargetSHA256) {
		t.Fatal("review does not carry the target", plan.JSON)
	}
	binding, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	raw := scalar[string](t, s.st, `SELECT policy_json FROM app_platform_secret_bindings WHERE id=?`, binding.ID)
	if !strings.Contains(raw, "portal.secret-binding-policy/3-dev.1") || strings.Contains(raw, "network_policy_sha256") {
		t.Fatal("binding did not get the v3 policy", raw)
	}
	v1 := scalar[string](t, s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	parsed, err := appplatform.ParseHTTPPolicyAny([]byte(v1))
	if err != nil {
		t.Fatal(err)
	}
	same, err := appplatform.HTTPTargetSHA256(parsed.Policy.Targets[0])
	if err != nil {
		t.Fatal(err)
	}
	v2 := platformTargetV2(t, platformTargetDefault())
	v2parsed, _ := appplatform.ParseHTTPPolicyAny([]byte(v2))
	v2sha, _ := appplatform.HTTPTargetSHA256(v2parsed.Policy.Targets[0])
	policy := platformTargetPolicy(t, s, binding.ID)
	if policy.networkTargetSHA256 != same || same != v2sha || policy.principals != "" || !policy.allows(platformPrincipalViewer) || policy.allows(platformPrincipalService) {
		t.Fatal("v3 policy digest", policy, same, v2sha)
	}
	platformTargetExpectUsable(t, s, installation, "upstream-api", platformPrincipalViewer, platformTargetDefaultURL, "v3 binding on a v1 grant")
	platformTargetExpectDenied(t, s, installation, "upstream-api", platformPrincipalViewer, platformTargetOtherURL, "another origin")
	platformTargetExpectDenied(t, s, installation, "upstream-api", platformPrincipalService, platformTargetDefaultURL, "a viewer-only binding for the service")
	// The same binding is still usable once the grant is the v2 equivalent.
	platformTargetSetGrant(t, s, installation, v2)
	platformTargetExpectUsable(t, s, installation, "upstream-api", platformPrincipalViewer, platformTargetDefaultURL, "v3 binding on the v2 equivalent")
}

// Adding a target to the grant leaves a binding on another target usable, a
// binding for target A is denied for target B before the custody, and removing
// the binding's own target makes it unavailable.
func testPlatformTargetBindingFollowsItsTarget(t *testing.T, pg bool) {
	s, admin, installation, credential := platformSecretBindingFixture(t, pg)
	a := platformTargetBind(t, s, admin, installation, "api-a", credential.ID, "", platformPrincipalsBoth)
	platformTargetSetGrant(t, s, installation, platformTargetV2(t, platformTargetDefault(), platformTargetOther()))
	for _, principal := range []string{platformPrincipalViewer, platformPrincipalService} {
		platformTargetExpectUsable(t, s, installation, "api-a", principal, platformTargetDefaultURL, "binding after a target was added")
		platformTargetExpectDenied(t, s, installation, "api-a", principal, platformTargetOtherURL, "binding for target A used for target B")
	}
	b := platformTargetBind(t, s, admin, installation, "api-b", credential.ID, "other", platformPrincipalsServiceOnly)
	if a.ID == b.ID || platformTargetPolicy(t, s, a.ID).networkTargetSHA256 == platformTargetPolicy(t, s, b.ID).networkTargetSHA256 {
		t.Fatal("two targets share a digest")
	}
	platformTargetExpectUsable(t, s, installation, "api-b", platformPrincipalService, platformTargetOtherURL, "binding on the added target")
	platformTargetExpectDenied(t, s, installation, "api-b", platformPrincipalService, platformTargetDefaultURL, "binding for target B used for target A")
	platformTargetExpectDenied(t, s, installation, "api-b", platformPrincipalViewer, platformTargetOtherURL, "a service-only binding for the viewer")
	// A target with a ambiguous review (two targets, none named) is refused.
	if _, err := s.createPlatformSecretBindingReview(admin, installation, "api-c", credential.ID, "http_bearer"); err == nil {
		t.Fatal("a review of a many-target policy chose a target")
	}
	if _, err := s.createPlatformSecretBindingReviewTarget(admin, installation, "api-c", credential.ID, "http_bearer", "", "nope"); err == nil {
		t.Fatal("a review named a target the grant does not have")
	}
	// Removing a target removes the binding's use of it, and only its.
	platformTargetSetGrant(t, s, installation, platformTargetV2(t, platformTargetOther()))
	platformTargetExpectDenied(t, s, installation, "api-a", platformPrincipalViewer, platformTargetDefaultURL, "binding whose target was removed")
	platformTargetExpectDenied(t, s, installation, "api-a", platformPrincipalViewer, platformTargetOtherURL, "binding whose target was removed, on another target")
	platformTargetExpectDenied(t, s, installation, "api-a", platformPrincipalViewer, platformBindingDestination{}, "binding whose target was removed, with no destination")
	platformTargetExpectUsable(t, s, installation, "api-b", platformPrincipalService, platformTargetOtherURL, "the other binding after a removal")
}

// v1 and 2-dev.1 bindings keep whole-policy scope: any change of the network
// policy (or its revision) ends them, even one that leaves their target alone.
func testPlatformTargetBindingLegacyKeepsWholePolicyScope(t *testing.T, pg bool) {
	for _, principals := range []string{"", platformPrincipalsBoth} {
		s, admin, installation, credential := platformSecretBindingFixture(t, pg)
		binding := platformTargetBind(t, s, admin, installation, "legacy-api", credential.ID, "", principals)
		platformTargetMakeLegacy(t, s, binding.ID, installation, principals)
		policy := platformTargetPolicy(t, s, binding.ID)
		if policy.networkTargetSHA256 != "" || policy.networkPolicySHA256 == "" {
			t.Fatal("not a whole-policy binding", policy)
		}
		platformTargetExpectUsable(t, s, installation, "legacy-api", platformPrincipalViewer, platformTargetDefaultURL, "whole-policy binding on an unchanged grant")
		if principals != "" {
			platformTargetExpectUsable(t, s, installation, "legacy-api", platformPrincipalService, platformTargetDefaultURL, "whole-policy service binding")
		}
		platformTargetSetGrant(t, s, installation, platformTargetV2(t, platformTargetDefault(), platformTargetOther()))
		platformTargetExpectDenied(t, s, installation, "legacy-api", platformPrincipalViewer, platformTargetDefaultURL, "whole-policy binding after the policy changed")
	}
}

// platformTargetMakeLegacy rewrites a v3 binding and its consumed review into
// what the code before the target digests wrote (v1, or 2-dev.1 with principals).
func platformTargetMakeLegacy(t *testing.T, s *Server, id, installation, principals string) {
	t.Helper()
	planID := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_secret_bindings WHERE id=?`, id)
	var plan platformSecretBindingReview
	if err := json.Unmarshal([]byte(scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=?`, planID)), &plan); err != nil {
		t.Fatal(err)
	}
	plan.NetworkTarget, plan.NetworkTargetSHA256 = "", ""
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := platformSecretBindingPolicyFor(plan.Operation, plan.NetworkPolicySHA256, principals)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=?`, string(raw), platformDigest(raw), planID)
	mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET policy_json=? WHERE id=?`, string(policy), id)
}

// A plain-http target is refused when a binding is reviewed, and a call whose
// URL selects one never reaches the custody, whichever binding it names.
func testPlatformTargetBindingRefusesPlainHTTP(t *testing.T, pg bool) {
	s, admin, installation, credential := platformSecretBindingFixture(t, pg)
	api := platformTargetBind(t, s, admin, installation, "api-a", credential.ID, "", "")
	platformTargetSetGrant(t, s, installation, platformTargetV2(t, platformTargetDefault(), platformTargetLAN()))
	before := platformCredentialOpenCount.Load()
	if _, err := s.createPlatformSecretBindingReviewTarget(admin, installation, "lan-api", credential.ID, "http_bearer", "", "lan"); err == nil {
		t.Fatal("a binding review accepted a plain-http target")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_secret_bindings`) != 1 {
		t.Fatal("a refused review wrote a binding")
	}
	// A binding and a consumed review both forged for the http target's digest
	// are still no way to it: the plain-http refusal is the use-time check alone.
	lan, err := appplatform.ParseHTTPPolicyAny([]byte(platformTargetV2(t, platformTargetLAN())))
	if err != nil {
		t.Fatal(err)
	}
	lanSHA, _ := appplatform.HTTPTargetSHA256(lan.Policy.Targets[0])
	platformTargetForgeTarget(t, s, api.ID, lanSHA)
	platformTargetExpectDenied(t, s, installation, "api-a", platformPrincipalViewer, platformTargetLANURL, "a binding on a plain-http target")
	if platformCredentialOpenCount.Load() != before {
		t.Fatal("a refusal reached the custody")
	}
}

// platformTargetForgeTarget points a binding, and the consumed review that
// admits it, at another target digest, as a store write could.
func platformTargetForgeTarget(t *testing.T, s *Server, id, sha string) {
	t.Helper()
	planID := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_secret_bindings WHERE id=?`, id)
	var plan platformSecretBindingReview
	if err := json.Unmarshal([]byte(scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=?`, planID)), &plan); err != nil {
		t.Fatal(err)
	}
	plan.NetworkTargetSHA256 = sha
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: plan.Operation, networkTargetSHA256: sha})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=?`, string(raw), platformDigest(raw), planID)
	mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET policy_json=? WHERE id=?`, string(policy), id)
}

// The egress path judges the target before it reads the binding: a binding for
// another target never opens the value, and a v3 binding on the grant's
// target still does.
func testPlatformTargetBindingEgressPath(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsServiceOnly)
	state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
	id := scalar[string](t, s.st, `SELECT id FROM app_platform_secret_bindings WHERE installation_id=?`, installation)
	if !strings.Contains(scalar[string](t, s.st, `SELECT policy_json FROM app_platform_secret_bindings WHERE id=?`, id), "3-dev.1") {
		t.Fatal("service binding is not v3")
	}
	admit := platformServiceCredentialAdmit()
	load := func() (platformServiceEgressAuthority, error) {
		return s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{
			identity: state.identity, resources: state.resources, credential: "upstream-api", decrypt: true, admit: &admit,
		})
	}
	before := platformCredentialOpenCount.Load()
	a, err := load()
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	if platformCredentialOpenCount.Load() != before+1 {
		t.Fatal("the matching target did not open the value once")
	}
	other, _ := appplatform.ParseHTTPPolicyAny([]byte(platformTargetV2(t, platformTargetOther())))
	otherSHA, _ := appplatform.HTTPTargetSHA256(other.Policy.Targets[0])
	forged, err := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: "http_bearer", networkTargetSHA256: otherSHA, principals: platformPrincipalsServiceOnly})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET policy_json=? WHERE id=?`, string(forged), id)
	before = platformCredentialOpenCount.Load()
	if a, err := load(); err == nil || !errors.Is(err, errPlatformServiceEgressDenied) {
		if err == nil {
			a.Close()
		}
		t.Fatal("a binding for another target was not denied", err)
	}
	if platformCredentialOpenCount.Load() != before {
		t.Fatal("a binding for another target reached the custody")
	}
}

func TestPlatformTargetBindingAgainstV1GrantSQLite(t *testing.T) {
	testPlatformTargetBindingAgainstV1Grant(t, false)
}
func TestPlatformTargetBindingAgainstV1GrantPostgres(t *testing.T) {
	testPlatformTargetBindingAgainstV1Grant(t, true)
}
func TestPlatformTargetBindingFollowsItsTargetSQLite(t *testing.T) {
	testPlatformTargetBindingFollowsItsTarget(t, false)
}
func TestPlatformTargetBindingFollowsItsTargetPostgres(t *testing.T) {
	testPlatformTargetBindingFollowsItsTarget(t, true)
}
func TestPlatformTargetBindingLegacyKeepsWholePolicyScopeSQLite(t *testing.T) {
	testPlatformTargetBindingLegacyKeepsWholePolicyScope(t, false)
}
func TestPlatformTargetBindingLegacyKeepsWholePolicyScopePostgres(t *testing.T) {
	testPlatformTargetBindingLegacyKeepsWholePolicyScope(t, true)
}
func TestPlatformTargetBindingRefusesPlainHTTPSQLite(t *testing.T) {
	testPlatformTargetBindingRefusesPlainHTTP(t, false)
}
func TestPlatformTargetBindingRefusesPlainHTTPPostgres(t *testing.T) {
	testPlatformTargetBindingRefusesPlainHTTP(t, true)
}
func TestPlatformTargetBindingEgressPathSQLite(t *testing.T) {
	testPlatformTargetBindingEgressPath(t, false)
}
func TestPlatformTargetBindingEgressPathPostgres(t *testing.T) {
	testPlatformTargetBindingEgressPath(t, true)
}

func TestPlatformSecretBindingPolicyV3(t *testing.T) {
	sha := platformDigest([]byte("target"))
	for _, tc := range []struct {
		principals string
		list       string
	}{{"", `["viewer"]`}, {platformPrincipalsServiceOnly, `["service"]`}, {platformPrincipalsBoth, `["service","viewer"]`}} {
		raw, err := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: "http_bearer", networkTargetSHA256: sha, principals: tc.principals})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"format":"portal.secret-binding-policy/3-dev.1","operation":"http_bearer","network_target_sha256":"` + sha + `","principals":` + tc.list + `}`
		if string(raw) != want {
			t.Fatal("v3 spelling", string(raw))
		}
		p, err := parsePlatformSecretBindingPolicy(raw)
		if err != nil || p.networkTargetSHA256 != sha || p.principals != tc.principals || p.networkPolicySHA256 != "" {
			t.Fatal("v3 round trip", p, err)
		}
	}
	for name, raw := range map[string]string{
		"empty principals":  `{"format":"portal.secret-binding-policy/3-dev.1","operation":"http_bearer","network_target_sha256":"` + sha + `","principals":[]}`,
		"unsorted":          `{"format":"portal.secret-binding-policy/3-dev.1","operation":"http_bearer","network_target_sha256":"` + sha + `","principals":["viewer","service"]}`,
		"duplicate":         `{"format":"portal.secret-binding-policy/3-dev.1","operation":"http_bearer","network_target_sha256":"` + sha + `","principals":["viewer","viewer"]}`,
		"unknown principal": `{"format":"portal.secret-binding-policy/3-dev.1","operation":"http_bearer","network_target_sha256":"` + sha + `","principals":["admin"]}`,
		"policy digest":     `{"format":"portal.secret-binding-policy/3-dev.1","operation":"http_bearer","network_policy_sha256":"` + sha + `","principals":["viewer"]}`,
		"raw read":          `{"format":"portal.secret-binding-policy/3-dev.1","operation":"raw_read","network_target_sha256":"` + sha + `","principals":["viewer"]}`,
		"short digest":      `{"format":"portal.secret-binding-policy/3-dev.1","operation":"http_bearer","network_target_sha256":"abc","principals":["viewer"]}`,
	} {
		if _, err := parsePlatformSecretBindingPolicy([]byte(raw)); err == nil {
			t.Fatal("accepted", name)
		}
	}
	if _, err := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: "http_bearer", networkTargetSHA256: sha, networkPolicySHA256: sha}); err == nil {
		t.Fatal("a policy carried both digests")
	}
	if _, err := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: "raw_read", networkTargetSHA256: sha}); err == nil {
		t.Fatal("raw_read carried a target")
	}
}

// A v3 binding is never used without a destination: a zero destination skips
// neither the target selection nor the plain-http refusal.
func testPlatformTargetBindingNeedsDestination(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsBoth)
	e := s.st.poolExec()
	facts, err := loadPlatformSecretBindingFacts(e, installation, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, principal := range []string{platformPrincipalViewer, platformPrincipalService} {
		if _, _, err := loadPlatformSecretBindingUseFor(e, installation, "upstream-api", facts, principal, platformBindingDestination{}); err == nil {
			t.Fatal("a v3 binding was read without a destination for", principal)
		}
	}
	// The egress load that opens the value refuses a call with no destination.
	state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
	before := platformCredentialOpenCount.Load()
	if a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{
		identity: state.identity, resources: state.resources, credential: "upstream-api", decrypt: true,
	}); err == nil {
		a.Close()
		t.Fatal("an egress load opened a credential with no destination")
	}
	if platformCredentialOpenCount.Load() != before {
		t.Fatal("a load with no destination reached the custody")
	}
	// Nor does a whole-policy binding open for a call that names no destination.
	id := scalar[string](t, s.st, `SELECT id FROM app_platform_secret_bindings WHERE installation_id=?`, installation)
	platformTargetMakeLegacy(t, s, id, installation, platformPrincipalsBoth)
	if a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{
		identity: state.identity, resources: state.resources, credential: "upstream-api", decrypt: true, destination: platformTargetDefaultURL,
	}); err != nil {
		t.Fatal("whole-policy binding refused with a destination:", err)
	} else {
		a.Close()
	}
	before = platformCredentialOpenCount.Load()
	if a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{
		identity: state.identity, resources: state.resources, credential: "upstream-api", decrypt: true,
	}); err == nil {
		a.Close()
		t.Fatal("a whole-policy binding opened with no destination")
	}
	if platformCredentialOpenCount.Load() != before {
		t.Fatal("a whole-policy load with no destination reached the custody")
	}
}

// The commit audit and the binding list say which target a binding is for.
func testPlatformTargetBindingShowsTarget(t *testing.T, pg bool) {
	s, admin, installation, credential := platformSecretBindingFixture(t, pg)
	binding := platformTargetBind(t, s, admin, installation, "api-a", credential.ID, "", "")
	sha := platformTargetPolicy(t, s, binding.ID).networkTargetSHA256
	detail := scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.secret_binding_active' AND target_id=?`, binding.ID)
	if !strings.Contains(detail, `"NetworkTargetSHA256":"`+sha+`"`) || !strings.Contains(detail, `"NetworkTarget":"https://api.example.com"`) {
		t.Fatal("commit audit lacks the target", detail)
	}
	list, err := s.listPlatformSecretBindings(admin, installation)
	if err != nil || len(list) != 1 || list[0].NetworkTargetSHA256 != sha || list[0].NetworkTarget != "https://api.example.com" {
		t.Fatal("list lacks the target", list, err)
	}
	// A legacy whole-policy binding names no target.
	platformTargetMakeLegacy(t, s, binding.ID, installation, "")
	list, err = s.listPlatformSecretBindings(admin, installation)
	if err != nil || len(list) != 1 || list[0].NetworkTargetSHA256 != "" || list[0].NetworkTarget != "" {
		t.Fatal("legacy binding lists a target", list, err)
	}
	targets, err := s.listPlatformSecretBindingTargets(admin, installation)
	if err != nil || len(targets) != 1 || targets[0].ID != "default" || targets[0].Origin != "https://api.example.com" {
		t.Fatal("grant targets", targets, err)
	}
	platformTargetSetGrant(t, s, installation, platformTargetV2(t, platformTargetDefault(), platformTargetLAN(), platformTargetOther()))
	if targets, err = s.listPlatformSecretBindingTargets(admin, installation); err != nil || len(targets) != 2 || targets[0].ID != "default" || targets[1].ID != "other" {
		t.Fatal("plain-http target offered", targets, err)
	}
}

func TestPlatformTargetBindingNeedsDestinationSQLite(t *testing.T) {
	testPlatformTargetBindingNeedsDestination(t, false)
}
func TestPlatformTargetBindingNeedsDestinationPostgres(t *testing.T) {
	testPlatformTargetBindingNeedsDestination(t, true)
}
func TestPlatformTargetBindingShowsTargetSQLite(t *testing.T) {
	testPlatformTargetBindingShowsTarget(t, false)
}
func TestPlatformTargetBindingShowsTargetPostgres(t *testing.T) {
	testPlatformTargetBindingShowsTarget(t, true)
}
